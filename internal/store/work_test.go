package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/domain"
)

func TestClaimPendingAttemptClaimsExistingInitialOnceAndAudits(t *testing.T) {
	// Break caught: creating a second initial attempt or claiming the current pending attempt more than once.
	s := openTestStore(t)
	result, err := s.Ingest(context.Background(), ingestParams("claim-once", []domain.NewDelivery{{TargetID: "target-a"}}))
	if err != nil {
		t.Fatal(err)
	}
	claimedAt := time.UnixMilli(1_725_000_001_000).UTC()
	work, claimed, err := s.ClaimPendingAttempt(context.Background(), claimedAt)
	if err != nil || !claimed {
		t.Fatalf("ClaimPendingAttempt() = %#v/%v/%v", work, claimed, err)
	}
	if work.EventID != result.EventID || work.DeliveryID == "" || work.AttemptID == "" || work.TargetID != "target-a" {
		t.Fatalf("work = %#v", work)
	}
	if _, claimed, err := s.ClaimPendingAttempt(context.Background(), claimedAt.Add(time.Second)); err != nil || claimed {
		t.Fatalf("second claim = %v/%v", claimed, err)
	}
	assertRowCount(t, s, "delivery_attempts", 1)

	var attemptTransport, deliveryTransport string
	var requestAtMissing int
	if err := s.db.QueryRow(`
		SELECT a.transport_status, d.transport_status, CASE WHEN a.request_at IS NULL THEN 1 ELSE 0 END
		FROM delivery_attempts a JOIN deliveries d ON d.current_attempt_id = a.id
		WHERE a.id = ?`, work.AttemptID).Scan(&attemptTransport, &deliveryTransport, &requestAtMissing); err != nil {
		t.Fatal(err)
	}
	if attemptTransport != "sending" || deliveryTransport != "sending" || requestAtMissing != 1 {
		t.Fatalf("claimed state = %s/%s request_at_missing=%d", attemptTransport, deliveryTransport, requestAtMissing)
	}
	assertAuditTransition(t, s, work.AttemptID, "transport_sending", "pending", "sending")
}

func TestRecordDeploymentRequestPersistsOnlyWhileAttemptIsSending(t *testing.T) {
	// Break caught: losing pre-POST evidence on interruption, or rewriting evidence after the attempt leaves sending.
	s := openTestStore(t)
	result, err := s.Ingest(context.Background(), ingestParams("record-deployment-request", []domain.NewDelivery{{TargetID: "target-a"}}))
	if err != nil {
		t.Fatal(err)
	}
	work, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(2000))
	if err != nil || !claimed || work.EventID != result.EventID {
		t.Fatalf("ClaimPendingAttempt() = %#v/%v/%v", work, claimed, err)
	}

	request := []byte(`{"version":1,"method":"POST","url":"https://dokploy.example.invalid/api/compose.deploy","headers":{"Content-Type":"application/json"},"body":{"composeId":"compose-1"}}`)
	requestAt := time.UnixMilli(2500).UTC()
	if err := s.RecordDeploymentRequest(context.Background(), work.AttemptID, request, requestAt); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	var storedAt int64
	if err := s.db.QueryRow(`SELECT request_json, request_at FROM delivery_attempts WHERE id = ?`, work.AttemptID).Scan(&stored, &storedAt); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, request) || storedAt != requestAt.UnixMilli() {
		t.Fatalf("request_json/request_at = %s/%d", stored, storedAt)
	}
	if err := s.RecordDeploymentRequest(context.Background(), work.AttemptID, []byte(`{"version":1,"method":"DELETE"}`), requestAt.Add(time.Second)); !errors.Is(err, ErrAttemptNotSending) {
		t.Fatalf("second RecordDeploymentRequest() = %v", err)
	}

	if err := s.MarkTransportFailed(context.Background(), work.AttemptID, request, []byte(`{"status_code":500}`), time.UnixMilli(3000)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDeploymentRequest(context.Background(), work.AttemptID, []byte(`{"version":1,"method":"DELETE"}`), requestAt.Add(2*time.Second)); !errors.Is(err, ErrAttemptNotSending) {
		t.Fatalf("RecordDeploymentRequest() after terminal state = %v", err)
	}
}

