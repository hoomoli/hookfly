package worker

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/config"
	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/store"
)

func TestDispatchUsesConfiguredComposeAndMarksEnqueued(t *testing.T) {
	// Break caught: posting before the history cursor is persisted, or resolving connection/resource data from the webhook payload.
	var gotRequest map[string]any
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "startup-api-key" {
			t.Errorf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		methods = append(methods, r.Method)
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path != "/api/deployment.allByCompose" || r.URL.Query().Get("composeId") != "configured-compose" {
				t.Errorf("preflight request = %s", r.URL.RequestURI())
			}
			_, _ = io.WriteString(w, `[{"deploymentId":"before-send","composeId":"configured-compose","description":"Commit: old","status":"done","createdAt":"2026-08-12T02:00:00Z"}]`)
		case http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
				t.Errorf("decode request: %v", err)
			}
			_, _ = io.WriteString(w, `true`)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	s, db := openWorkerStore(t)
	override := config.Duration{Duration: 9 * time.Second}
	cfg := dispatcherConfig(server.URL, &override)
	eventID := ingestWorkerEvent(t, s, "configured-target", time.UnixMilli(1000), workerDeliveries(t, cfg))
	attemptID := attemptForEvent(t, db, eventID)
	now := time.UnixMilli(5000).UTC()
	dispatcher := NewDispatcher(workerManager(t, cfg, s), s, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })

	worked, err := dispatcher.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce() = %v/%v", worked, err)
	}
	if gotRequest["composeId"] != "configured-compose" {
		t.Fatalf("composeId = %#v", gotRequest["composeId"])
	}
	description, _ := gotRequest["description"].(string)
	if first := strings.Fields(description); len(first) == 0 || first[0] != "attempt_id="+attemptID {
		t.Fatalf("description = %q", description)
	}

	if !reflect.DeepEqual(methods, []string{http.MethodGet, http.MethodPost}) {
		t.Fatalf("request order = %#v", methods)
	}
	var transportStatus, deploymentStatus, cursor string
	var requestJSON, responseJSON []byte
	var pollDue, deadline int64
	if err := db.QueryRow(`
		SELECT transport_status, deployment_status, deployment_cursor, request_json, response_json, poll_due_at, monitoring_deadline_at
		FROM delivery_attempts WHERE id = ?`, attemptID).Scan(
		&transportStatus, &deploymentStatus, &cursor, &requestJSON, &responseJSON, &pollDue, &deadline,
	); err != nil {
		t.Fatal(err)
	}
	if transportStatus != "enqueued" || deploymentStatus != "locating" {
		t.Fatalf("state = %s/%s", transportStatus, deploymentStatus)
	}
	if cursor != "before-send" {
		t.Fatalf("deployment cursor = %q", cursor)
	}
	if pollDue != now.Add(2*time.Second).UnixMilli() || deadline != now.Add(9*time.Second).UnixMilli() {
		t.Fatalf("poll/deadline = %d/%d", pollDue, deadline)
	}
	combined := string(requestJSON) + string(responseJSON)
	for _, secret := range []string{"startup-api-key", "payload-key", "payload-compose", "http://payload.invalid"} {
		if strings.Contains(combined, secret) {
			t.Fatalf("snapshot contains forbidden value %q: %s", secret, combined)
		}
	}
}

