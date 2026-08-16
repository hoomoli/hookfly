package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/config"
	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/store"
	_ "modernc.org/sqlite"
)

func TestAttemptsStateTable(t *testing.T) {
	// Break caught: retry/redeploy bypassing the state table, missing reconciliation, or replacing more than one current attempt.
	tests := []struct {
		name       string
		transport  string
		deployment string
		operation  domain.Operation
		body       string
		status     int
		wantErr    error
		wantCreate bool
		wantAfter  string
	}{
		{name: "failed not started retry", transport: "failed", deployment: "not_started", operation: domain.OperationRetry, wantCreate: true},
		{name: "failed redeploy rejected", transport: "failed", deployment: "not_started", operation: domain.OperationRedeploy, wantErr: ErrOperationNotAllowed},
		{name: "transport unknown retry with list failure", transport: "unknown", deployment: "locating", operation: domain.OperationRetry, status: http.StatusBadGateway, wantErr: ErrReconciliationUnavailable},
		{name: "transport unknown retry without match", transport: "unknown", deployment: "locating", operation: domain.OperationRetry, body: `[]`, wantCreate: true},
		{name: "deployment unknown redeploy without match", transport: "enqueued", deployment: "unknown", operation: domain.OperationRedeploy, body: `[{"deploymentId":"other","description":"attempt_id=another","status":"done"}]`, wantCreate: true},
		{name: "cancelled redeploy without match", transport: "enqueued", deployment: "cancelled", operation: domain.OperationRedeploy, body: `[]`, wantCreate: true},
		{name: "timeout redeploy without match", transport: "enqueued", deployment: "timeout", operation: domain.OperationRedeploy, body: `[]`, wantCreate: true},
		{name: "active rejected", transport: "enqueued", deployment: "locating", operation: domain.OperationRetry, wantErr: ErrDeploymentStillActive},
		{name: "done rejected", transport: "enqueued", deployment: "done", operation: domain.OperationRedeploy, wantErr: ErrAlreadySucceeded},
		{name: "retry discovers remote error", transport: "unknown", deployment: "locating", operation: domain.OperationRetry, body: `[{"deploymentId":"remote","description":"attempt_id=%s","status":"error"}]`, wantErr: ErrStateChanged, wantAfter: "error"},
		{name: "redeploy discovers remote error", transport: "enqueued", deployment: "error", operation: domain.OperationRedeploy, body: `[{"deploymentId":"remote","description":"attempt_id=%s","status":"error"}]`, wantCreate: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var attemptID string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.status != 0 {
					w.WriteHeader(test.status)
					return
				}
				body := test.body
				if body == "" {
					body = `[]`
				}
				_, _ = io.WriteString(w, strings.ReplaceAll(body, "%s", attemptID))
			}))
			defer server.Close()
			s, db, deliveryID, currentID := seedAttempt(t, server.URL, test.transport, test.deployment)
			attemptID = currentID
			attempts := NewAttempts(serviceManager(t, attemptConfig(server.URL), s), s, func() time.Time { return time.UnixMilli(10_000).UTC() })
			result, err := attempts.Execute(context.Background(), deliveryID, currentID, test.operation, "alice", "operator request")
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Execute() error = %v, want %v", err, test.wantErr)
			}
			if result.Created != test.wantCreate {
				t.Fatalf("Execute() created = %v, want %v", result.Created, test.wantCreate)
			}
			if test.wantCreate {
				if result.AttemptID == currentID || result.AttemptID == "" {
					t.Fatalf("manual attempt = %q, old = %q", result.AttemptID, currentID)
				}
				assertOperationAttempt(t, db, result.AttemptID, "alice", string(test.operation))
			}
			if test.wantAfter != "" {
				var got string
				if err := db.QueryRow(`SELECT deployment_status FROM delivery_attempts WHERE id = ?`, currentID).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != test.wantAfter {
					t.Fatalf("corrected deployment = %q, want %q", got, test.wantAfter)
				}
			}
			assertAttemptIdentityUnchanged(t, db, currentID)
		})
	}
}

