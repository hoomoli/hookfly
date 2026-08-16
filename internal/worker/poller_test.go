package worker

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/config"
	"github.com/hoomoli/hookfly/internal/store"
)

func TestPollDeploymentStateTable(t *testing.T) {
	// Break caught: deviating from the approved locating/running/terminal/timeout/recovery state table or auditing unchanged polls.
	tests := []struct {
		name                  string
		startTransport        string
		startDeployment       string
		remoteStatus          *string
		empty                 bool
		afterDeadline         bool
		recoverSending        bool
		wantTransport         string
		wantDeployment        string
		wantAuditDelta        int
		wantStatusLogDelta    int
		wantDeploymentID      bool
		wantDeploymentQueries int32
	}{
		{name: "locating matching running before deadline", startTransport: "enqueued", startDeployment: "locating", remoteStatus: statusPointer("running"), wantTransport: "enqueued", wantDeployment: "running", wantAuditDelta: 1, wantStatusLogDelta: 1, wantDeploymentID: true, wantDeploymentQueries: 1},
		{name: "running matching running before deadline", startTransport: "enqueued", startDeployment: "running", remoteStatus: statusPointer("running"), wantTransport: "enqueued", wantDeployment: "running", wantDeploymentID: true, wantDeploymentQueries: 1},
		{name: "running matching done before deadline", startTransport: "enqueued", startDeployment: "running", remoteStatus: statusPointer("done"), wantTransport: "enqueued", wantDeployment: "done", wantAuditDelta: 1, wantStatusLogDelta: 1, wantDeploymentID: true, wantDeploymentQueries: 1},
		{name: "running matching error before deadline", startTransport: "enqueued", startDeployment: "running", remoteStatus: statusPointer("error"), wantTransport: "enqueued", wantDeployment: "error", wantAuditDelta: 1, wantStatusLogDelta: 1, wantDeploymentID: true, wantDeploymentQueries: 1},
		{name: "running matching cancelled before deadline", startTransport: "enqueued", startDeployment: "running", remoteStatus: statusPointer("cancelled"), wantTransport: "enqueued", wantDeployment: "cancelled", wantAuditDelta: 1, wantStatusLogDelta: 1, wantDeploymentID: true, wantDeploymentQueries: 1},
		{name: "running matching running after deadline", startTransport: "enqueued", startDeployment: "running", remoteStatus: statusPointer("running"), afterDeadline: true, wantTransport: "enqueued", wantDeployment: "timeout", wantAuditDelta: 1, wantStatusLogDelta: 1, wantDeploymentID: true, wantDeploymentQueries: 1},
		{name: "locating empty list after deadline", startTransport: "unknown", startDeployment: "locating", empty: true, afterDeadline: true, wantTransport: "unknown", wantDeployment: "unknown", wantAuditDelta: 1, wantStatusLogDelta: 1, wantDeploymentQueries: 1},
		{name: "sending recovery before deadline", startTransport: "sending", startDeployment: "not_started", empty: true, recoverSending: true, wantTransport: "unknown", wantDeployment: "locating", wantAuditDelta: 1, wantStatusLogDelta: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var getCalls, postCalls atomic.Int32
			var responseBody atomic.Value
			responseBody.Store("[]")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					getCalls.Add(1)
					_, _ = io.WriteString(w, responseBody.Load().(string))
				case http.MethodPost:
					postCalls.Add(1)
					_, _ = io.WriteString(w, `true`)
				default:
					w.WriteHeader(http.StatusMethodNotAllowed)
				}
			}))
			defer server.Close()

			s, db := openWorkerStore(t)
			cfg := pollerConfig(server.URL, time.Second, 30*time.Second)
			now := time.UnixMilli(10_000).UTC()
			deadline := now.Add(10 * time.Second)
			if test.afterDeadline {
				deadline = now.Add(-time.Millisecond)
			}
			eventID := ingestWorkerEvent(t, s, "poll-state-"+test.name, time.UnixMilli(1000), workerDeliveries(t, cfg))
			attemptID := attemptForEvent(t, db, eventID)
			claim, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(2000))
			if err != nil || !claimed || claim.AttemptID != attemptID {
				t.Fatalf("ClaimPendingAttempt() = %#v/%v/%v", claim, claimed, err)
			}
			if !test.recoverSending {
				if test.startTransport == "unknown" {
					err = s.MarkTransportUnknown(context.Background(), attemptID, []byte(`{}`), []byte(`{}`), time.UnixMilli(2100), now, deadline)
				} else {
					err = s.MarkEnqueued(context.Background(), attemptID, []byte(`{}`), []byte(`true`), time.UnixMilli(2100), now, deadline)
				}
				if err != nil {
					t.Fatal(err)
				}
				if test.startDeployment == "running" {
					setWorkerAttemptState(t, db, attemptID, test.startTransport, test.startDeployment, "deployment-1")
				}
			}
			if !test.empty {
				statusJSON := "null"
				if test.remoteStatus != nil {
					statusJSON = fmt.Sprintf("%q", *test.remoteStatus)
				}
				responseBody.Store(fmt.Sprintf(`[{"deploymentId":"deployment-1","composeId":"configured-compose","description":"attempt_id=%s source=pipeline","status":%s,"createdAt":"2026-08-05T10:00:00.123456789Z"}]`, attemptID, statusJSON))
			}
			beforeAudits := workerAuditCount(t, db, attemptID)
			var logs bytes.Buffer
			poller := NewPoller(workerManager(t, cfg, s), s, slog.New(slog.NewJSONHandler(&logs, nil)))
			if test.recoverSending {
				if err := poller.Recover(context.Background(), now); err != nil {
					t.Fatal(err)
				}
				if err := poller.RunOnce(context.Background(), now); err != nil {
					t.Fatal(err)
				}
			} else if err := poller.RunOnce(context.Background(), now); err != nil {
				t.Fatal(err)
			}

			assertWorkerAttemptState(t, db, attemptID, test.wantTransport, test.wantDeployment)
			if got := workerAuditCount(t, db, attemptID) - beforeAudits; got != test.wantAuditDelta {
				t.Fatalf("audit delta = %d, want %d", got, test.wantAuditDelta)
			}
			if got := strings.Count(logs.String(), `"msg":"delivery status changed"`); got != test.wantStatusLogDelta {
				t.Fatalf("status log count = %d, want %d; logs=%s", got, test.wantStatusLogDelta, logs.String())
			}
			var deploymentID *string
			if err := db.QueryRow(`SELECT deployment_id FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&deploymentID); err != nil {
				t.Fatal(err)
			}
			if test.wantDeploymentID && (deploymentID == nil || *deploymentID != "deployment-1") {
				t.Fatalf("deployment ID = %v", deploymentID)
			}
			if getCalls.Load() != test.wantDeploymentQueries || postCalls.Load() != 0 {
				t.Fatalf("GET/POST calls = %d/%d", getCalls.Load(), postCalls.Load())
			}
		})
	}
}

func TestRecoverUsesPersistedCursorWithoutRepeatingDeploymentPost(t *testing.T) {
	// Break caught: losing the durable preflight boundary on restart or replaying an uncertain deployment POST.
	var getCalls, postCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getCalls.Add(1)
			_, _ = io.WriteString(w, `[
				{"deploymentId":"after-restart","composeId":"configured-compose","description":"Commit: abc123","status":"done","createdAt":"2026-08-12T03:00:01Z"},
				{"deploymentId":"before-send","composeId":"configured-compose","description":"Commit: previous","status":"done","createdAt":"2026-08-12T03:00:00Z"}
			]`)
		case http.MethodPost:
			postCalls.Add(1)
			_, _ = io.WriteString(w, `true`)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	s, db := openWorkerStore(t)
	cfg := pollerConfig(server.URL, time.Second, time.Minute)
	eventID := ingestWorkerEvent(t, s, "restart-cursor", time.UnixMilli(1000), workerDeliveries(t, cfg))
	attemptID := attemptForEvent(t, db, eventID)
	work, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(2000))
	if err != nil || !claimed || work.AttemptID != attemptID {
		t.Fatalf("ClaimPendingAttempt() = %#v/%v/%v", work, claimed, err)
	}
	if err := s.SetDeploymentCursor(context.Background(), attemptID, "before-send"); err != nil {
		t.Fatal(err)
	}

	recoveredAt := time.UnixMilli(5000).UTC()
	poller := NewPoller(workerManager(t, cfg, s), s, discardLogger())
	if err := poller.Recover(context.Background(), recoveredAt); err != nil {
		t.Fatal(err)
	}
	if err := poller.RunOnce(context.Background(), recoveredAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	assertWorkerAttemptState(t, db, attemptID, "enqueued", "done")
	var deploymentID, cursor string
	if err := db.QueryRow(`SELECT deployment_id, deployment_cursor FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&deploymentID, &cursor); err != nil {
		t.Fatal(err)
	}
	if deploymentID != "after-restart" || cursor != "before-send" {
		t.Fatalf("deployment ID/cursor = %q/%q", deploymentID, cursor)
	}
	if getCalls.Load() != 1 || postCalls.Load() != 0 {
		t.Fatalf("GET/POST calls = %d/%d", getCalls.Load(), postCalls.Load())
	}
}