func TestClaimPendingAttemptPreservesTargetReceiveOrder(t *testing.T) {
	// Break caught: a later delivery for one target overtaking its already-sending predecessor.
	s := openTestStore(t)
	firstCommand := ingestParams("received-first", []domain.NewDelivery{{TargetID: "target-a"}})
	firstCommand.ReceivedAt = time.UnixMilli(1000)
	first, err := s.Ingest(context.Background(), firstCommand)
	if err != nil {
		t.Fatal(err)
	}
	secondCommand := ingestParams("received-second", []domain.NewDelivery{{TargetID: "target-a", DispatchKey: "new-compose-fingerprint"}})
	secondCommand.ReceivedAt = time.UnixMilli(2000)
	second, err := s.Ingest(context.Background(), secondCommand)
	if err != nil {
		t.Fatal(err)
	}

	firstWork, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(3000))
	if err != nil || !claimed || firstWork.EventID != first.EventID {
		t.Fatalf("first claim = %#v/%v/%v", firstWork, claimed, err)
	}
	if work, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(3001)); err != nil || claimed {
		t.Fatalf("same target claimed while sending = %#v/%v/%v", work, claimed, err)
	}
	if err := s.SetDeploymentCursor(context.Background(), firstWork.AttemptID, "before-first"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkEnqueued(context.Background(), firstWork.AttemptID, []byte(`{"composeId":"one"}`), []byte(`true`), time.UnixMilli(4000), time.UnixMilli(5000), time.UnixMilli(9000)); err != nil {
		t.Fatal(err)
	}
	if work, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(4001)); err != nil || claimed {
		t.Fatalf("same target claimed while first deployment is unbound = %#v/%v/%v", work, claimed, err)
	}
	if _, err := s.db.Exec(`UPDATE delivery_attempts SET deployment_id = 'deployment-first' WHERE id = ?`, firstWork.AttemptID); err != nil {
		t.Fatal(err)
	}
	secondWork, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(4001))
	if err != nil || !claimed || secondWork.EventID != second.EventID {
		t.Fatalf("second claim = %#v/%v/%v", secondWork, claimed, err)
	}
}

func TestClaimPendingAttemptSerializesDifferentTargetsWithOneDispatchKeyUntilBound(t *testing.T) {
	// Break caught: two target aliases for one remote Compose enqueueing while neither can be correlated safely.
	s := openTestStore(t)
	command := ingestParams("shared-compose", []domain.NewDelivery{
		{TargetID: "alias-a", DispatchKey: "dokploy-compose-fingerprint"},
		{TargetID: "alias-b", DispatchKey: "dokploy-compose-fingerprint"},
	})
	if _, err := s.Ingest(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	first, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(2000))
	if err != nil || !claimed {
		t.Fatalf("first claim = %#v/%v/%v", first, claimed, err)
	}
	if err := s.SetDeploymentCursor(context.Background(), first.AttemptID, "cursor"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkEnqueued(context.Background(), first.AttemptID, []byte(`{}`), []byte(`true`), time.UnixMilli(2100), time.UnixMilli(3000), time.UnixMilli(9000)); err != nil {
		t.Fatal(err)
	}
	if work, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(2200)); err != nil || claimed {
		t.Fatalf("shared Compose claimed while unbound = %#v/%v/%v", work, claimed, err)
	}
	if _, err := s.db.Exec(`UPDATE delivery_attempts SET deployment_id = 'deployment-first' WHERE id = ?`, first.AttemptID); err != nil {
		t.Fatal(err)
	}
	second, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(2300))
	if err != nil || !claimed || second.TargetID == first.TargetID {
		t.Fatalf("second claim after binding = %#v/%v/%v", second, claimed, err)
	}
}

