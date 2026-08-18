package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/auth"
	"github.com/hoomoli/hookfly/internal/config"
	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/runtimecfg"
	"github.com/hoomoli/hookfly/internal/store"
)

func newTestApp(manager *runtimecfg.Manager, durable *store.Store, logger *slog.Logger) *App {
	return New(manager, durable, logger, auth.NewDevelopmentService())
}

func TestComponentRunErrorIgnoresErrorsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := componentRunError(ctx, errors.New("transaction already closed")); err != nil {
		t.Fatalf("componentRunError() = %v, want nil after cancellation", err)
	}
}

func TestPublicMuxExposesOnlyHook(t *testing.T) {
	gitlabHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	githubHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	harborHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	mux := NewPublicMux(gitlabHandler, githubHandler, harborHandler)
	for _, path := range []string{"/api/v1/repositories", "/api/v1/connections", "/api/v1/connections/primary/resources", "/api/v1/events", "/api/v1/events/event-1", "/api/v1/deliveries/delivery-1/attempts", "/api/v1/health", "/api/v1/auth/login", "/api/v1/auth/callback", "/api/v1/auth/session", "/api/v1/auth/logout", "/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s returned %d", path, rr.Code)
		}
	}

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/hooks/gitlab", nil))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("POST /hooks/gitlab returned %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/hooks/github/github-primary", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("POST /hooks/github/github-primary returned %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/hooks/harbor/harbor-primary", nil))
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST /hooks/harbor/harbor-primary returned %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/hooks/gitlab", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /hooks/gitlab returned %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/hooks/gitlab/gitlab-primary", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("legacy GitLab source path returned %d", rr.Code)
	}
}