func TestPollMatchesOnlyExactAttemptTokenAndUsesNewestExactMatch(t *testing.T) {
	// Break caught: substring-correlating another attempt or choosing an older exact token match.
	s, db := openWorkerStore(t)
	now := time.UnixMilli(10_000).UTC()
	var attemptID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[
			{"deploymentId":"substring-newest","composeId":"configured-compose","description":"attempt_id=%s-extra","status":"done","createdAt":"2026-08-05T10:00:03Z"},
			{"deploymentId":"exact-newer","composeId":"configured-compose","description":"source=pipeline attempt_id=%s","status":"running","createdAt":"2026-08-05T10:00:02Z"},
			{"deploymentId":"exact-older","composeId":"configured-compose","description":"attempt_id=%s source=pipeline","status":"done","createdAt":"2026-08-05T10:00:01Z"},
			{"deploymentId":"embedded","composeId":"configured-compose","description":"prefixattempt_id=%s","status":"done","createdAt":"2026-08-05T10:00:00Z"}
		]`, attemptID, attemptID, attemptID, attemptID)
	}))
	defer server.Close()
	attemptID = prepareWorkerPollAttempt(t, s, db, server.URL, "exact-token", "unknown", "locating", now, now.Add(time.Minute), "")
	poller := NewPoller(workerManager(t, pollerConfig(server.URL, time.Second, time.Minute), s), s, discardLogger())
	if err := poller.RunOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	assertWorkerAttemptState(t, db, attemptID, "enqueued", "running")
	var deploymentID string
	if err := db.QueryRow(`SELECT deployment_id FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&deploymentID); err != nil {
		t.Fatal(err)
	}
	if deploymentID != "exact-newer" {
		t.Fatalf("deployment ID = %q", deploymentID)
	}
}