func TestDeploymentCursorDistinguishesLegacyNullFromExplicitEmptyHistory(t *testing.T) {
	// Break caught: losing whether preflight completed when Dokploy had no prior Deployment records.
	s := openTestStore(t)
	if _, err := s.Ingest(context.Background(), ingestParams("empty-cursor", []domain.NewDelivery{{TargetID: "target-a", DispatchKey: "compose-a"}})); err != nil {
		t.Fatal(err)
	}
	work, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(2000))
	if err != nil || !claimed {
		t.Fatalf("claim = %#v/%v/%v", work, claimed, err)
	}
	var legacyCursor *string
	if err := s.db.QueryRow(`SELECT deployment_cursor FROM delivery_attempts WHERE id = ?`, work.AttemptID).Scan(&legacyCursor); err != nil {
		t.Fatal(err)
	}
	if legacyCursor != nil {
		t.Fatalf("new attempt cursor before preflight = %q", *legacyCursor)
	}
	if err := s.SetDeploymentCursor(context.Background(), work.AttemptID, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkEnqueued(context.Background(), work.AttemptID, []byte(`{}`), []byte(`true`), time.UnixMilli(2100), time.UnixMilli(3000), time.UnixMilli(9000)); err != nil {
		t.Fatal(err)
	}
	due, err := s.ListDuePolls(context.Background(), time.UnixMilli(3000))
	if err != nil || len(due) != 1 {
		t.Fatalf("ListDuePolls() = %#v/%v", due, err)
	}
	if due[0].DeploymentCursor == nil || *due[0].DeploymentCursor != "" {
		t.Fatalf("explicit empty cursor = %#v", due[0].DeploymentCursor)
	}
}

func TestClaimPendingAttemptRollsBackStateWhenAuditFails(t *testing.T) {
	// Break caught: committing sending state without its audit row.
	s := openTestStore(t)
	if _, err := s.Ingest(context.Background(), ingestParams("claim-audit-rollback", []domain.NewDelivery{{TargetID: "target-a"}})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_audit BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT, 'audit rejected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(2000)); err == nil {
		t.Fatal("claim succeeded despite rejected audit")
	}
	assertCurrentState(t, s, "pending", "not_started")
	assertRowCount(t, s, "audit_logs", 0)
}

func TestAttemptTransitionsUpdateAttemptDeliveryAndAuditAtomically(t *testing.T) {
	// Break caught: divergent attempt/delivery states or missing append-only terminal audit entries.
	tests := []struct {
		name           string
		transition     func(*Store, string) error
		wantTransport  string
		wantDeployment string
		wantAction     string
		wantDeadline   bool
	}{
		{
			name: "enqueued",
			transition: func(s *Store, attemptID string) error {
				return s.MarkEnqueued(context.Background(), attemptID, []byte(`{"composeId":"compose-1"}`), []byte(`{"success":true}`), time.UnixMilli(3000), time.UnixMilli(4000), time.UnixMilli(9000))
			},
			wantTransport: "enqueued", wantDeployment: "locating", wantAction: "transport_enqueued", wantDeadline: true,
		},
		{
			name: "definitive failure",
			transition: func(s *Store, attemptID string) error {
				return s.MarkTransportFailed(context.Background(), attemptID, []byte(`{"composeId":"compose-1"}`), []byte(`{"status_code":500}`), time.UnixMilli(3000))
			},
			wantTransport: "failed", wantDeployment: "not_started", wantAction: "transport_failed",
		},
		{
			name: "uncertain",
			transition: func(s *Store, attemptID string) error {
				return s.MarkTransportUnknown(context.Background(), attemptID, []byte(`{"composeId":"compose-1"}`), []byte(`{"kind":"uncertain"}`), time.UnixMilli(3000), time.UnixMilli(4000), time.UnixMilli(9000))
			},
			wantTransport: "unknown", wantDeployment: "locating", wantAction: "transport_unknown", wantDeadline: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := openTestStore(t)
			if _, err := s.Ingest(context.Background(), ingestParams("transition-"+test.name, []domain.NewDelivery{{TargetID: "target-a"}})); err != nil {
				t.Fatal(err)
			}
			work, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(2000))
			if err != nil || !claimed {
				t.Fatalf("claim = %#v/%v/%v", work, claimed, err)
			}
			if err := test.transition(s, work.AttemptID); err != nil {
				t.Fatal(err)
			}

			var attemptTransport, attemptDeployment, deliveryTransport, deliveryDeployment string
			var requestJSON, responseJSON []byte
			var responseAt int64
			var deadline *int64
			if err := s.db.QueryRow(`
				SELECT a.transport_status, a.deployment_status, d.transport_status, d.deployment_status,
				       a.request_json, a.response_json, a.response_at, a.monitoring_deadline_at
				FROM delivery_attempts a JOIN deliveries d ON d.current_attempt_id = a.id
				WHERE a.id = ?`, work.AttemptID).Scan(
				&attemptTransport, &attemptDeployment, &deliveryTransport, &deliveryDeployment,
				&requestJSON, &responseJSON, &responseAt, &deadline,
			); err != nil {
				t.Fatal(err)
			}
			if attemptTransport != test.wantTransport || deliveryTransport != test.wantTransport ||
				attemptDeployment != test.wantDeployment || deliveryDeployment != test.wantDeployment {
				t.Fatalf("states = attempt %s/%s delivery %s/%s", attemptTransport, attemptDeployment, deliveryTransport, deliveryDeployment)
			}
			if len(requestJSON) == 0 || len(responseJSON) == 0 || responseAt != 3000 {
				t.Fatalf("snapshots/response_at = %q/%q/%d", requestJSON, responseJSON, responseAt)
			}
			if (deadline != nil) != test.wantDeadline {
				t.Fatalf("monitoring deadline = %v", deadline)
			}
			if deadline != nil && *deadline != 9000 {
				t.Fatalf("monitoring deadline = %d", *deadline)
			}
			assertAuditTransition(t, s, work.AttemptID, test.wantAction, "sending", test.wantTransport)
			assertRowCount(t, s, "audit_logs", 2)
		})
	}
}