func TestServersAreSeparateAndDefensive(t *testing.T) {
	application := newTestApp(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	publicServer, adminServer := application.servers()

	if publicServer == adminServer {
		t.Fatal("public and admin listeners share a server")
	}
	for name, server := range map[string]*http.Server{"public": publicServer, "admin": adminServer} {
		if server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 10*time.Second || server.WriteTimeout != 10*time.Second || server.IdleTimeout != 60*time.Second {
			t.Fatalf("%s server timeouts = header:%v read:%v write:%v idle:%v", name, server.ReadHeaderTimeout, server.ReadTimeout, server.WriteTimeout, server.IdleTimeout)
		}
	}

	for _, path := range []string{"/", "/hooks/gitlab"} {
		rr := httptest.NewRecorder()
		adminServer.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("admin %s returned %d", path, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	adminServer.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("admin health returned %d", rr.Code)
	}
}

func TestAdminMuxUsesAppAttemptsServiceAndConfiguredRepositories(t *testing.T) {
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hookfly.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cfg := &config.Bundle{
		DokployConnections: []config.DokployConnection{{ID: "dokploy", BaseURL: "https://dokploy.example.invalid", APIKey: "app-test-key"}},
		Targets:            []config.Target{{ID: "production", Type: "dokploy", Connection: "dokploy", ResourceType: "compose", ResourceID: "compose-1"}},
	}
	manager := appManager(t, cfg, database)
	target, _ := manager.Current().Target("production")
	result, err := database.Ingest(context.Background(), domain.IngestCommand{
		DeliveryID: "app-admin", ReceivedAt: time.UnixMilli(1000),
		CanonicalEvent: domain.CanonicalEvent{Provider: "gitlab", Source: "test", Repository: "4", Event: "pipeline"}, RoutingResult: domain.RoutingDeploy,
		Deliveries: []domain.NewDelivery{{TargetID: "production", TargetSnapshot: target.Snapshot}},
	})
	if err != nil {
		t.Fatal(err)
	}
	work, claimed, err := database.ClaimPendingAttempt(context.Background(), time.UnixMilli(2000))
	if err != nil || !claimed {
		t.Fatalf("claim = %#v/%v/%v", work, claimed, err)
	}
	if err := database.MarkTransportFailed(context.Background(), work.AttemptID, []byte(`{}`), []byte(`{}`), time.UnixMilli(3000)); err != nil {
		t.Fatal(err)
	}
	application := newTestApp(manager, database, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, adminServer := application.servers()

	rr := httptest.NewRecorder()
	adminServer.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/repositories", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != `{"repositories":[]}`+"\n" {
		t.Fatalf("repositories = %d %s", rr.Code, rr.Body.String())
	}

	body := []byte(`{"expected_current_attempt_id":"` + work.AttemptID + `","operation":"retry"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/deliveries/"+work.DeliveryID+"/attempts", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	adminServer.Handler.ServeHTTP(rr, request)
	if rr.Code != http.StatusCreated {
		t.Fatalf("manual retry = %d %s", rr.Code, rr.Body.String())
	}
	detail, err := database.GetEventDetail(context.Background(), result.EventID)
	if err != nil {
		t.Fatal(err)
	}
	attempts := detail.Deliveries[0].Attempts
	if len(attempts) != 2 || attempts[1].Actor == nil || *attempts[1].Actor != "wireguard-anonymous" || attempts[1].Kind != "retry" {
		t.Fatalf("manual attempts = %#v", attempts)
	}
}

func TestRunGracefullyStopsBothServersOnCancellation(t *testing.T) {
	application := newTestApp(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	application.PublicAddr = "127.0.0.1:0"
	application.AdminAddr = "127.0.0.1:0"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestRunStartsDispatcherAndStopsItWithServers(t *testing.T) {
	// Break caught: wiring only the HTTP listeners and leaving durable pending attempts undispatched.
	posted := make(chan struct{}, 1)
	dokployServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posted <- struct{}{}
		_, _ = io.WriteString(w, `true`)
	}))
	defer dokployServer.Close()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hookfly.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cfg := &config.Bundle{
		Global:             config.Global{Polling: config.Polling{Interval: config.Duration{Duration: time.Second}, Timeout: config.Duration{Duration: time.Minute}}},
		DokployConnections: []config.DokployConnection{{ID: "dokploy", BaseURL: dokployServer.URL, APIKey: "app-test-key"}},
		Targets:            []config.Target{{ID: "production", Type: "dokploy", Connection: "dokploy", ResourceType: "compose", ResourceID: "compose-1"}},
	}
	manager := appManager(t, cfg, database)
	target, _ := manager.Current().Target("production")
	if _, err := database.Ingest(context.Background(), domain.IngestCommand{
		DeliveryID: "app-dispatch", ReceivedAt: time.UnixMilli(1000),
		CanonicalEvent: domain.CanonicalEvent{Provider: "gitlab", Source: "test", Repository: "repository", Event: "pipeline"}, RoutingResult: domain.RoutingDeploy,
		Deliveries: []domain.NewDelivery{{TargetID: "production", TargetSnapshot: target.Snapshot}},
	}); err != nil {
		t.Fatal(err)
	}
	application := newTestApp(manager, database, slog.New(slog.NewTextHandler(io.Discard, nil)))
	application.PublicAddr = "127.0.0.1:0"
	application.AdminAddr = "127.0.0.1:0"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	select {
	case <-posted:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("dispatcher did not enqueue pending attempt")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop dispatcher and servers")
	}
}

func TestRunRecoversSendingAndPollsWithoutReposting(t *testing.T) {
	// Break caught: starting dispatcher before recovery and replaying an interrupted non-idempotent deployment POST, or omitting the poller.
	queried := make(chan struct{}, 1)
	var postCalls atomic.Int32
	dokployServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/deployment.allByCompose":
			select {
			case queried <- struct{}{}:
			default:
			}
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPost:
			postCalls.Add(1)
			_, _ = io.WriteString(w, `true`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer dokployServer.Close()
	databasePath := filepath.Join(t.TempDir(), "hookfly.db")
	database, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cfg := &config.Bundle{
		Global:             config.Global{Polling: config.Polling{Interval: config.Duration{Duration: 10 * time.Millisecond}, Timeout: config.Duration{Duration: time.Minute}}},
		DokployConnections: []config.DokployConnection{{ID: "dokploy", BaseURL: dokployServer.URL, APIKey: "app-test-key"}},
		Targets:            []config.Target{{ID: "production", Type: "dokploy", Connection: "dokploy", ResourceType: "compose", ResourceID: "compose-1"}},
	}
	manager := appManager(t, cfg, database)
	target, _ := manager.Current().Target("production")
	result, err := database.Ingest(context.Background(), domain.IngestCommand{
		DeliveryID: "app-recover", ReceivedAt: time.UnixMilli(1000),
		CanonicalEvent: domain.CanonicalEvent{Provider: "gitlab", Source: "test", Repository: "repository", Event: "pipeline"}, RoutingResult: domain.RoutingDeploy,
		Deliveries: []domain.NewDelivery{{TargetID: "production", TargetSnapshot: target.Snapshot}},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := database.ClaimPendingAttempt(context.Background(), time.UnixMilli(2000))
	if err != nil || !ok || claimed.EventID != result.EventID {
		t.Fatalf("ClaimPendingAttempt() = %#v/%v/%v", claimed, ok, err)
	}
	application := newTestApp(manager, database, slog.New(slog.NewTextHandler(io.Discard, nil)))
	application.PublicAddr = "127.0.0.1:0"
	application.AdminAddr = "127.0.0.1:0"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	select {
	case <-queried:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("recovered attempt was not polled")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop recovered workers")
	}
	if postCalls.Load() != 0 {
		t.Fatalf("recovery POST calls = %d", postCalls.Load())
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var transport, deployment string
	if err := db.QueryRow(`SELECT transport_status, deployment_status FROM delivery_attempts WHERE id = ?`, claimed.AttemptID).Scan(&transport, &deployment); err != nil {
		t.Fatal(err)
	}
	if transport != "unknown" || deployment != "locating" {
		t.Fatalf("recovered state = %s/%s", transport, deployment)
	}
}

func appManager(t *testing.T, cfg *config.Bundle, durable *store.Store) *runtimecfg.Manager {
	t.Helper()
	bundle := *cfg
	bundle.Global.Kind = "Hookfly"
	generation, err := runtimecfg.Compile(&bundle, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return runtimecfg.NewManager(generation, durable, runtimecfg.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}