func TestPollBindsOnePostCursorDeploymentAfterDescriptionBecomesCommit(t *testing.T) {
	// Break caught: leaving a real Dokploy deployment unbound after Git metadata replaces the correlation description.
	s, db := openWorkerStore(t)
	now := time.UnixMilli(10_000).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[
			{"deploymentId":"new-deployment","composeId":"configured-compose","description":"Commit: c05faf4","status":"error","createdAt":"2026-08-12T03:00:01Z"},
			{"deploymentId":"cursor","composeId":"configured-compose","description":"Commit: old","status":"done","createdAt":"2026-08-12T03:00:00Z"}
		]`)
	}))
	defer server.Close()
	attemptID := prepareWorkerPollAttempt(t, s, db, server.URL, "cursor-commit", "enqueued", "locating", now, now.Add(time.Minute), "")
	if _, err := db.Exec(`UPDATE delivery_attempts SET deployment_cursor = 'cursor' WHERE id = ?`, attemptID); err != nil {
		t.Fatal(err)
	}
	poller := NewPoller(workerManager(t, pollerConfig(server.URL, time.Second, time.Minute), s), s, discardLogger())
	if err := poller.RunOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	assertWorkerAttemptState(t, db, attemptID, "enqueued", "error")
	var deploymentID string
	if err := db.QueryRow(`SELECT deployment_id FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&deploymentID); err != nil {
		t.Fatal(err)
	}
	if deploymentID != "new-deployment" {
		t.Fatalf("deployment ID = %q", deploymentID)
	}
}

