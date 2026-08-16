package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/config"
	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/runtimecfg"
	"github.com/hoomoli/hookfly/internal/store"
)

func TestPrepareTerminatesIncompatibleActiveBindingBeforeAnyWorkerOrListener(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hookfly.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer durable.Close()
	generation := appTargetGeneration(t, server.URL)
	changedSnapshot := []byte(`{"binding_version":2,"id":"production","type":"dokploy","resource_type":"compose","resource_id":"old-compose","connection":{"id":"primary","base_url":"` + server.URL + `"}}`)
	result, err := durable.Ingest(context.Background(), domain.IngestCommand{
		DeliveryID: "active", ReceivedAt: time.UnixMilli(1), RoutingResult: domain.RoutingDeploy,
		CanonicalEvent: domain.CanonicalEvent{Provider: "gitlab", Source: "test", Repository: "repository", Event: "pipeline"},
		Deliveries:     []domain.NewDelivery{{TargetID: "production", TargetSnapshot: changedSnapshot}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := runtimecfg.NewManager(generation, durable, runtimecfg.Options{Logger: discardAppLogger()})
	application := newTestApp(manager, durable, discardAppLogger())
	if err := application.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("external calls = %d", calls.Load())
	}
	active, err := durable.CountActive(context.Background())
	if err != nil || active != 0 {
		t.Fatalf("active = %d, %v", active, err)
	}
	detail, err := durable.GetEventDetail(context.Background(), result.EventID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Deliveries) != 1 || detail.Deliveries[0].TransportStatus != "failed" || detail.Deliveries[0].DeploymentStatus != "not_started" {
		t.Fatalf("delivery = %#v", detail.Deliveries)
	}
}

func TestRunDrainsStartupBacklogAfterPrepareWithoutBrowserRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "hookfly.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer durable.Close()
	if _, err := durable.IngestDeferred(ctx, domain.IngestCommand{
		DeliveryID: "queued", ReceivedAt: time.UnixMilli(1), RoutingResult: domain.RoutingDeferred,
		CanonicalEvent: domain.CanonicalEvent{Provider: "gitlab", Source: "source", Repository: "repository", Event: "pipeline"},
	}); err != nil {
		t.Fatal(err)
	}
	generation, err := runtimecfg.Compile(&config.Bundle{Global: config.Global{Kind: "Hookfly"}}, discardAppLogger())
	if err != nil {
		t.Fatal(err)
	}
	manager := runtimecfg.NewManager(generation, durable, runtimecfg.Options{Logger: discardAppLogger()})
	application := newTestApp(manager, durable, discardAppLogger())
	application.PublicAddr = "127.0.0.1:0"
	application.AdminAddr = "127.0.0.1:0"
	if err := application.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(ctx)
	if err != nil || status.State != runtimecfg.StateDraining {
		t.Fatalf("prepared status = %#v, %v", status, err)
	}
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := manager.Status(ctx)
		if statusErr == nil && status.State == runtimecfg.StateIdle && status.QueuedEvents == 0 {
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("startup backlog did not drain")
}

func appTargetGeneration(t *testing.T, baseURL string) *runtimecfg.Generation {
	t.Helper()
	generation, err := runtimecfg.Compile(&config.Bundle{
		Global:             config.Global{Kind: "Hookfly"},
		DokployConnections: []config.DokployConnection{{ID: "primary", BaseURL: baseURL, APIKey: "fake-key"}},
		Targets:            []config.Target{{ID: "production", Type: "dokploy", Connection: "primary", ResourceType: "compose", ResourceID: "current-compose"}},
	}, discardAppLogger())
	if err != nil {
		t.Fatal(err)
	}
	return generation
}

func discardAppLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
