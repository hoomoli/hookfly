package store

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/domain"
)

func TestCanonicalIngestNamespacesProviderDeliveryIDsAndRoundTripsDeferredEvents(t *testing.T) {
	s := openTestStore(t)
	deliveryID := "provider-delivery-uuid"
	gitLab := canonicalCommand("gitlab", "production", "group/project", deliveryID)
	gitHub := canonicalCommand("github", "production", "owner/repository", deliveryID)
	first, err := s.Ingest(context.Background(), gitLab)
	if err != nil || first.Duplicate {
		t.Fatalf("GitLab ingest = %#v, %v", first, err)
	}
	second, err := s.Ingest(context.Background(), gitHub)
	if err != nil || second.Duplicate || second.EventID == first.EventID {
		t.Fatalf("GitHub ingest = %#v, %v", second, err)
	}
	assertRowCount(t, s, "events", 2)

	deferred := canonicalCommand("github", "staging", "owner/queued", "queued-delivery")
	deferred.RoutingResult = domain.RoutingDeferred
	deferred.CanonicalEvent = domain.CanonicalEvent{
		Provider: "github", Source: "staging", Repository: "owner/queued", Event: "push",
		Ref: "refs/heads/main", Status: "success", Revision: "a1b2c3", ExternalID: "run-42", Trigger: "push",
	}
	if _, err := s.IngestDeferred(context.Background(), deferred); err != nil {
		t.Fatal(err)
	}
	head, found, err := s.PeekDeferred(context.Background())
	if err != nil || !found {
		t.Fatalf("PeekDeferred() = %#v, %v, %v", head, found, err)
	}
	if head.DeliveryID != deferred.DeliveryID || !reflect.DeepEqual(head.CanonicalEvent, deferred.CanonicalEvent) {
		t.Fatalf("deferred canonical event = %#v", head)
	}
	for _, forbidden := range []string{"token", "signature"} {
		if contains(string(head.HeadersJSON), forbidden) {
			t.Fatalf("stored safe headers include %q: %s", forbidden, head.HeadersJSON)
		}
	}
	if string(head.PayloadJSON) != string(deferred.PayloadJSON) {
		t.Fatalf("deferred payload = %s", head.PayloadJSON)
	}
}