func TestPollTerminatesUnsafeCursorCorrelationAsUnknown(t *testing.T) {
	// Break caught: guessing which deployment belongs to Hookfly when history is ambiguous or its boundary was pruned.
	tests := []struct {
		name   string
		cursor string
		body   string
	}{
		{
			name:   "ambiguous",
			cursor: "cursor",
			body: `[
				{"deploymentId":"manual","composeId":"configured-compose","description":"Commit: manual","status":"done","createdAt":"2026-08-12T03:00:02Z"},
				{"deploymentId":"automatic","composeId":"configured-compose","description":"Commit: automatic","status":"done","createdAt":"2026-08-12T03:00:01Z"},
				{"deploymentId":"cursor","composeId":"configured-compose","description":"Commit: old","status":"done","createdAt":"2026-08-12T03:00:00Z"}
			]`,
		},
		{
			name:   "cursor missing",
			cursor: "pruned",
			body:   `[{"deploymentId":"retained","composeId":"configured-compose","description":"Commit: retained","status":"done","createdAt":"2026-08-12T03:00:01Z"}]`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, db := openWorkerStore(t)
			now := time.UnixMilli(10_000).UTC()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			attemptID := prepareWorkerPollAttempt(t, s, db, server.URL, "unsafe-cursor-"+test.name, "enqueued", "locating", now, now.Add(time.Minute), "")
			if _, err := db.Exec(`UPDATE delivery_attempts SET deployment_cursor = ? WHERE id = ?`, test.cursor, attemptID); err != nil {
				t.Fatal(err)
			}
			poller := NewPoller(workerManager(t, pollerConfig(server.URL, time.Second, time.Minute), s), s, discardLogger())
			if err := poller.RunOnce(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			assertWorkerAttemptState(t, db, attemptID, "enqueued", "unknown")
			var deploymentID *string
			if err := db.QueryRow(`SELECT deployment_id FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&deploymentID); err != nil {
				t.Fatal(err)
			}
			if deploymentID != nil {
				t.Fatalf("unsafe deployment ID = %q", *deploymentID)
			}
		})
	}
}

func TestPollUnknownStatusesAndListErrorsStayActiveUntilDeadline(t *testing.T) {
	// Break caught: treating unknown/null or query failures as success/failure before the deadline, or failing to resolve them conservatively at expiry.
	tests := []struct {
		name           string
		status         int
		bodyStatus     string
		afterDeadline  bool
		wantTransport  string
		wantDeployment string
		wantAudit      int
		wantID         bool
	}{
		{name: "unknown status before deadline", status: http.StatusOK, bodyStatus: `"queued"`, wantTransport: "enqueued", wantDeployment: "unrecognized", wantAudit: 1, wantID: true},
		{name: "null status after deadline", status: http.StatusOK, bodyStatus: `null`, afterDeadline: true, wantTransport: "enqueued", wantDeployment: "timeout", wantAudit: 1, wantID: true},
		{name: "list error before deadline", status: http.StatusBadGateway, wantTransport: "unknown", wantDeployment: "locating"},
		{name: "list error at deadline", status: http.StatusBadGateway, afterDeadline: true, wantTransport: "unknown", wantDeployment: "unknown", wantAudit: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, db := openWorkerStore(t)
			now := time.UnixMilli(10_000).UTC()
			deadline := now.Add(time.Minute)
			if test.afterDeadline {
				deadline = now
			}
			var attemptID string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				if test.status == http.StatusOK {
					_, _ = fmt.Fprintf(w, `[{"deploymentId":"deployment-edge","composeId":"configured-compose","description":"attempt_id=%s","status":%s,"createdAt":"2026-08-05T10:00:00Z"}]`, attemptID, test.bodyStatus)
				} else {
					_, _ = io.WriteString(w, `{"message":"unavailable"}`)
				}
			}))
			defer server.Close()
			attemptID = prepareWorkerPollAttempt(t, s, db, server.URL, "poll-edge-"+test.name, "unknown", "locating", now, deadline, "")
			beforeAudits := workerAuditCount(t, db, attemptID)
			poller := NewPoller(workerManager(t, pollerConfig(server.URL, 0, time.Minute), s), s, discardLogger())
			if err := poller.RunOnce(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			assertWorkerAttemptState(t, db, attemptID, test.wantTransport, test.wantDeployment)
			if got := workerAuditCount(t, db, attemptID) - beforeAudits; got != test.wantAudit {
				t.Fatalf("audit delta = %d, want %d", got, test.wantAudit)
			}
			var deploymentID *string
			var pollDue *int64
			if err := db.QueryRow(`SELECT deployment_id, poll_due_at FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&deploymentID, &pollDue); err != nil {
				t.Fatal(err)
			}
			if test.wantID != (deploymentID != nil && *deploymentID == "deployment-edge") {
				t.Fatalf("deployment ID = %v, want present %v", deploymentID, test.wantID)
			}
			if !test.afterDeadline && (pollDue == nil || *pollDue != now.Add(time.Second).UnixMilli()) {
				t.Fatalf("default next poll due = %v", pollDue)
			}
		})
	}
}