func TestDispatchHTTPAcceptsWithoutPollingAndRedactsCredential(t *testing.T) {
	var gotBody map[string]any
	var deliveryID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/deploy" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-API-Token") != "http-secret" || r.Header.Get("Idempotency-Key") != deliveryID {
			t.Fatalf("headers = %#v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"token":"http-secret","state":"accepted"}`)
	}))
	defer server.Close()
	s, db := openWorkerStore(t)
	cfg := httpDispatcherConfig(server.URL)
	eventID := ingestWorkerEvent(t, s, "http-accepted", time.UnixMilli(1000), workerDeliveries(t, cfg))
	attemptID := attemptForEvent(t, db, eventID)
	if err := db.QueryRow(`SELECT id FROM deliveries WHERE event_id = ?`, eventID).Scan(&deliveryID); err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(workerManager(t, cfg, s), s, discardLogger(), func() time.Time { return time.UnixMilli(5000) })
	worked, err := dispatcher.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce() = %v/%v", worked, err)
	}
	assertWorkerAttemptState(t, db, attemptID, "enqueued", "done")
	var pollDue, deadline sql.NullInt64
	var requestJSON, responseJSON []byte
	if err := db.QueryRow(`SELECT poll_due_at, monitoring_deadline_at, request_json, response_json FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&pollDue, &deadline, &requestJSON, &responseJSON); err != nil {
		t.Fatal(err)
	}
	if pollDue.Valid || deadline.Valid || strings.Contains(string(requestJSON), "http-secret") || strings.Contains(string(responseJSON), "http-secret") {
		t.Fatalf("poll/evidence = %#v/%#v/%s/%s", pollDue, deadline, requestJSON, responseJSON)
	}
	if gotBody["attempt_id"] != attemptID {
		t.Fatalf("body = %#v", gotBody)
	}
}

func TestDispatchHTTPFailureIsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	s, db := openWorkerStore(t)
	cfg := httpDispatcherConfig(server.URL)
	eventID := ingestWorkerEvent(t, s, "http-failed", time.UnixMilli(1000), workerDeliveries(t, cfg))
	attemptID := attemptForEvent(t, db, eventID)
	dispatcher := NewDispatcher(workerManager(t, cfg, s), s, discardLogger(), func() time.Time { return time.UnixMilli(5000) })
	if worked, err := dispatcher.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("RunOnce() = %v/%v", worked, err)
	}
	assertWorkerAttemptState(t, db, attemptID, "failed", "not_started")
	if got := domain.AllowedOperations(domain.TransportFailed, domain.DeploymentNotStarted); !reflect.DeepEqual(got, []domain.Operation{domain.OperationRetry}) {
		t.Fatalf("AllowedOperations() = %#v", got)
	}
}

func TestDispatchHTTPUncertainResultIsRetryableWithoutPolling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatal(err)
		}
		_ = connection.Close()
	}))
	defer server.Close()
	s, db := openWorkerStore(t)
	cfg := httpDispatcherConfig(server.URL)
	eventID := ingestWorkerEvent(t, s, "http-unknown", time.UnixMilli(1000), workerDeliveries(t, cfg))
	attemptID := attemptForEvent(t, db, eventID)
	dispatcher := NewDispatcher(workerManager(t, cfg, s), s, discardLogger(), func() time.Time { return time.UnixMilli(5000) })
	if worked, err := dispatcher.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("RunOnce() = %v/%v", worked, err)
	}
	assertWorkerAttemptState(t, db, attemptID, "unknown", "unknown")
	var pollDue, deadline sql.NullInt64
	if err := db.QueryRow(`SELECT poll_due_at, monitoring_deadline_at FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&pollDue, &deadline); err != nil {
		t.Fatal(err)
	}
	if pollDue.Valid || deadline.Valid {
		t.Fatalf("unexpected polling schedule = %#v/%#v", pollDue, deadline)
	}
}

