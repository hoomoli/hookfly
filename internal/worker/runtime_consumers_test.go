package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/config"
	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/runtimecfg"
	"github.com/hoomoli/hookfly/internal/store"
)

func TestManagedDispatcherRejectsChangedHistoricalBindingWithoutExternalCall(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	s, db := openWorkerStore(t)
	cfg := dispatcherConfig(server.URL, nil)
	manager := managedWorkerManager(t, cfg, s)
	changedSnapshot := []byte(`{"binding_version":2,"id":"production","type":"dokploy","resource_type":"compose","resource_id":"old-compose","connection":{"id":"dokploy","base_url":"` + server.URL + `"}}`)
	eventID := ingestWorkerEvent(t, s, "changed-binding", time.UnixMilli(1), []domain.NewDelivery{{TargetID: "production", TargetSnapshot: changedSnapshot}})
	attemptID := attemptForEvent(t, db, eventID)

	dispatcher := NewDispatcher(manager, s, discardLogger(), func() time.Time { return time.UnixMilli(2) })
	processed, err := dispatcher.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce() = %v, %v", processed, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("external calls = %d", calls.Load())
	}
	assertWorkerAttemptState(t, db, attemptID, "failed", "not_started")
}

func TestManagedPollerRejectsChangedHistoricalBindingWithoutExternalCall(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	s, db := openWorkerStore(t)
	cfg := pollerConfig(server.URL, time.Second, time.Minute)
	manager := managedWorkerManager(t, cfg, s)
	changedSnapshot := []byte(`{"binding_version":2,"id":"production","type":"dokploy","resource_type":"compose","resource_id":"old-compose","connection":{"id":"dokploy","base_url":"` + server.URL + `"}}`)
	eventID := ingestWorkerEvent(t, s, "poll-changed-binding", time.UnixMilli(1), []domain.NewDelivery{{TargetID: "production", TargetSnapshot: changedSnapshot}})
	attemptID := attemptForEvent(t, db, eventID)
	work, claimed, err := s.ClaimPendingAttempt(context.Background(), time.UnixMilli(2))
	if err != nil || !claimed || work.AttemptID != attemptID {
		t.Fatalf("ClaimPendingAttempt() = %#v, %v, %v", work, claimed, err)
	}
	if err := s.MarkEnqueued(context.Background(), attemptID, []byte(`{}`), []byte(`{}`), time.UnixMilli(3), time.UnixMilli(4), time.UnixMilli(5)); err != nil {
		t.Fatal(err)
	}
	poller := NewPoller(manager, s, discardLogger())
	if err := poller.RunOnce(context.Background(), time.UnixMilli(6)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("external calls = %d", calls.Load())
	}
	assertWorkerAttemptState(t, db, attemptID, "enqueued", "unknown")
}

func managedWorkerManager(t *testing.T, cfg *config.Bundle, durableStore *store.Store) *runtimecfg.Manager {
	t.Helper()
	bundle := *cfg
	bundle.Global.Kind = "Hookfly"
	generation, err := runtimecfg.Compile(&bundle, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	return runtimecfg.NewManager(generation, durableStore, runtimecfg.Options{Logger: discardLogger()})
}

func workerManager(t *testing.T, cfg *config.Bundle, durableStore *store.Store) *runtimecfg.Manager {
	t.Helper()
	return managedWorkerManager(t, cfg, durableStore)
}

func workerDeliveries(t *testing.T, cfg *config.Bundle) []domain.NewDelivery {
	t.Helper()
	manager := managedWorkerManager(t, cfg, nil)
	target, found := manager.Current().Target("production")
	if !found {
		t.Fatal("production target is missing")
	}
	return []domain.NewDelivery{{TargetID: target.ID, TargetSnapshot: target.Snapshot}}
}