func TestPollFindsAttemptInHistoryLargerThan64KiBAndTerminates(t *testing.T) {
	// Break caught: rejecting the entire Dokploy history once unrelated older deployments push it past 64 KiB.
	s, db := openWorkerStore(t)
	now := time.UnixMilli(10_000).UTC()
	var attemptID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"deploymentId":"matched","composeId":"configured-compose","description":"attempt_id=%s source=pipeline","status":"done"}`, attemptID)
		for index := 0; index < 900; index++ {
			_, _ = fmt.Fprintf(w, `,{"deploymentId":"old-%d","composeId":"configured-compose","description":"attempt_id=other-%d %s","status":"done"}`, index, index, strings.Repeat("x", 64))
		}
		_, _ = io.WriteString(w, `]`)
	}))
	defer server.Close()
	attemptID = prepareWorkerPollAttempt(t, s, db, server.URL, "large-history", "enqueued", "locating", now, now.Add(time.Minute), "")
	poller := NewPoller(workerManager(t, pollerConfig(server.URL, time.Second, time.Minute), s), s, discardLogger())
	if err := poller.RunOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	assertWorkerAttemptState(t, db, attemptID, "enqueued", "done")
}

func TestPollUnrecognizedDeploymentContinuesToDone(t *testing.T) {
	// Break caught: treating an unrecognized remote status as terminal or making it ineligible for the next due poll.
	s, db := openWorkerStore(t)
	now := time.UnixMilli(10_000).UTC()
	var attemptID string
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		status := "queued"
		if calls.Add(1) == 2 {
			status = "done"
		}
		_, _ = fmt.Fprintf(w, `[{"deploymentId":"deployment-1","composeId":"configured-compose","description":"attempt_id=%s","status":%q}]`, attemptID, status)
	}))
	defer server.Close()
	attemptID = prepareWorkerPollAttempt(t, s, db, server.URL, "unrecognized-then-done", "enqueued", "locating", now, now.Add(time.Minute), "")
	poller := NewPoller(workerManager(t, pollerConfig(server.URL, time.Second, time.Minute), s), s, discardLogger())
	if err := poller.RunOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	assertWorkerAttemptState(t, db, attemptID, "enqueued", "unrecognized")
	if err := poller.RunOnce(context.Background(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	assertWorkerAttemptState(t, db, attemptID, "enqueued", "done")
	if calls.Load() != 2 {
		t.Fatalf("deployment queries = %d, want 2", calls.Load())
	}
}

func TestPollContinuesByDeploymentIDAfterDescriptionChanges(t *testing.T) {
	// Break caught: losing a running deployment when Dokploy replaces the attempt token with Git metadata.
	s, db := openWorkerStore(t)
	now := time.UnixMilli(10_000).UTC()
	var attemptID string
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = fmt.Fprintf(w, `[{"deploymentId":"deployment-1","composeId":"configured-compose","description":"attempt_id=%s","status":"running"}]`, attemptID)
			return
		}
		_, _ = io.WriteString(w, `[{"deploymentId":"deployment-1","composeId":"configured-compose","description":"Commit: abc123","status":"done"}]`)
	}))
	defer server.Close()
	attemptID = prepareWorkerPollAttempt(t, s, db, server.URL, "description-replaced", "enqueued", "locating", now, now.Add(time.Minute), "")
	poller := NewPoller(workerManager(t, pollerConfig(server.URL, time.Second, time.Minute), s), s, discardLogger())
	if err := poller.RunOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	assertWorkerAttemptState(t, db, attemptID, "enqueued", "running")
	if err := poller.RunOnce(context.Background(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	assertWorkerAttemptState(t, db, attemptID, "enqueued", "done")
}

func TestPollLogsSafeLookupFailureAndNeverPosts(t *testing.T) {
	// Break caught: silently dropping GET failures or logging a secret-bearing response while reconciling.
	s, db := openWorkerStore(t)
	now := time.UnixMilli(10_000).UTC()
	var attemptID string
	var postCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postCalls.Add(1)
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"message":"poll-test-key response-body-secret"}`)
	}))
	defer server.Close()
	attemptID = prepareWorkerPollAttempt(t, s, db, server.URL, "safe-lookup-error", "unknown", "locating", now, now.Add(time.Minute), "")
	var logs bytes.Buffer
	poller := NewPoller(workerManager(t, pollerConfig(server.URL, time.Second, time.Minute), s), s, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err := poller.RunOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	assertWorkerAttemptState(t, db, attemptID, "unknown", "locating")
	text := logs.String()
	for _, required := range []string{"Dokploy deployment lookup failed", attemptID, "production", "unexpected HTTP status 502"} {
		if !strings.Contains(text, required) {
			t.Fatalf("warning missing %q: %s", required, text)
		}
	}
	for _, secret := range []string{"poll-test-key", "response-body-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("warning leaked %q: %s", secret, text)
		}
	}
	if postCalls.Load() != 0 {
		t.Fatalf("POST calls = %d", postCalls.Load())
	}
}