func TestDispatchForwardReplaysOriginalRequestAcrossRetry(t *testing.T) {
	// Break caught: rebuilding from canonical fields, dropping credentials, or reading mutable request data on retry.
	type received struct {
		method, query, host, body string
		headers                   http.Header
	}
	var requests []received
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, received{method: r.Method, query: r.URL.RawQuery, host: r.Host, body: string(body), headers: r.Header.Clone()})
		if len(requests) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	s, db := openWorkerStore(t)
	cfg := forwardDispatcherConfig(server.URL+"/receiver", "origin")
	command := domain.IngestCommand{
		DeliveryID: "forward-delivery", ReceivedAt: time.UnixMilli(1000),
		CanonicalEvent: domain.CanonicalEvent{Provider: "gitlab", Source: "test", Repository: "repository", Event: "pipeline"},
		RoutingResult:  domain.RoutingDeploy, Deliveries: workerDeliveries(t, cfg),
		PayloadJSON: []byte(`{"ref":"main","unchanged":"a b"}`),
		RequestJSON: []byte(`{"version":1,"method":"POST","host":"hookfly.example.invalid","raw_query":"token=a%2Bb&token=second","headers":{"Content-Type":["application/json"],"X-Gitlab-Token":["gitlab-secret"],"X-Hub-Signature-256":["sha256=signature"],"X-Repeated":["first","second"]}}`),
	}
	result, err := s.Ingest(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	firstAttempt := attemptForEvent(t, db, result.EventID)
	dispatcher := NewDispatcher(workerManager(t, cfg, s), s, discardLogger(), func() time.Time { return time.UnixMilli(5000) })
	if worked, err := dispatcher.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("first RunOnce() = %v/%v", worked, err)
	}
	assertWorkerAttemptState(t, db, firstAttempt, "failed", "not_started")

	current, err := s.CurrentAttempt(context.Background(), deliveryForEvent(t, db, result.EventID))
	if err != nil {
		t.Fatal(err)
	}
	secondAttempt, err := s.ApplyManualUpdate(context.Background(), store.ManualUpdate{
		Expected: current, Create: true, Operation: domain.OperationRetry, ActorID: "operator", At: time.UnixMilli(6000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := dispatcher.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("retry RunOnce() = %v/%v", worked, err)
	}
	assertWorkerAttemptState(t, db, secondAttempt, "enqueued", "done")

	if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Fatalf("forwarded requests = %#v", requests)
	}
	got := requests[0]
	if got.method != http.MethodPost || got.query != "token=a%2Bb&token=second" || got.host != "hookfly.example.invalid" || got.body != string(command.PayloadJSON) {
		t.Fatalf("forwarded request = %#v", got)
	}
	if got.headers.Get("X-Gitlab-Token") != "gitlab-secret" || got.headers.Get("X-Hub-Signature-256") != "sha256=signature" || !reflect.DeepEqual(got.headers.Values("X-Repeated"), []string{"first", "second"}) {
		t.Fatalf("forwarded headers = %#v", got.headers)
	}
	var evidence []byte
	if err := db.QueryRow(`SELECT request_json FROM delivery_attempts WHERE id = ?`, secondAttempt).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"gitlab-secret", "sha256=signature", "token=a%2Bb", `\"unchanged\"`} {
		if bytes.Contains(evidence, []byte(forbidden)) {
			t.Fatalf("request evidence exposed %q: %s", forbidden, evidence)
		}
	}
}

