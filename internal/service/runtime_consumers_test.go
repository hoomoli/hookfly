package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/config"
	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/runtimecfg"
	"github.com/hoomoli/hookfly/internal/store"
)

func TestManagedManualAttemptPrioritizesReloadAndBindingErrorsWithoutExternalCall(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	s, db, deliveryID, attemptID := seedAttempt(t, server.URL, "failed", "not_started")
	changedSnapshot := `{"binding_version":2,"id":"production","type":"dokploy","resource_type":"compose","resource_id":"old-compose","connection":{"id":"dokploy","base_url":"` + server.URL + `"}}`
	if _, err := db.Exec(`UPDATE deliveries SET target_snapshot = ? WHERE id = ?`, []byte(changedSnapshot), deliveryID); err != nil {
		t.Fatal(err)
	}
	generation, err := compileServiceConfig(attemptConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	manager := runtimecfg.NewManager(generation, s, runtimecfg.Options{Logger: discardServiceLogger()})
	attempts := NewAttempts(manager, s, time.Now)

	manager.SetDraining()
	if _, err := attempts.Execute(context.Background(), deliveryID, attemptID, domain.OperationRetry, "alice", "blocked"); !errors.Is(err, ErrConfigReloadInProgress) {
		t.Fatalf("non-idle Execute() error = %v", err)
	}
	if idle, err := manager.SetIdleIfQueueEmpty(context.Background()); err != nil || !idle {
		t.Fatalf("SetIdleIfQueueEmpty() = %v, %v", idle, err)
	}
	if _, err := attempts.Execute(context.Background(), deliveryID, attemptID, domain.OperationRetry, "alice", "changed"); !errors.Is(err, ErrTargetChanged) {
		t.Fatalf("changed Execute() error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("external calls = %d", calls.Load())
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM delivery_attempts WHERE delivery_id = ?`, deliveryID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("attempt rows = %d", count)
	}
}

func compileServiceConfig(cfg *config.Bundle) (*runtimecfg.Generation, error) {
	bundle := *cfg
	bundle.Global.Kind = "Hookfly"
	return runtimecfg.Compile(&bundle, discardServiceLogger())
}

func serviceManager(t *testing.T, cfg *config.Bundle, durableStore *store.Store) *runtimecfg.Manager {
	t.Helper()
	generation, err := compileServiceConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return runtimecfg.NewManager(generation, durableStore, runtimecfg.Options{Logger: discardServiceLogger()})
}

func TestManagedRetentionUsesNewGenerationLimitsImmediately(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "hookfly.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer durable.Close()
	for index, key := range []string{"old", "middle", "new"} {
		if _, err := durable.Ingest(ctx, domain.IngestCommand{
			DeliveryID: key, ReceivedAt: time.UnixMilli(int64(index + 1)),
			CanonicalEvent: domain.CanonicalEvent{Provider: "gitlab", Source: "test", Repository: "repository", Event: "pipeline"}, RoutingResult: domain.RoutingRecordOnly,
		}); err != nil {
			t.Fatal(err)
		}
	}
	initial, err := compileServiceConfig(&config.Bundle{Global: config.Global{History: config.GlobalHistory{DefaultRepositoryLimit: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hookfly.yaml")
	if err := os.WriteFile(path, []byte("kind: Hookfly\nhistory:\n  default_repository_limit: 1\npolling: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := runtimecfg.NewManager(initial, durable, runtimecfg.Options{Path: path, Logger: discardServiceLogger()})
	runner := NewRetention(manager, durable)
	if outcome, err := manager.Reload(ctx, "operator"); err != nil || outcome != runtimecfg.ReloadApplied {
		t.Fatalf("Reload() = %q, %v", outcome, err)
	}
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := durable.ListEvents(ctx, store.EventQuery{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID == "" {
		t.Fatalf("page = %#v", page)
	}
}

func discardServiceLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