func TestPollTerminalTransitionRunsRetentionHook(t *testing.T) {
	// Break caught: a completed remote deployment becoming terminal without triggering configured history pruning.
	s, db := openWorkerStore(t)
	now := time.UnixMilli(10_000).UTC()
	var attemptID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"deploymentId":"done","composeId":"configured-compose","description":"attempt_id=%s","status":"done"}]`, attemptID)
	}))
	defer server.Close()
	attemptID = prepareWorkerPollAttempt(t, s, db, server.URL, "poll-terminal-hook", "enqueued", "locating", now, now.Add(time.Minute), "")
	poller := NewPoller(workerManager(t, pollerConfig(server.URL, time.Second, time.Minute), s), s, discardLogger())
	var calls atomic.Int32
	poller.SetTerminalHook(func(context.Context) error { calls.Add(1); return nil })
	if err := poller.RunOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("retention calls = %d", calls.Load())
	}
}

func TestWorkerConstructorsNormalizePollInterval(t *testing.T) {
	// Break caught: bypassing the persisted-clock floor or changing the non-positive fallback in any worker schedule.
	tests := []struct {
		name     string
		interval time.Duration
		want     time.Duration
	}{
		{name: "negative", interval: -time.Nanosecond, want: time.Second},
		{name: "zero", want: time.Second},
		{name: "sub-millisecond", interval: 500 * time.Microsecond, want: time.Millisecond},
		{name: "one millisecond", interval: time.Millisecond, want: time.Millisecond},
		{name: "larger positive", interval: 1500 * time.Microsecond, want: 1500 * time.Microsecond},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := pollerConfig("http://127.0.0.1:1", test.interval, time.Minute)
			manager := workerManager(t, cfg, nil)
			dispatcher := NewDispatcher(manager, nil, discardLogger(), nil)
			poller := NewPoller(manager, nil, discardLogger())

			target, found := dispatcher.manager.Current().Target("production")
			if !found {
				t.Fatal("dispatcher target missing")
			}
			if got := target.PollInterval; got != test.want {
				t.Errorf("dispatcher interval = %v, want %v", got, test.want)
			}
			if got := poller.currentInterval(); got != test.want {
				t.Errorf("poller interval = %v, want %v", got, test.want)
			}
			pollTarget, found := poller.manager.Current().Target("production")
			if !found {
				t.Fatal("poller target missing")
			}
			if got := pollTarget.PollInterval; got != test.want {
				t.Errorf("target recovery interval = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSubMillisecondPollIntervalContinuesThroughDeadline(t *testing.T) {
	// Break caught: persisting a positive sub-millisecond interval at the claim millisecond and permanently stranding the attempt.
	var getCalls, postCalls atomic.Int32
	var posted atomic.Bool
	s, db := openWorkerStore(t)
	var attemptID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			postCalls.Add(1)
			posted.Store(true)
			_, _ = io.WriteString(w, `true`)
		case http.MethodGet:
			getCalls.Add(1)
			if !posted.Load() {
				_, _ = io.WriteString(w, `[]`)
				return
			}
			_, _ = fmt.Fprintf(w, `[{"deploymentId":"deployment-1","composeId":"configured-compose","description":"attempt_id=%s","status":"running","createdAt":"2026-08-05T10:00:00Z"}]`, attemptID)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	cfg := pollerConfig(server.URL, 500*time.Microsecond, 2*time.Millisecond)
	eventID := ingestWorkerEvent(t, s, "sub-millisecond-poll", time.UnixMilli(1), workerDeliveries(t, cfg))
	attemptID = attemptForEvent(t, db, eventID)
	dispatchedAt := time.UnixMilli(10).UTC()
	manager := workerManager(t, cfg, s)
	dispatcher := NewDispatcher(manager, s, discardLogger(), func() time.Time { return dispatchedAt })
	worked, err := dispatcher.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("dispatcher RunOnce() = %v/%v", worked, err)
	}
	var initialPollDue int64
	if err := db.QueryRow(`SELECT poll_due_at FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&initialPollDue); err != nil {
		t.Fatal(err)
	}

	poller := NewPoller(manager, s, discardLogger())
	if err := poller.RunOnce(context.Background(), dispatchedAt.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := poller.RunOnce(context.Background(), dispatchedAt.Add(2*time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	var transport, deployment string
	if err := db.QueryRow(`SELECT transport_status, deployment_status FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&transport, &deployment); err != nil {
		t.Fatal(err)
	}
	if initialPollDue != dispatchedAt.Add(time.Millisecond).UnixMilli() {
		t.Errorf("initial poll due = %d, want %d", initialPollDue, dispatchedAt.Add(time.Millisecond).UnixMilli())
	}
	if transport != "enqueued" || deployment != "timeout" {
		t.Errorf("attempt state = %s/%s, want enqueued/timeout", transport, deployment)
	}
	if getCalls.Load() != 3 || postCalls.Load() != 1 {
		t.Errorf("GET/POST calls = %d/%d, want 3/1", getCalls.Load(), postCalls.Load())
	}
}

func TestRecoverFloorsSubMillisecondPollIntervalToPersistedMillisecond(t *testing.T) {
	// Break caught: recovery scheduling an interrupted send in the same persisted millisecond as recovery.
	s, db := openWorkerStore(t)
	cfg := pollerConfig("http://127.0.0.1:1", 500*time.Microsecond, time.Minute)
	eventID := ingestWorkerEvent(t, s, "recover-sub-millisecond", time.UnixMilli(1), workerDeliveries(t, cfg))
	attemptID := attemptForEvent(t, db, eventID)
	work, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(2))
	if err != nil || !claimed || work.AttemptID != attemptID {
		t.Fatalf("ClaimPendingAttempt() = %#v/%v/%v", work, claimed, err)
	}

	recoveryNow := time.UnixMilli(10).UTC()
	poller := NewPoller(workerManager(t, cfg, s), s, discardLogger())
	if err := poller.Recover(context.Background(), recoveryNow); err != nil {
		t.Fatal(err)
	}
	var pollDue int64
	if err := db.QueryRow(`SELECT poll_due_at FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&pollDue); err != nil {
		t.Fatal(err)
	}
	if want := recoveryNow.Add(time.Millisecond).UnixMilli(); pollDue != want {
		t.Fatalf("recovered poll due = %d, want %d", pollDue, want)
	}
}

func TestPollClaimPreventsOverlappingQueries(t *testing.T) {
	// Break caught: two overlapping RunOnce calls issuing duplicate GETs for one attempt.
	s, db := openWorkerStore(t)
	now := time.UnixMilli(10_000).UTC()
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		_, _ = io.WriteString(w, `[]`)
	}))
	defer server.Close()
	prepareWorkerPollAttempt(t, s, db, server.URL, "overlap", "enqueued", "locating", now, now.Add(time.Minute), "")
	poller := NewPoller(workerManager(t, pollerConfig(server.URL, time.Second, time.Minute), s), s, discardLogger())
	firstDone := make(chan error, 1)
	go func() { firstDone <- poller.RunOnce(context.Background(), now) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first deployment query did not start")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- poller.RunOnce(context.Background(), now) }()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("overlapping poll blocked")
	}
	if calls.Load() != 1 {
		t.Fatalf("deployment queries = %d", calls.Load())
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestPollRunOnceUsesBoundedWorkBatch(t *testing.T) {
	// Break caught: one tick scanning and querying every active attempt without a fixed upper bound.
	s, db := openWorkerStore(t)
	now := time.UnixMilli(10_000).UTC()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `[]`)
	}))
	defer server.Close()
	for index := 0; index < 40; index++ {
		prepareWorkerPollAttempt(t, s, db, server.URL, fmt.Sprintf("bounded-%02d", index), "enqueued", "locating", now, now.Add(time.Minute), fmt.Sprintf("deployment-%02d", index))
	}
	poller := NewPoller(workerManager(t, pollerConfig(server.URL, time.Second, time.Minute), s), s, discardLogger())
	if err := poller.RunOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 32 {
		t.Fatalf("first poll batch queries = %d, want 32", calls.Load())
	}
	if err := poller.RunOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 40 {
		t.Fatalf("second poll total queries = %d, want 40", calls.Load())
	}
}