func TestDispatchPersistsSafeRequestEnvelopeBeforeDeploymentPOSTCompletes(t *testing.T) {
	// Break caught: recording only after the POST returns, omitting the endpoint, or persisting the API key.
	postStarted := make(chan struct{})
	releasePost := make(chan struct{})
	var releaseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, `[]`)
		case http.MethodPost:
			close(postStarted)
			<-releasePost
			_, _ = io.WriteString(w, `true`)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releasePost) })
		server.Close()
	})

	s, db := openWorkerStore(t)
	cfg := dispatcherConfig(server.URL, nil)
	eventID := ingestWorkerEvent(t, s, "durable-request-envelope", time.UnixMilli(1000), workerDeliveries(t, cfg))
	attemptID := attemptForEvent(t, db, eventID)
	dispatcher := NewDispatcher(workerManager(t, cfg, s), s, discardLogger(), func() time.Time { return time.UnixMilli(5000) })

	done := make(chan error, 1)
	go func() {
		worked, err := dispatcher.RunOnce(context.Background())
		if err == nil && !worked {
			err = errors.New("dispatcher did not claim work")
		}
		done <- err
	}()
	select {
	case <-postStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("deployment POST did not start")
	}

	var requestJSON []byte
	var requestAt int64
	if err := db.QueryRow(`SELECT request_json, request_at FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&requestJSON, &requestAt); err != nil {
		t.Fatal(err)
	}
	if requestAt != time.UnixMilli(5000).UnixMilli() {
		t.Fatalf("request_at = %d", requestAt)
	}
	want := map[string]any{
		"version": float64(1), "method": "POST", "url": server.URL + "/api/compose.deploy",
		"headers": map[string]any{"Content-Type": "application/json"},
		"body":    map[string]any{"composeId": "configured-compose", "description": "attempt_id=" + attemptID},
	}
	var got map[string]any
	if err := json.Unmarshal(requestJSON, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request_json = %#v\nwant         = %#v", got, want)
	}
	if strings.Contains(string(requestJSON), "startup-api-key") || strings.Contains(string(requestJSON), "x-api-key") {
		t.Fatalf("request envelope contains credential material: %s", requestJSON)
	}

	releaseOnce.Do(func() { close(releasePost) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDispatchPreflightFailureDoesNotSendDeployment(t *testing.T) {
	// Break caught: issuing a non-idempotent POST without a durable history boundary.
	var getCalls, postCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			getCalls.Add(1)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		postCalls.Add(1)
		_, _ = io.WriteString(w, `true`)
	}))
	defer server.Close()
	s, db := openWorkerStore(t)
	cfg := dispatcherConfig(server.URL, nil)
	eventID := ingestWorkerEvent(t, s, "preflight-failure", time.UnixMilli(1000), workerDeliveries(t, cfg))
	attemptID := attemptForEvent(t, db, eventID)
	dispatcher := NewDispatcher(workerManager(t, cfg, s), s, discardLogger(), func() time.Time { return time.UnixMilli(5000) })

	worked, err := dispatcher.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce() = %v/%v", worked, err)
	}
	assertWorkerAttemptState(t, db, attemptID, "failed", "not_started")
	var requestAt sql.NullInt64
	if err := db.QueryRow(`SELECT request_at FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&requestAt); err != nil {
		t.Fatal(err)
	}
	if requestAt.Valid {
		t.Fatalf("preflight failure request_at = %d", requestAt.Int64)
	}
	if getCalls.Load() != 1 || postCalls.Load() != 0 {
		t.Fatalf("GET/POST calls = %d/%d", getCalls.Load(), postCalls.Load())
	}
}

func TestDispatchCanceledAfterPreflightDoesNotRecordDeploymentRequest(t *testing.T) {
	// Break caught: recording deployment evidence before Client.Deploy accepts the request context.
	ctx, cancel := context.WithCancel(context.Background())
	var postCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `[]`)
			cancel()
			return
		}
		postCalls.Add(1)
		_, _ = io.WriteString(w, `true`)
	}))
	defer server.Close()
	s, db := openWorkerStore(t)
	cfg := dispatcherConfig(server.URL, nil)
	eventID := ingestWorkerEvent(t, s, "canceled-after-preflight", time.UnixMilli(1000), workerDeliveries(t, cfg))
	attemptID := attemptForEvent(t, db, eventID)
	dispatcher := NewDispatcher(workerManager(t, cfg, s), s, discardLogger(), func() time.Time { return time.UnixMilli(5000) })

	worked, err := dispatcher.RunOnce(ctx)
	if err != nil || !worked {
		t.Fatalf("RunOnce() = %v/%v", worked, err)
	}
	var requestJSON []byte
	var requestAt sql.NullInt64
	if err := db.QueryRow(`SELECT request_json, request_at FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&requestJSON, &requestAt); err != nil {
		t.Fatal(err)
	}
	if requestJSON != nil || requestAt.Valid || postCalls.Load() != 0 {
		t.Fatalf("request_json/request_at/POST = %q/%#v/%d", requestJSON, requestAt, postCalls.Load())
	}
}

func TestDispatchRedactsRequestEvidenceWhenBodyMatchesAPIKey(t *testing.T) {
	// Break caught: leaking a configured API key through a coincidentally equal Compose ID in durable evidence.
	var postCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		postCalls.Add(1)
		_, _ = io.WriteString(w, `true`)
	}))
	defer server.Close()
	s, db := openWorkerStore(t)
	cfg := dispatcherConfig(server.URL, nil)
	cfg.DokployConnections[0].APIKey = cfg.Targets[0].ResourceID
	eventID := ingestWorkerEvent(t, s, "credential-collision", time.UnixMilli(1000), workerDeliveries(t, cfg))
	attemptID := attemptForEvent(t, db, eventID)
	dispatcher := NewDispatcher(workerManager(t, cfg, s), s, discardLogger(), func() time.Time { return time.UnixMilli(5000) })

	worked, err := dispatcher.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce() = %v/%v", worked, err)
	}
	var requestJSON []byte
	if err := db.QueryRow(`SELECT request_json FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&requestJSON); err != nil {
		t.Fatal(err)
	}
	if string(requestJSON) != `{"redacted":true}` || strings.Contains(string(requestJSON), cfg.DokployConnections[0].APIKey) || postCalls.Load() != 1 {
		t.Fatalf("request_json/POST = %s/%d", requestJSON, postCalls.Load())
	}
	detail, err := s.GetEventDetail(context.Background(), eventID)
	if err != nil {
		t.Fatal(err)
	}
	if got := detail.Deliveries[0].Attempts[0].DeployCommand; got != nil {
		t.Fatalf("credential-colliding deploy command = %q", *got)
	}
}