func TestAttemptsReconciliationCorrectsRunningAndDone(t *testing.T) {
	// Break caught: accepting an unsafe click while its prior Dokploy deployment is still running or already done.
	for _, remoteStatus := range []string{"running", "done"} {
		t.Run(remoteStatus, func(t *testing.T) {
			var attemptID string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `[{"deploymentId":"remote-id","description":"source attempt_id=%s","status":%q}]`, attemptID, remoteStatus)
			}))
			defer server.Close()
			s, db, deliveryID, attemptID := seedAttempt(t, server.URL, "unknown", "locating")
			attempts := NewAttempts(serviceManager(t, attemptConfig(server.URL), s), s, func() time.Time { return time.UnixMilli(10_000).UTC() })
			if remoteStatus == "running" {
				attempts.SetTerminalHook(func(context.Context) error { return errors.New("retention failed") })
			}
			_, err := attempts.Execute(context.Background(), deliveryID, attemptID, domain.OperationRetry, "alice", "inspect first")
			wantErr := ErrDeploymentStillActive
			wantStatus := "running"
			if remoteStatus == "done" {
				wantErr, wantStatus = ErrAlreadySucceeded, "done"
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("Execute() error = %v, want %v", err, wantErr)
			}
			var gotStatus, deploymentID string
			var due, deadline, lastPolled *int64
			if err := db.QueryRow(`SELECT deployment_status, deployment_id, poll_due_at, monitoring_deadline_at, last_polled_at FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&gotStatus, &deploymentID, &due, &deadline, &lastPolled); err != nil {
				t.Fatal(err)
			}
			if gotStatus != wantStatus || deploymentID != "remote-id" {
				t.Fatalf("correction = %s/%s", gotStatus, deploymentID)
			}
			if remoteStatus == "running" && (due == nil || deadline == nil || *deadline != time.UnixMilli(10_000).Add(time.Minute).UnixMilli()) {
				t.Fatalf("running schedule = %v/%v", due, deadline)
			}
			if lastPolled != nil {
				t.Fatalf("last_polled_at = %v, want cleared", lastPolled)
			}
			assertAttemptIdentityUnchanged(t, db, attemptID)
		})
	}
}

func TestAttemptsReconciliationKeepsUnrecognizedRemoteStatusActive(t *testing.T) {
	// Break caught: creating a new attempt when reconciliation finds a null or version-specific active Dokploy status.
	for _, remoteStatus := range []string{`"queued"`, `null`} {
		t.Run(remoteStatus, func(t *testing.T) {
			var attemptID string
			var postCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					postCalls.Add(1)
				}
				_, _ = fmt.Fprintf(w, `[{"deploymentId":"remote-id","description":"source attempt_id=%s","status":%s}]`, attemptID, remoteStatus)
			}))
			defer server.Close()
			s, db, deliveryID, currentID := seedAttempt(t, server.URL, "unknown", "locating")
			attemptID = currentID
			attempts := NewAttempts(serviceManager(t, attemptConfig(server.URL), s), s, func() time.Time { return time.UnixMilli(10_000).UTC() })
			result, err := attempts.Execute(context.Background(), deliveryID, currentID, domain.OperationRetry, "alice", "inspect first")
			if !errors.Is(err, ErrDeploymentStillActive) || result.Created || result.AttemptID != currentID {
				t.Fatalf("Execute() = %#v/%v", result, err)
			}
			var status string
			var due, deadline *int64
			if err := db.QueryRow(`SELECT deployment_status, poll_due_at, monitoring_deadline_at FROM delivery_attempts WHERE id = ?`, currentID).Scan(&status, &due, &deadline); err != nil {
				t.Fatal(err)
			}
			if status != "unrecognized" || due == nil || deadline == nil {
				t.Fatalf("reconciled state = %s/%v/%v", status, due, deadline)
			}
			var attemptsCount int
			if err := db.QueryRow(`SELECT COUNT(*) FROM delivery_attempts WHERE delivery_id = ?`, deliveryID).Scan(&attemptsCount); err != nil {
				t.Fatal(err)
			}
			if attemptsCount != 1 || postCalls.Load() != 0 {
				t.Fatalf("attempts/POSTs = %d/%d", attemptsCount, postCalls.Load())
			}
		})
	}
}

func TestAttemptsPropagatesCurrentAttemptStorageFailure(t *testing.T) {
	// Break caught: treating an unavailable database as a stale-click conflict.
	s, _, deliveryID, currentID := seedAttempt(t, "http://127.0.0.1:1", "failed", "not_started")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	attempts := NewAttempts(serviceManager(t, attemptConfig("http://127.0.0.1:1"), s), s, time.Now)
	_, err := attempts.Execute(context.Background(), deliveryID, currentID, domain.OperationRetry, "alice", "database failure")
	if err == nil || errors.Is(err, ErrStateChanged) {
		t.Fatalf("Execute() error = %v, want propagated storage failure", err)
	}
}

func TestAttemptsCASAllowsOneSimultaneousClickAndDispatcherClaimsOnlyRetryCurrent(t *testing.T) {
	// Break caught: two clicks appending two retry attempts, or the dispatcher selecting an obsolete pending attempt.
	s, db, deliveryID, currentID := seedAttempt(t, "http://127.0.0.1:1", "failed", "not_started")
	attempts := NewAttempts(serviceManager(t, attemptConfig("http://127.0.0.1:1"), s), s, time.Now)
	var wg sync.WaitGroup
	results := make(chan AttemptResult, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := attempts.Execute(context.Background(), deliveryID, currentID, domain.OperationRetry, "alice", "double click")
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	var retryID string
	for result := range results {
		if result.Created {
			retryID = result.AttemptID
		}
	}
	created, changed := 0, 0
	for err := range errs {
		if err == nil {
			created++
		}
		if errors.Is(err, ErrStateChanged) {
			changed++
		}
	}
	if created != 1 || changed != 1 || retryID == "" {
		t.Fatalf("created/state-changed/retry = %d/%d/%q", created, changed, retryID)
	}
	if _, err := db.Exec(`UPDATE delivery_attempts SET transport_status = 'pending' WHERE id = ?`, currentID); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(20_000))
	if err != nil || !ok || claimed.AttemptID != retryID {
		t.Fatalf("ClaimPendingAttempt() = %#v/%v/%v, want retry %q", claimed, ok, err, retryID)
	}
}

func seedAttempt(t *testing.T, baseURL, transport, deployment string) (*store.Store, *sql.DB, string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hookfly.db")
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	generation, err := compileServiceConfig(attemptConfig(baseURL))
	if err != nil {
		t.Fatal(err)
	}
	target, found := generation.Target("production")
	if !found {
		t.Fatal("production target is missing")
	}
	result, err := s.Ingest(context.Background(), domain.IngestCommand{
		DeliveryID: t.Name(), ReceivedAt: time.UnixMilli(1_000), CanonicalEvent: domain.CanonicalEvent{Provider: "gitlab", Source: "test", Repository: "repository", Event: "pipeline"},
		RoutingResult: domain.RoutingDeploy, Deliveries: []domain.NewDelivery{{TargetID: "production", TargetSnapshot: target.Snapshot}},
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var deliveryID, attemptID string
	if err := db.QueryRow(`SELECT id, current_attempt_id FROM deliveries WHERE event_id = ?`, result.EventID).Scan(&deliveryID, &attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE delivery_attempts SET transport_status = ?, deployment_status = ?, request_json = '{"identity":"request"}', response_json = '{"identity":"response"}', last_polled_at = 123 WHERE id = ?`, transport, deployment, attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE deliveries SET transport_status = ?, deployment_status = ? WHERE id = ?`, transport, deployment, deliveryID); err != nil {
		t.Fatal(err)
	}
	return s, db, deliveryID, attemptID
}

func attemptConfig(baseURL string) *config.Bundle {
	return &config.Bundle{
		Global:             config.Global{Polling: config.Polling{Interval: config.Duration{Duration: time.Second}, Timeout: config.Duration{Duration: time.Minute}}},
		DokployConnections: []config.DokployConnection{{ID: "dokploy", BaseURL: baseURL, APIKey: "test-key"}},
		Targets:            []config.Target{{ID: "production", Type: "dokploy", Connection: "dokploy", ResourceType: "compose", ResourceID: "compose"}},
	}
}

func assertOperationAttempt(t *testing.T, db *sql.DB, attemptID, actor, wantKind string) {
	t.Helper()
	var kind, gotActor, transport, deployment string
	if err := db.QueryRow(`SELECT kind, actor, transport_status, deployment_status FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&kind, &gotActor, &transport, &deployment); err != nil {
		t.Fatal(err)
	}
	if kind != wantKind || gotActor != actor || transport != "pending" || deployment != "not_started" {
		t.Fatalf("operation attempt = %s/%s/%s/%s", kind, gotActor, transport, deployment)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE attempt_id = ? AND actor = ? AND action LIKE 'manual_%'`, attemptID, actor).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("manual actor audit rows = %d", count)
	}
	var afterJSON string
	if err := db.QueryRow(`SELECT after_json FROM audit_logs WHERE attempt_id = ? AND actor = ? AND action LIKE 'manual_%'`, attemptID, actor).Scan(&afterJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(afterJSON, "operator request") && !strings.Contains(afterJSON, "double click") {
		t.Fatalf("manual audit reason = %s", afterJSON)
	}
}

func assertAttemptIdentityUnchanged(t *testing.T, db *sql.DB, attemptID string) {
	t.Helper()
	var request, response string
	if err := db.QueryRow(`SELECT request_json, response_json FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&request, &response); err != nil {
		t.Fatal(err)
	}
	if request != `{"identity":"request"}` || response != `{"identity":"response"}` {
		t.Fatalf("old identity fields changed: %q/%q", request, response)
	}
}