func TestRecoverFinalizesExpiredActiveAttemptOnFirstPass(t *testing.T) {
	// Break caught: restart waiting for a future poll_due_at even though the stored monitoring deadline already expired.
	s, db := openWorkerStore(t)
	now := time.UnixMilli(10_000).UTC()
	var getCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			t.Fatal("recovery issued a deployment POST")
		}
		getCalls.Add(1)
		_, _ = io.WriteString(w, `[]`)
	}))
	defer server.Close()
	attemptID := prepareWorkerPollAttempt(t, s, db, server.URL, "recover-expired", "unknown", "locating", now.Add(time.Hour), now.Add(-time.Millisecond), "")
	if _, err := db.Exec(`UPDATE delivery_attempts SET last_polled_at = ? WHERE id = ?`, now.Add(-time.Second).UnixMilli(), attemptID); err != nil {
		t.Fatal(err)
	}
	poller := NewPoller(workerManager(t, pollerConfig(server.URL, time.Second, time.Minute), s), s, discardLogger())
	if err := poller.Recover(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if err := poller.RunOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	assertWorkerAttemptState(t, db, attemptID, "unknown", "unknown")
	if getCalls.Load() != 1 {
		t.Fatalf("first recovery deployment queries = %d", getCalls.Load())
	}
}