func TestDispatchScrubsUnicodeEscapedAPIKeyFromStoredResponse(t *testing.T) {
	// Break caught: decoding an escaped API key under a non-sensitive field and persisting its literal value.
	escapedKey := jsonUnicodeEscapeASCII("startup-api-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		_, _ = io.WriteString(w, `{"message":{"items":["`+escapedKey+`"]}}`)
	}))
	defer server.Close()
	s, db := openWorkerStore(t)
	cfg := dispatcherConfig(server.URL, nil)
	eventID := ingestWorkerEvent(t, s, "escaped-response-key", time.UnixMilli(1000), workerDeliveries(t, cfg))
	attemptID := attemptForEvent(t, db, eventID)
	dispatcher := NewDispatcher(workerManager(t, cfg, s), s, discardLogger(), func() time.Time { return time.UnixMilli(5000) })

	worked, err := dispatcher.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce() = %v/%v", worked, err)
	}
	var responseJSON []byte
	if err := db.QueryRow(`SELECT response_json FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&responseJSON); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(responseJSON) || strings.Contains(string(responseJSON), "startup-api-key") || !strings.Contains(string(responseJSON), "[REDACTED]") {
		t.Fatalf("stored response snapshot = %s", responseJSON)
	}
}

func TestDispatchScrubsNumericAPIKeyFromStoredResponse(t *testing.T) {
	// Break caught: persisting a numeric response scalar whose JSON representation equals the configured API key.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		_, _ = io.WriteString(w, `{"message":12345}`)
	}))
	defer server.Close()
	s, db := openWorkerStore(t)
	cfg := dispatcherConfig(server.URL, nil)
	cfg.DokployConnections[0].APIKey = "12345"
	eventID := ingestWorkerEvent(t, s, "numeric-response-key", time.UnixMilli(1000), workerDeliveries(t, cfg))
	attemptID := attemptForEvent(t, db, eventID)
	dispatcher := NewDispatcher(workerManager(t, cfg, s), s, discardLogger(), func() time.Time { return time.UnixMilli(5000) })

	worked, err := dispatcher.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce() = %v/%v", worked, err)
	}
	var responseJSON []byte
	if err := db.QueryRow(`SELECT response_json FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&responseJSON); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(responseJSON) || strings.Contains(string(responseJSON), "12345") || !strings.Contains(string(responseJSON), "[REDACTED]") {
		t.Fatalf("stored response snapshot = %s", responseJSON)
	}
}