func TestAttemptTransitionCASAndAuditRollback(t *testing.T) {
	// Break caught: a duplicate completion overwriting an outcome, or a failed audit leaving the state changed.
	s := openTestStore(t)
	if _, err := s.Ingest(context.Background(), ingestParams("transition-cas", []domain.NewDelivery{{TargetID: "target-a"}})); err != nil {
		t.Fatal(err)
	}
	work, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(2000))
	if err != nil || !claimed {
		t.Fatalf("claim = %#v/%v/%v", work, claimed, err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_terminal_audit BEFORE INSERT ON audit_logs WHEN NEW.action = 'transport_enqueued' BEGIN SELECT RAISE(ABORT, 'audit rejected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkEnqueued(context.Background(), work.AttemptID, []byte(`{}`), []byte(`true`), time.UnixMilli(3000), time.UnixMilli(4000), time.UnixMilli(9000)); err == nil {
		t.Fatal("transition succeeded despite rejected audit")
	}
	assertCurrentState(t, s, "sending", "not_started")
	assertRowCount(t, s, "audit_logs", 1)
	if _, err := s.db.Exec(`DROP TRIGGER reject_terminal_audit`); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkTransportFailed(context.Background(), work.AttemptID, []byte(`{}`), []byte(`{"status_code":500}`), time.UnixMilli(3000)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkTransportUnknown(context.Background(), work.AttemptID, []byte(`{}`), []byte(`{}`), time.UnixMilli(4000), time.UnixMilli(5000), time.UnixMilli(9000)); err == nil {
		t.Fatal("duplicate terminal transition succeeded")
	}
	assertCurrentState(t, s, "failed", "not_started")
	assertRowCount(t, s, "audit_logs", 2)
}

func TestListDuePollsClaimsEachCurrentAttemptOnce(t *testing.T) {
	// Break caught: overlapping poll ticks selecting the same due attempt before either HTTP query completes.
	s := openTestStore(t)
	work := preparePollAttempt(t, s, "poll-claim", "target-a", "enqueued", "locating", time.UnixMilli(3000), time.UnixMilli(9000))

	due, err := s.ListDuePolls(context.Background(), time.UnixMilli(3000))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].AttemptID != work.AttemptID || due[0].ClaimedAt.UnixMilli() != 3000 {
		t.Fatalf("first due polls = %#v", due)
	}
	again, err := s.ListDuePolls(context.Background(), time.UnixMilli(3000))
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("overlapping due polls = %#v", again)
	}

	next := time.UnixMilli(4000)
	changed, err := s.CompletePoll(context.Background(), due[0], PollUpdate{
		TransportStatus: "enqueued", DeploymentStatus: "locating", NextPollAt: &next,
	}, time.UnixMilli(3001))
	if err != nil || changed {
		t.Fatalf("unchanged CompletePoll() = %v/%v", changed, err)
	}
	if rows := auditCount(t, s, work.AttemptID); rows != 2 {
		t.Fatalf("audit rows after unchanged poll = %d", rows)
	}
	if due, err := s.ListDuePolls(context.Background(), time.UnixMilli(3999)); err != nil || len(due) != 0 {
		t.Fatalf("early due polls = %#v/%v", due, err)
	}
	if due, err := s.ListDuePolls(context.Background(), next); err != nil || len(due) != 1 {
		t.Fatalf("rescheduled due polls = %#v/%v", due, err)
	}
}

