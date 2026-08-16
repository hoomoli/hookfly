package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/domain"
)

func TestManualCorrectionInvalidatesAnEarlierPollClaim(t *testing.T) {
	// Break caught: an old poll completion overwriting a reconciliation result after a human inspected the same current attempt.
	s := openTestStore(t)
	result, err := s.Ingest(context.Background(), ingestParams("manual-poll-cas", []domain.NewDelivery{{TargetID: "target"}}))
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(10_000).UTC()
	var deliveryID, attemptID string
	if err := s.db.QueryRow(`SELECT id, current_attempt_id FROM deliveries WHERE event_id = ?`, result.EventID).Scan(&deliveryID, &attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE delivery_attempts SET transport_status = 'enqueued', deployment_status = 'locating', poll_due_at = ?, monitoring_deadline_at = ? WHERE id = ?`, now.UnixMilli(), now.Add(time.Minute).UnixMilli(), attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE deliveries SET transport_status = 'enqueued', deployment_status = 'locating' WHERE id = ?`, deliveryID); err != nil {
		t.Fatal(err)
	}
	claims, err := s.ListDuePolls(context.Background(), now)
	if err != nil || len(claims) != 1 {
		t.Fatalf("ListDuePolls() = %#v/%v", claims, err)
	}
	current, err := s.CurrentAttempt(context.Background(), deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	pollDue, deadline := now.Add(time.Second), now.Add(time.Minute)
	if _, err := s.ApplyManualUpdate(context.Background(), ManualUpdate{
		Expected: current, Correct: true, TransportStatus: domain.TransportEnqueued, DeploymentStatus: domain.DeploymentRunning,
		DeploymentID: "remote", PollDue: &pollDue, MonitoringDeadline: &deadline, ActorID: "alice", Reason: "fresh remote state", At: now,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.CompletePoll(context.Background(), claims[0], PollUpdate{TransportStatus: "enqueued", DeploymentStatus: "done"}, now)
	if !errors.Is(err, ErrPollNotClaimed) {
		t.Fatalf("CompletePoll() error = %v, want %v", err, ErrPollNotClaimed)
	}
}

func TestApplyManualUpdatePropagatesReadFailure(t *testing.T) {
	// Break caught: a corrupted storage query being reported as a normal current-attempt conflict.
	s := openTestStore(t)
	if _, err := s.db.Exec(`DROP TABLE deliveries`); err != nil {
		t.Fatal(err)
	}
	_, err := s.ApplyManualUpdate(context.Background(), ManualUpdate{Expected: CurrentAttempt{DeliveryID: "delivery", AttemptID: "attempt"}})
	if err == nil || errors.Is(err, ErrCurrentAttemptChanged) {
		t.Fatalf("ApplyManualUpdate() error = %v, want propagated storage failure", err)
	}
}

func TestApplyManualUpdateRejectsUnsupportedOperationKind(t *testing.T) {
	// Break caught: a caller persisting an arbitrary attempt kind outside the approved initial/retry/redeploy vocabulary.
	s := openTestStore(t)
	result, err := s.Ingest(context.Background(), ingestParams("manual-kind", []domain.NewDelivery{{TargetID: "target"}}))
	if err != nil {
		t.Fatal(err)
	}
	var deliveryID string
	if err := s.db.QueryRow(`SELECT id FROM deliveries WHERE event_id = ?`, result.EventID).Scan(&deliveryID); err != nil {
		t.Fatal(err)
	}
	current, err := s.CurrentAttempt(context.Background(), deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ApplyManualUpdate(context.Background(), ManualUpdate{Expected: current, Create: true, Operation: domain.Operation("manual"), At: time.Now()})
	if err == nil {
		t.Fatal("ApplyManualUpdate accepted an unsupported operation")
	}
	var attempts int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM delivery_attempts WHERE delivery_id = ?`, deliveryID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("attempt rows = %d, want 1", attempts)
	}
}
