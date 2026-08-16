package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/config"
	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/runtimecfg"
	"github.com/hoomoli/hookfly/internal/store"
)

func TestRetentionRunOnceUsesStartupHistoryLimits(t *testing.T) {
	// Break caught: retention ignoring source-scoped repository/default limits or depending on an admin request to run.
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hookfly.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for index, event := range []struct{ source, key string }{
		{source: "gitlab-a", key: "a-old"}, {source: "gitlab-a", key: "a-new"},
		{source: "gitlab-b", key: "b-old"}, {source: "gitlab-b", key: "b-middle"}, {source: "gitlab-b", key: "b-new"},
	} {
		if _, err := database.Ingest(context.Background(), domain.IngestCommand{
			DeliveryID: event.key, ReceivedAt: time.UnixMilli(int64(index + 1)),
			CanonicalEvent: domain.CanonicalEvent{Provider: "gitlab", Source: event.source, Repository: "app", Event: "pipeline"}, RoutingResult: domain.RoutingRecordOnly,
		}); err != nil {
			t.Fatal(err)
		}
	}
	configuredLimit := 1
	generation, err := runtimecfg.Compile(&config.Bundle{
		Global: config.Global{Kind: "Hookfly", History: config.GlobalHistory{DefaultRepositoryLimit: 2}},
		GitLabSources: []config.GitLabSource{
			{ID: "gitlab-a", Token: "test-token-a", Repositories: []config.Repository{{ID: "app", Name: "Application", ExternalID: "1", HistoryLimit: &configuredLimit}}},
			{ID: "gitlab-b", Token: "test-token-b", Repositories: []config.Repository{{ID: "app", Name: "Application", ExternalID: "2"}}},
		},
	}, discardServiceLogger())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRetention(runtimecfg.NewManager(generation, database, runtimecfg.Options{Logger: discardServiceLogger()}), database)
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, err := database.ListEvents(context.Background(), store.EventQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]int)
	for _, item := range page.Items {
		got[item.SourceID]++
	}
	if got["gitlab-a"] != 1 || got["gitlab-b"] != 2 || page.Total != 3 {
		t.Fatalf("retained repositories = %#v, total %d", got, page.Total)
	}
}