func TestDispatchMarksDefinitiveHTTPFailureWithoutRetry(t *testing.T) {
	// Break caught: retrying a non-idempotent POST or starting monitoring after an observed HTTP rejection.
	var getCalls, postCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			getCalls.Add(1)
			_, _ = io.WriteString(w, `[]`)
			return
		}
		postCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"failed"}`)
	}))
	defer server.Close()
	s, db := openWorkerStore(t)
	cfg := dispatcherConfig(server.URL, nil)
	eventID := ingestWorkerEvent(t, s, "http-failure", time.UnixMilli(1000), workerDeliveries(t, cfg))
	attemptID := attemptForEvent(t, db, eventID)
	dispatcher := NewDispatcher(workerManager(t, cfg, s), s, discardLogger(), func() time.Time { return time.UnixMilli(5000) })
	worked, err := dispatcher.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce() = %v/%v", worked, err)
	}
	if worked, err := dispatcher.RunOnce(context.Background()); err != nil || worked {
		t.Fatalf("second RunOnce() = %v/%v", worked, err)
	}
	assertWorkerAttemptState(t, db, attemptID, "failed", "not_started")
	if getCalls.Load() != 1 || postCalls.Load() != 1 {
		t.Fatalf("GET/POST calls = %d/%d", getCalls.Load(), postCalls.Load())
	}
}

func TestDispatchSlow2xxStartsTransitionWindowAfterResponse(t *testing.T) {
	// Break caught: starting the transition deadline before a slow successful POST and leaving the attempt stuck sending.
	const transitionWindow = 100 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		time.Sleep(transitionWindow + 50*time.Millisecond)
		_, _ = io.WriteString(w, `true`)
	}))
	defer server.Close()
	s, db := openWorkerStore(t)
	cfg := dispatcherConfig(server.URL, nil)
	eventID := ingestWorkerEvent(t, s, "slow-success", time.UnixMilli(1000), workerDeliveries(t, cfg))
	attemptID := attemptForEvent(t, db, eventID)
	dispatcher := NewDispatcher(workerManager(t, cfg, s), s, discardLogger(), func() time.Time { return time.UnixMilli(5000) })
	dispatcher.transitionTimeout = transitionWindow

	worked, err := dispatcher.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce() = %v/%v", worked, err)
	}
	assertWorkerAttemptState(t, db, attemptID, "enqueued", "locating")
	var deliveryTransport, deliveryDeployment string
	if err := db.QueryRow(`SELECT transport_status, deployment_status FROM deliveries WHERE current_attempt_id = ?`, attemptID).Scan(&deliveryTransport, &deliveryDeployment); err != nil {
		t.Fatal(err)
	}
	if deliveryTransport != "enqueued" || deliveryDeployment != "locating" {
		t.Fatalf("delivery state = %s/%s", deliveryTransport, deliveryDeployment)
	}
	var actionCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE attempt_id = ? AND action = 'transport_enqueued'`, attemptID).Scan(&actionCount); err != nil {
		t.Fatal(err)
	}
	if actionCount != 1 {
		t.Fatalf("transport_enqueued audit rows = %d", actionCount)
	}
}

