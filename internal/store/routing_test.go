package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/domain"
)

func TestDeferredRoutingUsesDurableAcceptanceSequenceAndIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	second := deferredCommand("second", time.UnixMilli(1))
	secondResult, err := s.IngestDeferred(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	first := deferredCommand("first", time.UnixMilli(9999))
	if _, err := s.IngestDeferred(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.IngestDeferred(context.Background(), second)
	if err != nil || !duplicate.Duplicate || duplicate.EventID != secondResult.EventID {
		t.Fatalf("duplicate = %#v, %v", duplicate, err)
	}

	head, ok, err := s.PeekDeferred(context.Background())
	if err != nil || !ok {
		t.Fatalf("peek = %#v, %v, %v", head, ok, err)
	}
	if head.EventID != secondResult.EventID || head.DeliveryID != "second" {
		t.Fatalf("head = %#v", head)
	}
	if count, err := s.CountQueued(context.Background()); err != nil || count != 2 {
		t.Fatalf("queued = %d, %v", count, err)
	}
	assertRowCount(t, s, "deliveries", 0)
}

func TestFinalizeDeferredAtomicallyCreatesInitialDeliveriesAndRemovesQueueHead(t *testing.T) {
	s := openTestStore(t)
	result, err := s.IngestDeferred(context.Background(), deferredCommand("finalize", time.UnixMilli(10)))
	if err != nil {
		t.Fatal(err)
	}
	head, ok, err := s.PeekDeferred(context.Background())
	if err != nil || !ok {
		t.Fatalf("peek = %#v, %v, %v", head, ok, err)
	}
	changed, err := s.FinalizeDeferred(context.Background(), head, domain.RouteDecision{
		RoutingResult: domain.RoutingDeploy,
		RuleID:        "new-rule",
		RuleSnapshot:  []byte(`{"id":"new-rule"}`),
		Deliveries: []domain.NewDelivery{
			{TargetID: "a", TargetSnapshot: []byte(`{"binding_version":2}`)},
			{TargetID: "b", TargetSnapshot: []byte(`{"binding_version":2}`)},
		},
	}, "new-digest")
	if err != nil || !changed {
		t.Fatalf("FinalizeDeferred() = %v, %v", changed, err)
	}
	var routing, ruleID, digest string
	if err := s.db.QueryRow("SELECT routing_result, rule_id, config_digest FROM events WHERE id = ?", result.EventID).Scan(&routing, &ruleID, &digest); err != nil {
		t.Fatal(err)
	}
	if routing != "deploy" || ruleID != "new-rule" || digest != "new-digest" {
		t.Fatalf("event = %q/%q/%q", routing, ruleID, digest)
	}
	assertRowCount(t, s, "routing_queue", 0)
	assertRowCount(t, s, "deliveries", 2)
	assertRowCount(t, s, "delivery_attempts", 2)

	changed, err = s.FinalizeDeferred(context.Background(), head, domain.RouteDecision{RoutingResult: domain.RoutingUnmatched}, "other")
	if err != nil || changed {
		t.Fatalf("second FinalizeDeferred() = %v, %v", changed, err)
	}
}

func TestFinalizeDeferredFailureLeavesEventAndQueueUnchanged(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.IngestDeferred(context.Background(), deferredCommand("rollback", time.UnixMilli(10))); err != nil {
		t.Fatal(err)
	}
	head, _, err := s.PeekDeferred(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.FinalizeDeferred(context.Background(), head, domain.RouteDecision{
		RoutingResult: domain.RoutingDeploy,
		Deliveries:    []domain.NewDelivery{{TargetID: "same"}, {TargetID: "same"}},
	}, "digest")
	if err == nil {
		t.Fatal("expected duplicate target failure")
	}
	var routing string
	if err := s.db.QueryRow("SELECT routing_result FROM events WHERE id = ?", head.EventID).Scan(&routing); err != nil {
		t.Fatal(err)
	}
	if routing != "deferred" {
		t.Fatalf("routing_result = %q", routing)
	}
	assertRowCount(t, s, "routing_queue", 1)
	assertRowCount(t, s, "deliveries", 0)
}

func TestFinalizeDeferredRejectsDeliveriesForNonDeployResult(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.IngestDeferred(context.Background(), deferredCommand("contradictory-finalize", time.UnixMilli(10))); err != nil {
		t.Fatal(err)
	}
	head, found, err := s.PeekDeferred(context.Background())
	if err != nil || !found {
		t.Fatalf("PeekDeferred() = %#v, %v, %v", head, found, err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	changed, err := s.FinalizeDeferred(canceled, head, domain.RouteDecision{
		RoutingResult: domain.RoutingRecordOnly,
		Deliveries:    []domain.NewDelivery{{TargetID: "production"}},
	}, "digest")
	if err == nil || !strings.Contains(err.Error(), "deliveries require deploy routing result") || changed {
		t.Fatalf("FinalizeDeferred() = %v, %v; want pre-transaction atomic rejection", changed, err)
	}
	var routing string
	if err := s.db.QueryRow("SELECT routing_result FROM events WHERE id = ?", head.EventID).Scan(&routing); err != nil {
		t.Fatal(err)
	}
	if routing != string(domain.RoutingDeferred) {
		t.Fatalf("routing_result = %q, want %q", routing, domain.RoutingDeferred)
	}
	assertRowCount(t, s, "routing_queue", 1)
	assertRowCount(t, s, "deliveries", 0)
}

func TestPeekDeferredRecoversNullableCanonicalFields(t *testing.T) {
	s := openTestStore(t)
	command := canonicalCommand("github", "source", "repository", "nullable-fields")
	command.RoutingResult = domain.RoutingDeferred
	result, err := s.IngestDeferred(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE events SET ref = NULL, status = NULL, revision = NULL, external_id = NULL, trigger = NULL WHERE id = ?`, result.EventID); err != nil {
		t.Fatal(err)
	}
	head, found, err := s.PeekDeferred(context.Background())
	if err != nil || !found {
		t.Fatalf("PeekDeferred() = %#v, %v, %v", head, found, err)
	}
	if head.CanonicalEvent.Ref != "" || head.CanonicalEvent.Status != "" || head.CanonicalEvent.Revision != "" || head.CanonicalEvent.ExternalID != "" || head.CanonicalEvent.Trigger != "" {
		t.Fatalf("nullable canonical event = %#v", head.CanonicalEvent)
	}
}

func TestCountActiveUsesCurrentAttemptAuthoritativePredicate(t *testing.T) {
	tests := []struct {
		transport, deployment string
		active                bool
	}{
		{"pending", "not_started", true},
		{"sending", "not_started", true},
		{"enqueued", "locating", true},
		{"enqueued", "running", true},
		{"enqueued", "unrecognized", true},
		{"failed", "not_started", false},
		{"enqueued", "done", false},
		{"unknown", "unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.transport+"_"+tt.deployment, func(t *testing.T) {
			s := openTestStore(t)
			seedActiveBinding(t, s, "event", "delivery", "attempt", tt.transport, tt.deployment, []byte(`{"binding_version":2}`))
			got, err := s.CountActive(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if tt.active {
				want = 1
			}
			if got != want {
				t.Fatalf("CountActive() = %d, want %d", got, want)
			}
		})
	}
}

func deferredCommand(key string, receivedAt time.Time) domain.IngestCommand {
	command := ingestParams(key, nil)
	command.ReceivedAt = receivedAt
	command.CanonicalEvent.Repository = key
	command.RoutingResult = domain.RoutingDeferred
	command.RuleID = ""
	command.RuleSnapshot = nil
	command.ConfigDigest = ""
	return command
}