func TestIngestDoesNotPersistSecretOrSignatureHeaders(t *testing.T) {
	s := openTestStore(t)
	command := canonicalCommand("github", "production", "owner/repository", "delivery-with-headers")
	command.HeadersJSON = []byte(`{"X-Request-ID":"safe","X-Gitlab-Token":"secret","X-Hub-Signature-256":"signature"}`)
	result, err := s.Ingest(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	var headers string
	if err := s.db.QueryRow("SELECT raw_headers_json FROM events WHERE id = ?", result.EventID).Scan(&headers); err != nil {
		t.Fatal(err)
	}
	if !contains(headers, "X-Request-ID") || contains(headers, "Token") || contains(headers, "Signature") || contains(headers, "secret") || contains(headers, "signature") {
		t.Fatalf("stored headers = %s", headers)
	}
}

func TestIngestRejectsEmptyCanonicalIdentityBeforePersistence(t *testing.T) {
	for _, field := range []string{"source", "event", "delivery"} {
		t.Run(field, func(t *testing.T) {
			s := openTestStore(t)
			command := canonicalCommand("github", "source", "", "delivery")
			switch field {
			case "source":
				command.CanonicalEvent.Source = ""
			case "event":
				command.CanonicalEvent.Event = ""
			case "delivery":
				command.DeliveryID = ""
			}
			if _, err := s.Ingest(context.Background(), command); err == nil {
				t.Fatal("Ingest() accepted empty canonical identity")
			}
			assertRowCount(t, s, "events", 0)
		})
	}
}

func TestIngestUnsupportedEventIsFinalWithoutQueueEntry(t *testing.T) {
	s := openTestStore(t)
	command := canonicalCommand("github", "source", "", "unsupported-delivery")
	command.RoutingResult = domain.RoutingUnsupported
	result, err := s.Ingest(context.Background(), command)
	if err != nil || result.Duplicate {
		t.Fatalf("Ingest() = %#v, %v", result, err)
	}
	if queued, err := s.CountQueued(context.Background()); err != nil || queued != 0 {
		t.Fatalf("CountQueued() = %d, %v", queued, err)
	}
	var routing string
	if err := s.db.QueryRow("SELECT routing_result FROM events WHERE id = ?", result.EventID).Scan(&routing); err != nil {
		t.Fatal(err)
	}
	if routing != string(domain.RoutingUnsupported) {
		t.Fatalf("routing_result = %q", routing)
	}
}

func TestIngestRejectsDeferredWithoutStrandingCrossEntryRetry(t *testing.T) {
	s := openTestStore(t)
	command := canonicalCommand("github", "source", "repository", "cross-entry-deferred")
	command.RoutingResult = domain.RoutingDeferred

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Ingest(canceled, command); err == nil || !strings.Contains(err.Error(), "direct ingest cannot persist deferred routing result") {
		t.Fatalf("Ingest() error = %v, want pre-transaction deferred rejection", err)
	}
	assertRowCount(t, s, "events", 0)

	first, err := s.IngestDeferred(context.Background(), command)
	if err != nil || first.Duplicate || first.EventID == "" {
		t.Fatalf("first IngestDeferred() = %#v, %v", first, err)
	}
	duplicate, err := s.IngestDeferred(context.Background(), command)
	if err != nil || !duplicate.Duplicate || duplicate.EventID != first.EventID {
		t.Fatalf("duplicate IngestDeferred() = %#v, %v", duplicate, err)
	}
	assertRowCount(t, s, "events", 1)
	assertRowCount(t, s, "routing_queue", 1)
	assertRowCount(t, s, "deliveries", 0)
}

func TestIngestRejectsDeliveriesForNonDeployRoutingResults(t *testing.T) {
	for _, test := range []struct {
		name          string
		routingResult domain.RoutingResult
		deferred      bool
	}{
		{name: "unsupported", routingResult: domain.RoutingUnsupported},
		{name: "unmatched", routingResult: domain.RoutingUnmatched},
		{name: "record only", routingResult: domain.RoutingRecordOnly},
		{name: "deferred", routingResult: domain.RoutingDeferred, deferred: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := openTestStore(t)
			command := canonicalCommand("github", "source", "repository", "contradictory-"+test.name)
			command.RoutingResult = test.routingResult
			command.Deliveries = []domain.NewDelivery{{TargetID: "production"}}
			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			var err error
			if test.deferred {
				_, err = s.IngestDeferred(canceled, command)
			} else {
				_, err = s.Ingest(canceled, command)
			}
			if err == nil || !strings.Contains(err.Error(), "deliveries require deploy routing result") {
				t.Fatalf("ingest error = %v, want pre-transaction delivery rejection for %q", err, test.routingResult)
			}
			assertRowCount(t, s, "events", 0)
			assertRowCount(t, s, "routing_queue", 0)
			assertRowCount(t, s, "deliveries", 0)
		})
	}
}

func TestEventsCanonicalIdentityChecksRejectEmptyValuesAndAllowEmptyRepository(t *testing.T) {
	s := openTestStore(t)
	for _, field := range []string{"provider", "source_id", "event", "delivery_id"} {
		t.Run(field, func(t *testing.T) {
			values := map[string]string{"provider": "github", "source_id": "source", "repository_id": "", "delivery_id": "delivery", "event": "push"}
			values[field] = ""
			if _, err := s.db.Exec(`INSERT INTO events (id, provider, source_id, repository_id, delivery_id, received_at, event, routing_result) VALUES (?, ?, ?, ?, ?, 1, ?, 'unmatched')`, field, values["provider"], values["source_id"], values["repository_id"], values["delivery_id"], values["event"]); err == nil {
				t.Fatalf("empty %s was accepted", field)
			}
		})
	}
	if _, err := s.db.Exec(`INSERT INTO events (id, provider, source_id, repository_id, delivery_id, received_at, event, routing_result) VALUES ('empty-repository', 'github', 'source', '', 'delivery', 1, 'push', 'unmatched')`); err != nil {
		t.Fatalf("empty repository was rejected: %v", err)
	}
}