func TestDispatchMarksResetAsUnknownAndLocating(t *testing.T) {
	// Break caught: treating a reset after the request may have been sent as safe to fail or retry.
	var postCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		postCalls.Add(1)
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	}))
	defer server.Close()
	s, db := openWorkerStore(t)
	cfg := dispatcherConfig(server.URL, nil)
	eventID := ingestWorkerEvent(t, s, "reset", time.UnixMilli(1000), workerDeliveries(t, cfg))
	attemptID := attemptForEvent(t, db, eventID)
	dispatcher := NewDispatcher(workerManager(t, cfg, s), s, discardLogger(), func() time.Time { return time.UnixMilli(5000) })
	worked, err := dispatcher.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce() = %v/%v", worked, err)
	}
	assertWorkerAttemptState(t, db, attemptID, "unknown", "locating")
	var deadline int64
	if err := db.QueryRow(`SELECT monitoring_deadline_at FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	if deadline != time.UnixMilli(5000).Add(30*time.Second).UnixMilli() {
		t.Fatalf("global monitoring deadline = %d", deadline)
	}
	if postCalls.Load() != 1 {
		t.Fatalf("POST calls = %d", postCalls.Load())
	}
}

func TestDispatchSerializesOneTargetInReceivedOrder(t *testing.T) {
	// Break caught: concurrent workers posting a later delivery for the same target before its predecessor completes.
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseFirst) }) })
	descriptions := make(chan string, 2)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		descriptions <- request["description"].(string)
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		_, _ = io.WriteString(w, `true`)
	}))
	defer server.Close()
	s, db := openWorkerStore(t)
	cfg := dispatcherConfig(server.URL, nil)
	firstEvent := ingestWorkerEvent(t, s, "ordered-first", time.UnixMilli(1000), workerDeliveries(t, cfg))
	secondEvent := ingestWorkerEvent(t, s, "ordered-second", time.UnixMilli(2000), workerDeliveries(t, cfg))
	firstAttempt := attemptForEvent(t, db, firstEvent)
	secondAttempt := attemptForEvent(t, db, secondEvent)
	dispatcher := NewDispatcher(workerManager(t, cfg, s), s, discardLogger(), time.Now)

	firstDone := make(chan error, 1)
	go func() {
		worked, err := dispatcher.RunOnce(context.Background())
		if err == nil && !worked {
			err = context.Canceled
		}
		firstDone <- err
	}()
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first POST did not start")
	}
	secondRun := make(chan struct {
		worked bool
		err    error
	}, 1)
	go func() {
		worked, err := dispatcher.RunOnce(context.Background())
		secondRun <- struct {
			worked bool
			err    error
		}{worked, err}
	}()
	select {
	case result := <-secondRun:
		if result.err != nil || result.worked {
			t.Fatalf("concurrent same-target RunOnce() = %v/%v", result.worked, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("same-target RunOnce blocked instead of reporting no claimable work")
	}
	if calls.Load() != 1 {
		t.Fatalf("POST calls before release = %d", calls.Load())
	}
	releaseOnce.Do(func() { close(releaseFirst) })
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	setWorkerAttemptState(t, db, firstAttempt, "enqueued", "running", "deployment-first")
	worked, err := dispatcher.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("later RunOnce() = %v/%v", worked, err)
	}
	firstDescription, secondDescription := <-descriptions, <-descriptions
	if strings.Fields(firstDescription)[0] != "attempt_id="+firstAttempt || strings.Fields(secondDescription)[0] != "attempt_id="+secondAttempt {
		t.Fatalf("descriptions = %q then %q", firstDescription, secondDescription)
	}
}

func TestDispatchRunTreatsCancellationFromRunOnceAsGracefulShutdown(t *testing.T) {
	// Break caught: cancellation racing with a SQLite claim surfacing context.Canceled as an application failure.
	ctx, cancel := context.WithCancel(context.Background())
	runOnce := func(runContext context.Context) (bool, error) {
		cancel()
		return false, runContext.Err()
	}
	if err := runDispatchLoop(ctx, time.Hour, runOnce); err != nil {
		t.Fatalf("runDispatchLoop() error = %v", err)
	}
}

func TestDispatchTerminalFailureRunsRetentionHookAndPropagatesItsError(t *testing.T) {
	// Break caught: terminal transport failures bypassing pruning, or retention failures being silently discarded.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer server.Close()
	s, _ := openWorkerStore(t)
	cfg := dispatcherConfig(server.URL, nil)
	ingestWorkerEvent(t, s, "terminal-hook", time.UnixMilli(1), workerDeliveries(t, cfg))
	dispatcher := NewDispatcher(workerManager(t, cfg, s), s, discardLogger(), func() time.Time { return time.UnixMilli(2) })
	want := errors.New("retention failed")
	var calls atomic.Int32
	dispatcher.SetTerminalHook(func(context.Context) error {
		calls.Add(1)
		return want
	})
	_, err := dispatcher.RunOnce(context.Background())
	if !errors.Is(err, want) || calls.Load() != 1 {
		t.Fatalf("RunOnce() / retention calls = %v / %d", err, calls.Load())
	}
}

func openWorkerStore(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hookfly.db")
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		_ = s.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		_ = s.Close()
	})
	return s, db
}

func ingestWorkerEvent(t *testing.T, s *store.Store, key string, receivedAt time.Time, deliveries []domain.NewDelivery) string {
	t.Helper()
	result, err := s.Ingest(context.Background(), domain.IngestCommand{
		DeliveryID: key, ReceivedAt: receivedAt,
		CanonicalEvent: domain.CanonicalEvent{Provider: "gitlab", Source: "test", Repository: "repository", Event: "pipeline"},
		RoutingResult:  domain.RoutingDeploy, Deliveries: deliveries,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.EventID
}

func attemptForEvent(t *testing.T, db *sql.DB, eventID string) string {
	t.Helper()
	var attemptID string
	if err := db.QueryRow(`SELECT current_attempt_id FROM deliveries WHERE event_id = ?`, eventID).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}
	return attemptID
}

func deliveryForEvent(t *testing.T, db *sql.DB, eventID string) string {
	t.Helper()
	var deliveryID string
	if err := db.QueryRow(`SELECT id FROM deliveries WHERE event_id = ?`, eventID).Scan(&deliveryID); err != nil {
		t.Fatal(err)
	}
	return deliveryID
}

func assertWorkerAttemptState(t *testing.T, db *sql.DB, attemptID, wantTransport, wantDeployment string) {
	t.Helper()
	var transport, deployment string
	if err := db.QueryRow(`SELECT transport_status, deployment_status FROM delivery_attempts WHERE id = ?`, attemptID).Scan(&transport, &deployment); err != nil {
		t.Fatal(err)
	}
	if transport != wantTransport || deployment != wantDeployment {
		t.Fatalf("attempt state = %s/%s", transport, deployment)
	}
}

func dispatcherConfig(baseURL string, timeout *config.Duration) *config.Bundle {
	return &config.Bundle{
		Global:             config.Global{Polling: config.Polling{Interval: config.Duration{Duration: 2 * time.Second}, Timeout: config.Duration{Duration: 30 * time.Second}}},
		DokployConnections: []config.DokployConnection{{ID: "dokploy", BaseURL: baseURL, APIKey: "startup-api-key"}},
		Targets: []config.Target{{
			ID: "production", Type: "dokploy", Connection: "dokploy", ResourceType: "compose", ResourceID: "configured-compose", PollTimeout: timeout,
		}},
	}
}

func httpDispatcherConfig(baseURL string) *config.Bundle {
	return &config.Bundle{
		Global:          config.Global{Polling: config.Polling{Interval: config.Duration{Duration: 2 * time.Second}, Timeout: config.Duration{Duration: 30 * time.Second}}},
		HTTPConnections: []config.HTTPConnection{{ID: "admin", BaseURL: baseURL, AllowPrivateNetwork: true, Auth: config.HTTPAuthentication{Type: "api_key", Value: "http-secret", Header: "X-API-Token"}}},
		Targets: []config.Target{{
			ID: "production", Type: "http", Connection: "admin", Method: http.MethodPost, Path: "/api/deploy",
			Body: &config.HTTPBody{Type: "json", Value: map[string]any{"attempt_id": "{{ attempt.id }}"}}, SuccessStatuses: []int{http.StatusAccepted},
		}},
	}
}

func forwardDispatcherConfig(targetURL, host string) *config.Bundle {
	return &config.Bundle{
		Global:  config.Global{Polling: config.Polling{Interval: config.Duration{Duration: 2 * time.Second}, Timeout: config.Duration{Duration: 30 * time.Second}}},
		Targets: []config.Target{{ID: "production", Type: "forward", URL: targetURL, Host: host, AllowPrivateNetwork: true}},
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func jsonUnicodeEscapeASCII(value string) string {
	const hexadecimal = "0123456789abcdef"
	var escaped strings.Builder
	for _, character := range []byte(value) {
		escaped.WriteString(`\u00`)
		escaped.WriteByte(hexadecimal[character>>4])
		escaped.WriteByte(hexadecimal[character&0x0f])
	}
	return escaped.String()
}