func statusPointer(value string) *string { return &value }

func pollerConfig(baseURL string, interval, timeout time.Duration) *config.Bundle {
	return &config.Bundle{
		Global:             config.Global{Polling: config.Polling{Interval: config.Duration{Duration: interval}, Timeout: config.Duration{Duration: timeout}}},
		DokployConnections: []config.DokployConnection{{ID: "dokploy", BaseURL: baseURL, APIKey: "poll-test-key"}},
		Targets: []config.Target{{
			ID: "production", Type: "dokploy", Connection: "dokploy", ResourceType: "compose", ResourceID: "configured-compose",
		}},
	}
}

func prepareWorkerPollAttempt(t *testing.T, s *store.Store, db *sql.DB, baseURL, key, transport, deployment string, pollDue, deadline time.Time, deploymentID string) string {
	t.Helper()
	eventID := ingestWorkerEvent(t, s, key, time.UnixMilli(1000), workerDeliveries(t, pollerConfig(baseURL, time.Second, time.Minute)))
	attemptID := attemptForEvent(t, db, eventID)
	work, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(2000))
	if err != nil || !claimed || work.AttemptID != attemptID {
		t.Fatalf("ClaimPendingAttempt() = %#v/%v/%v", work, claimed, err)
	}
	if transport == "unknown" {
		err = s.MarkTransportUnknown(context.Background(), attemptID, []byte(`{}`), []byte(`{}`), time.UnixMilli(2100), pollDue, deadline)
	} else {
		err = s.MarkEnqueued(context.Background(), attemptID, []byte(`{}`), []byte(`true`), time.UnixMilli(2100), pollDue, deadline)
	}
	if err != nil {
		t.Fatal(err)
	}
	setWorkerAttemptState(t, db, attemptID, transport, deployment, deploymentID)
	return attemptID
}

func setWorkerAttemptState(t *testing.T, db *sql.DB, attemptID, transport, deployment, deploymentID string) {
	t.Helper()
	var storedDeploymentID any
	if deploymentID != "" {
		storedDeploymentID = deploymentID
	}
	if _, err := db.Exec(`UPDATE delivery_attempts SET transport_status = ?, deployment_status = ?, deployment_id = ? WHERE id = ?`, transport, deployment, storedDeploymentID, attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE deliveries SET transport_status = ?, deployment_status = ? WHERE current_attempt_id = ?`, transport, deployment, attemptID); err != nil {
		t.Fatal(err)
	}
}

func workerAuditCount(t *testing.T, db *sql.DB, attemptID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE attempt_id = ?`, attemptID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