func canonicalCommand(provider, source, repository, deliveryID string) domain.IngestCommand {
	return domain.IngestCommand{
		DeliveryID: deliveryID,
		CanonicalEvent: domain.CanonicalEvent{
			Provider: provider, Source: source, Repository: repository, Event: "pipeline",
			Ref: "refs/heads/main", Status: "success", Revision: "deadbeef", ExternalID: "external-1", Trigger: "push",
		},
		ReceivedAt: time.UnixMilli(1_725_000_000_000).UTC(), RoutingResult: domain.RoutingRecordOnly,
		HeadersJSON: []byte(`{"X-Gitlab-Event":"Pipeline Hook"}`), PayloadJSON: []byte(`{"object_kind":"pipeline"}`),
	}
}

func TestIngestIsAtomicAndIdempotent(t *testing.T) {
	s := openTestStore(t)
	first, err := s.Ingest(context.Background(), ingestParams("event-key", []domain.NewDelivery{{TargetID: "a"}, {TargetID: "b"}}))
	if err != nil || first.Duplicate {
		t.Fatalf("first ingest: %#v %v", first, err)
	}
	second, err := s.Ingest(context.Background(), ingestParams("event-key", nil))
	if err != nil || !second.Duplicate || second.EventID != first.EventID {
		t.Fatalf("duplicate: %#v %v", second, err)
	}
	assertRowCount(t, s, "events", 1)
	assertRowCount(t, s, "deliveries", 2)
	assertRowCount(t, s, "delivery_attempts", 2)
}

func TestOpenEnablesWALAndForeignKeys(t *testing.T) {
	s := openTestStore(t)
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	var foreignKeys int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
}

func TestIngestRecordsEventWithoutDeliveries(t *testing.T) {
	s := openTestStore(t)
	result, err := s.Ingest(context.Background(), ingestParams("record-only", nil))
	if err != nil || result.Duplicate || result.EventID == "" {
		t.Fatalf("record-only ingest: %#v %v", result, err)
	}
	assertRowCount(t, s, "events", 1)
	assertRowCount(t, s, "deliveries", 0)
	assertRowCount(t, s, "delivery_attempts", 0)
}

