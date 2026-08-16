package worker

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/config"
	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/runtimecfg"
	"github.com/hoomoli/hookfly/internal/store"
)

func TestDrainerFinalizesStartupBacklogWithCurrentGenerationWithoutExternalCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "hookfly.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer durable.Close()
	if _, err := durable.IngestDeferred(ctx, domain.IngestCommand{
		DeliveryID: "queued", ReceivedAt: time.UnixMilli(10), RoutingResult: domain.RoutingDeferred,
		CanonicalEvent: domain.CanonicalEvent{Provider: "gitlab", Source: "gitlab-primary", Repository: "application", Event: "pipeline", Ref: "main", Status: "success"},
	}); err != nil {
		t.Fatal(err)
	}
	priority := 1
	generation, err := runtimecfg.Compile(&config.Bundle{
		Global:             config.Global{Kind: "Hookfly"},
		GitLabSources:      []config.GitLabSource{{ID: "gitlab-primary", Token: "test-token", Repositories: []config.Repository{{ID: "application", Name: "Application", ExternalID: "7"}}}},
		DokployConnections: []config.DokployConnection{{ID: "primary", BaseURL: "https://dokploy.example.invalid", APIKey: "fake-key"}},
		Targets:            []config.Target{{ID: "production", Type: "dokploy", Connection: "primary", ResourceType: "compose", ResourceID: "fake-compose"}},
		Routes:             []config.Route{{ID: "deploy-main", Priority: &priority, Match: config.RouteMatch{Source: "gitlab-primary", Repository: "application", Event: "pipeline"}, Action: config.Action{Type: "deploy", Targets: []string{"production"}}}},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	manager := runtimecfg.NewManager(generation, durable, runtimecfg.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := manager.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	drainer := NewDrainer(manager, durable, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go func() { _ = drainer.Run(ctx) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := manager.Status(ctx)
		if statusErr == nil && status.State == runtimecfg.StateIdle && status.QueuedEvents == 0 {
			page, listErr := durable.ListEvents(ctx, store.EventQuery{Page: 1, PageSize: 10})
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(page.Items) != 1 || page.Items[0].RoutingResult != "deploy" || page.Items[0].DeliverySummary.Count != 1 {
				t.Fatalf("page = %#v", page)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("startup backlog was not drained to idle")
}