func TestCompletePollUpdatesAttemptDeliveryAndAuditAtomically(t *testing.T) {
	// Break caught: deployment evidence changing only one current-status row, losing its ID, or committing without audit history.
	s := openTestStore(t)
	work := preparePollAttempt(t, s, "poll-transition", "target-a", "unknown", "locating", time.UnixMilli(3000), time.UnixMilli(9000))
	due, err := s.ListDuePolls(context.Background(), time.UnixMilli(3000))
	if err != nil || len(due) != 1 {
		t.Fatalf("ListDuePolls() = %#v/%v", due, err)
	}
	next := time.UnixMilli(4000)
	changed, err := s.CompletePoll(context.Background(), due[0], PollUpdate{
		TransportStatus: "enqueued", DeploymentStatus: "running", DeploymentID: "deployment-1", NextPollAt: &next,
	}, time.UnixMilli(3100))
	if err != nil || !changed {
		t.Fatalf("CompletePoll() = %v/%v", changed, err)
	}
	assertCurrentState(t, s, "enqueued", "running")
	var deploymentID string
	var pollDue int64
	if err := s.db.QueryRow(`SELECT deployment_id, poll_due_at FROM delivery_attempts WHERE id = ?`, work.AttemptID).Scan(&deploymentID, &pollDue); err != nil {
		t.Fatal(err)
	}
	if deploymentID != "deployment-1" || pollDue != next.UnixMilli() {
		t.Fatalf("deployment/poll due = %q/%d", deploymentID, pollDue)
	}
	assertAuditTransition(t, s, work.AttemptID, "deployment_running", "unknown", "enqueued")

	terminalDue, err := s.ListDuePolls(context.Background(), next)
	if err != nil || len(terminalDue) != 1 {
		t.Fatalf("terminal ListDuePolls() = %#v/%v", terminalDue, err)
	}
	changed, err = s.CompletePoll(context.Background(), terminalDue[0], PollUpdate{
		TransportStatus: "enqueued", DeploymentStatus: "done", DeploymentID: "deployment-1",
	}, time.UnixMilli(4100))
	if err != nil || !changed {
		t.Fatalf("terminal CompletePoll() = %v/%v", changed, err)
	}
	var terminalPollDue *int64
	if err := s.db.QueryRow(`SELECT poll_due_at FROM delivery_attempts WHERE id = ?`, work.AttemptID).Scan(&terminalPollDue); err != nil {
		t.Fatal(err)
	}
	if terminalPollDue != nil {
		t.Fatalf("terminal poll_due_at = %v", terminalPollDue)
	}
	assertCurrentState(t, s, "enqueued", "done")
	assertAuditTransition(t, s, work.AttemptID, "deployment_done", "enqueued", "enqueued")
}