func TestDeliveryTargetIsUniquePerEvent(t *testing.T) {
	s := openTestStore(t)
	result, err := s.Ingest(context.Background(), ingestParams("unique-target", []domain.NewDelivery{{TargetID: "a"}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO deliveries (id, event_id, target_id) VALUES ('other', ?, 'a')", result.EventID); err == nil {
		t.Fatal("duplicate target insertion succeeded")
	}
}

func TestIngestRollsBackRelatedRowsWhenTargetIsDuplicated(t *testing.T) {
	s := openTestStore(t)
	_, err := s.Ingest(context.Background(), ingestParams("rollback", []domain.NewDelivery{{TargetID: "a"}, {TargetID: "a"}}))
	if err == nil {
		t.Fatal("duplicate targets were accepted")
	}
	assertRowCount(t, s, "events", 0)
	assertRowCount(t, s, "deliveries", 0)
	assertRowCount(t, s, "delivery_attempts", 0)
}

func TestIngestCreatesPendingInitialAttempt(t *testing.T) {
	s := openTestStore(t)
	_, err := s.Ingest(context.Background(), ingestParams("initial-attempt", []domain.NewDelivery{{TargetID: "a"}}))
	if err != nil {
		t.Fatal(err)
	}
	var currentAttemptID, kind, transportStatus, deploymentStatus string
	if err := s.db.QueryRow(`
		SELECT d.current_attempt_id, a.kind, a.transport_status, a.deployment_status
		FROM deliveries d JOIN delivery_attempts a ON a.id = d.current_attempt_id`).Scan(
		&currentAttemptID, &kind, &transportStatus, &deploymentStatus,
	); err != nil {
		t.Fatal(err)
	}
	if currentAttemptID == "" || kind != "initial" || transportStatus != "pending" || deploymentStatus != "not_started" {
		t.Fatalf("initial attempt = id=%q kind=%q transport=%q deployment=%q", currentAttemptID, kind, transportStatus, deploymentStatus)
	}
}

func TestCurrentAttemptMustBelongToDelivery(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.Ingest(context.Background(), ingestParams("current-attempt-owner", []domain.NewDelivery{{TargetID: "a"}, {TargetID: "b"}})); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.Query(`SELECT id, current_attempt_id FROM deliveries ORDER BY target_id`)
	if err != nil {
		t.Fatal(err)
	}
	var firstDeliveryID, firstAttemptID, secondAttemptID string
	if !rows.Next() {
		t.Fatal("missing first delivery")
	}
	if err := rows.Scan(&firstDeliveryID, &firstAttemptID); err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatal("missing second delivery")
	}
	var secondDeliveryID string
	if err := rows.Scan(&secondDeliveryID, &secondAttemptID); err != nil {
		t.Fatal(err)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if firstAttemptID == secondAttemptID || firstDeliveryID == secondDeliveryID {
		t.Fatal("expected distinct deliveries and attempts")
	}
	if _, err := s.db.Exec("UPDATE deliveries SET current_attempt_id = 'missing' WHERE id = ?", firstDeliveryID); err == nil {
		t.Fatal("nonexistent current attempt was accepted")
	}
	if _, err := s.db.Exec("UPDATE deliveries SET current_attempt_id = ? WHERE id = ?", secondAttemptID, firstDeliveryID); err == nil {
		t.Fatal("cross-delivery current attempt was accepted")
	}
}

func TestDeletingEventCascadesRelatedRows(t *testing.T) {
	s := openTestStore(t)
	result, err := s.Ingest(context.Background(), ingestParams("cascade", []domain.NewDelivery{{TargetID: "a"}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO audit_logs (id, event_id, action, created_at) VALUES ('audit', ?, 'created', 1)", result.EventID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DELETE FROM events WHERE id = ?", result.EventID); err != nil {
		t.Fatal(err)
	}
	assertRowCount(t, s, "deliveries", 0)
	assertRowCount(t, s, "delivery_attempts", 0)
	assertRowCount(t, s, "audit_logs", 0)
}

func TestIngestSurvivesStoreReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hookfly.db")
	firstStore, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := firstStore.Ingest(context.Background(), ingestParams("durable", nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}
	secondStore, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	second, err := secondStore.Ingest(context.Background(), ingestParams("durable", nil))
	if err != nil || !second.Duplicate || second.EventID != first.EventID {
		t.Fatalf("reopened duplicate: %#v %v", second, err)
	}
	assertRowCount(t, secondStore, "events", 1)
}

func ingestParams(key string, deliveries []domain.NewDelivery) domain.IngestCommand {
	return domain.IngestCommand{
		DeliveryID: key,
		CanonicalEvent: domain.CanonicalEvent{
			Provider: "gitlab", Source: "legacy", Repository: "42", Event: "pipeline",
			Ref: "main", Status: "success", ExternalID: "99", Trigger: "push", Revision: "abcdef",
		},
		ReceivedAt: time.UnixMilli(1_725_000_000_000).UTC(), RoutingResult: domain.RoutingDeploy,
		RuleID: "deploy-main", RuleSnapshot: []byte(`{"id":"deploy-main"}`), ConfigDigest: "config-digest",
		HeadersJSON: []byte(`{"X-Gitlab-Event":"Pipeline Hook"}`), PayloadJSON: []byte(`{"object_kind":"pipeline"}`),
		Deliveries: deliveries,
	}
}
