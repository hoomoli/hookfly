package worker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDispatcherClaimsOnlyCurrentApprovedAttemptKinds(t *testing.T) {
	// Break caught: dispatching a historical attempt or an unknown kind instead of the current initial/retry/redeploy work item.
	for _, kind := range []string{"initial", "retry", "redeploy"} {
		t.Run("current "+kind, func(t *testing.T) {
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				posts++
				_, _ = io.WriteString(w, `true`)
			}))
			defer server.Close()
			s, db := openWorkerStore(t)
			cfg := dispatcherConfig(server.URL, nil)
			eventID := ingestWorkerEvent(t, s, "approved-"+kind, time.UnixMilli(1), workerDeliveries(t, cfg))
			attemptID := attemptForEvent(t, db, eventID)
			if _, err := db.Exec(`UPDATE delivery_attempts SET kind = ? WHERE id = ?`, kind, attemptID); err != nil {
				t.Fatal(err)
			}
			dispatcher := NewDispatcher(workerManager(t, cfg, s), s, discardLogger(), func() time.Time { return time.UnixMilli(2) })
			worked, err := dispatcher.RunOnce(context.Background())
			if err != nil || !worked || posts != 1 {
				t.Fatalf("RunOnce() = %v/%v, posts = %d", worked, err, posts)
			}
		})
	}

	t.Run("old approved and current unknown", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			t.Fatal("dispatcher posted an unknown current attempt")
		}))
		defer server.Close()
		s, db := openWorkerStore(t)
		cfg := dispatcherConfig(server.URL, nil)
		eventID := ingestWorkerEvent(t, s, "unknown-current", time.UnixMilli(1), workerDeliveries(t, cfg))
		var deliveryID, oldAttemptID string
		if err := db.QueryRow(`SELECT id, current_attempt_id FROM deliveries WHERE event_id = ?`, eventID).Scan(&deliveryID, &oldAttemptID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO delivery_attempts (id, delivery_id, kind, transport_status, deployment_status, created_at) VALUES ('unknown-current', ?, 'manual', 'pending', 'not_started', 2)`, deliveryID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE deliveries SET current_attempt_id = 'unknown-current', transport_status = 'pending', deployment_status = 'not_started' WHERE id = ?`, deliveryID); err != nil {
			t.Fatal(err)
		}
		dispatcher := NewDispatcher(workerManager(t, cfg, s), s, discardLogger(), func() time.Time { return time.UnixMilli(2) })
		worked, err := dispatcher.RunOnce(context.Background())
		if err != nil || worked {
			t.Fatalf("RunOnce() = %v/%v", worked, err)
		}
		var oldKind string
		if err := db.QueryRow(`SELECT kind FROM delivery_attempts WHERE id = ?`, oldAttemptID).Scan(&oldKind); err != nil {
			t.Fatal(err)
		}
		if oldKind != "initial" {
			t.Fatalf("old attempt kind = %q", oldKind)
		}
	})
}