func TestCompletePollRollsBackStatusWhenAuditFails(t *testing.T) {
	// Break caught: a status transition surviving when its required audit insertion fails.
	s := openTestStore(t)
	work := preparePollAttempt(t, s, "poll-audit-rollback", "target-a", "enqueued", "locating", time.UnixMilli(3000), time.UnixMilli(9000))
	due, err := s.ListDuePolls(context.Background(), time.UnixMilli(3000))
	if err != nil || len(due) != 1 {
		t.Fatalf("ListDuePolls() = %#v/%v", due, err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_poll_audit BEFORE INSERT ON audit_logs WHEN NEW.action = 'deployment_running' BEGIN SELECT RAISE(ABORT, 'audit rejected'); END`); err != nil {
		t.Fatal(err)
	}
	next := time.UnixMilli(4000)
	if _, err := s.CompletePoll(context.Background(), due[0], PollUpdate{
		TransportStatus: "enqueued", DeploymentStatus: "running", DeploymentID: "deployment-1", NextPollAt: &next,
	}, time.UnixMilli(3100)); err == nil {
		t.Fatal("CompletePoll succeeded despite rejected audit")
	}
	assertCurrentState(t, s, "enqueued", "locating")
	var deploymentID *string
	if err := s.db.QueryRow(`SELECT deployment_id FROM delivery_attempts WHERE id = ?`, work.AttemptID).Scan(&deploymentID); err != nil {
		t.Fatal(err)
	}
	if deploymentID != nil {
		t.Fatalf("deployment ID survived rollback = %q", *deploymentID)
	}
}

func TestRecoverInterruptedPreservesPendingAndResumesActiveWithoutNewAttempts(t *testing.T) {
	// Break caught: restart replaying a pending/sending POST, creating attempts, or discarding stored active deadlines.
	s := openTestStore(t)
	if _, err := s.Ingest(context.Background(), ingestParams("recover-sending", []domain.NewDelivery{{TargetID: "override-target"}})); err != nil {
		t.Fatal(err)
	}
	sending, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(2000))
	if err != nil || !claimed {
		t.Fatalf("sending claim = %#v/%v/%v", sending, claimed, err)
	}
	active := preparePollAttempt(t, s, "recover-active", "active-target", "enqueued", "unrecognized", time.UnixMilli(4500), time.UnixMilli(8000))
	if _, err := s.db.Exec(`UPDATE delivery_attempts SET last_polled_at = 4400 WHERE id = ?`, active.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ingest(context.Background(), ingestParams("recover-pending", []domain.NewDelivery{{TargetID: "pending-target"}})); err != nil {
		t.Fatal(err)
	}
	attemptsBefore := rowCount(t, s, "delivery_attempts")
	now := time.UnixMilli(5000)
	changes, err := s.RecoverInterrupted(context.Background(), now,
		PollSchedule{Interval: time.Second, Timeout: 30 * time.Second},
		map[string]PollSchedule{sending.TargetID: {Interval: 2 * time.Second, Timeout: 9 * time.Second}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].AttemptID != sending.AttemptID || changes[0].AfterTransport != "unknown" || changes[0].AfterDeployment != "locating" {
		t.Fatalf("recovery changes = %#v", changes)
	}
	if got := rowCount(t, s, "delivery_attempts"); got != attemptsBefore {
		t.Fatalf("attempt rows = %d, want %d", got, attemptsBefore)
	}
	assertAttemptState(t, s, sending.AttemptID, "unknown", "locating")
	var sendingDue, sendingDeadline int64
	if err := s.db.QueryRow(`SELECT poll_due_at, monitoring_deadline_at FROM delivery_attempts WHERE id = ?`, sending.AttemptID).Scan(&sendingDue, &sendingDeadline); err != nil {
		t.Fatal(err)
	}
	if sendingDue != now.Add(2*time.Second).UnixMilli() || sendingDeadline != now.Add(9*time.Second).UnixMilli() {
		t.Fatalf("sending recovery due/deadline = %d/%d", sendingDue, sendingDeadline)
	}
	var activeDue, activeDeadline int64
	var activeLastPolled *int64
	if err := s.db.QueryRow(`SELECT poll_due_at, monitoring_deadline_at, last_polled_at FROM delivery_attempts WHERE id = ?`, active.AttemptID).Scan(&activeDue, &activeDeadline, &activeLastPolled); err != nil {
		t.Fatal(err)
	}
	if activeDue != 4500 || activeDeadline != 8000 || activeLastPolled != nil {
		t.Fatalf("active recovery due/deadline/claim = %d/%d/%v", activeDue, activeDeadline, activeLastPolled)
	}
	var pendingCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM delivery_attempts WHERE transport_status = 'pending' AND deployment_status = 'not_started'`).Scan(&pendingCount); err != nil {
		t.Fatal(err)
	}
	if pendingCount == 0 {
		t.Fatal("recovery changed every pending attempt")
	}
}

func preparePollAttempt(t *testing.T, s *Store, key, targetID, transport, deployment string, pollDue, deadline time.Time) PendingAttempt {
	t.Helper()
	if _, err := s.Ingest(context.Background(), ingestParams(key, []domain.NewDelivery{{TargetID: targetID}})); err != nil {
		t.Fatal(err)
	}
	work, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(2000))
	if err != nil || !claimed {
		t.Fatalf("claim poll fixture = %#v/%v/%v", work, claimed, err)
	}
	if err := s.MarkEnqueued(context.Background(), work.AttemptID, []byte(`{}`), []byte(`true`), time.UnixMilli(2100), pollDue, deadline); err != nil {
		t.Fatal(err)
	}
	if transport != "enqueued" || deployment != "locating" {
		if _, err := s.db.Exec(`UPDATE delivery_attempts SET transport_status = ?, deployment_status = ? WHERE id = ?`,
			transport, deployment, work.AttemptID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE deliveries SET transport_status = ?, deployment_status = ? WHERE current_attempt_id = ?`,
			transport, deployment, work.AttemptID); err != nil {
			t.Fatal(err)
		}
	}
	return work
}

func assertAttemptState(t *testing.T, s *Store, attemptID, wantTransport, wantDeployment string) {
	t.Helper()
	var transport, deployment string
	if err := s.db.QueryRow(`SELECT transport_status, deployment_status FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&transport, &deployment); err != nil {
		t.Fatal(err)
	}
	if transport != wantTransport || deployment != wantDeployment {
		t.Fatalf("attempt %s state = %s/%s", attemptID, transport, deployment)
	}
}

func auditCount(t *testing.T, s *Store, attemptID string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE attempt_id = ?`, attemptID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func rowCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertCurrentState(t *testing.T, s *Store, wantTransport, wantDeployment string) {
	t.Helper()
	var attemptTransport, attemptDeployment, deliveryTransport, deliveryDeployment string
	if err := s.db.QueryRow(`
		SELECT a.transport_status, a.deployment_status, d.transport_status, d.deployment_status
		FROM delivery_attempts a JOIN deliveries d ON d.current_attempt_id = a.id`).Scan(
		&attemptTransport, &attemptDeployment, &deliveryTransport, &deliveryDeployment,
	); err != nil {
		t.Fatal(err)
	}
	if attemptTransport != wantTransport || deliveryTransport != wantTransport ||
		attemptDeployment != wantDeployment || deliveryDeployment != wantDeployment {
		t.Fatalf("states = attempt %s/%s delivery %s/%s", attemptTransport, attemptDeployment, deliveryTransport, deliveryDeployment)
	}
}

func assertAuditTransition(t *testing.T, s *Store, attemptID, wantAction, wantBefore, wantAfter string) {
	t.Helper()
	var action string
	var beforeJSON, afterJSON []byte
	if err := s.db.QueryRow(`SELECT action, before_json, after_json FROM audit_logs WHERE attempt_id = ? AND action = ?`, attemptID, wantAction).Scan(&action, &beforeJSON, &afterJSON); err != nil {
		t.Fatal(err)
	}
	if action != wantAction {
		t.Fatalf("audit action = %q", action)
	}
	var before, after map[string]string
	if err := json.Unmarshal(beforeJSON, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(afterJSON, &after); err != nil {
		t.Fatal(err)
	}
	if before["transport_status"] != wantBefore || after["transport_status"] != wantAfter {
		t.Fatalf("audit transition = %s -> %s", beforeJSON, afterJSON)
	}
	if bytes.Equal(beforeJSON, afterJSON) {
		t.Fatal("audit before and after snapshots are identical")
	}
}
