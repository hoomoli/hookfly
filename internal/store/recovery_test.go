package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

func TestListAndTerminateIncompatibleActiveBindings(t *testing.T) {
	type connectionSnapshot struct {
		ID      string `json:"id"`
		BaseURL string `json:"base_url"`
	}
	type bindingSnapshot struct {
		BindingVersion int                `json:"binding_version"`
		ID             string             `json:"id"`
		Type           string             `json:"type"`
		ResourceType   string             `json:"resource_type"`
		ResourceID     string             `json:"resource_id"`
		Connection     connectionSnapshot `json:"connection"`
	}
	wantBinding := bindingSnapshot{
		BindingVersion: 2,
		ID:             "target",
		Type:           "dokploy",
		ResourceType:   "compose",
		ResourceID:     "old-compose",
		Connection: connectionSnapshot{
			ID:      "primary",
			BaseURL: "https://dokploy.example.invalid",
		},
	}
	tests := []struct {
		name                  string
		transport, deployment string
		wantTransport         string
		wantDeployment        string
	}{
		{"pending send", "pending", "not_started", "failed", "not_started"},
		{"remote state", "enqueued", "running", "unknown", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			snapshot := []byte(`{"binding_version":2,"id":"target","type":"dokploy","resource_type":"compose",` +
				`"resource_id":"old-compose","connection":{"id":"primary","base_url":"https://dokploy.example.invalid"}}`)
			seedActiveBinding(t, s, "event", "delivery", "attempt", tt.transport, tt.deployment, snapshot)
			bindings, err := s.ListActiveBindings(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(bindings) != 1 || string(bindings[0].TargetSnapshot) != string(snapshot) {
				t.Fatalf("bindings = %#v", bindings)
			}
			var binding bindingSnapshot
			if err := json.Unmarshal(bindings[0].TargetSnapshot, &binding); err != nil {
				t.Fatal(err)
			}
			if binding != wantBinding {
				t.Fatalf("binding = %#v, want %#v", binding, wantBinding)
			}
			if bindings[0].TargetID != binding.ID {
				t.Fatalf("target ID = %q, binding ID = %q", bindings[0].TargetID, binding.ID)
			}
			if err := s.TerminateIncompatibleActive(context.Background(), bindings[0], "target_changed", time.UnixMilli(50)); err != nil {
				t.Fatal(err)
			}
			var transport, deployment string
			var pollDue, deadline, lastPolled sql.NullInt64
			if err := s.db.QueryRow(`SELECT transport_status, deployment_status, poll_due_at, monitoring_deadline_at, last_polled_at FROM delivery_attempts WHERE id = 'attempt'`).Scan(&transport, &deployment, &pollDue, &deadline, &lastPolled); err != nil {
				t.Fatal(err)
			}
			if transport != tt.wantTransport || deployment != tt.wantDeployment {
				t.Fatalf("attempt = %s/%s", transport, deployment)
			}
			if pollDue.Valid || deadline.Valid || lastPolled.Valid {
				t.Fatalf("poll fields remain: %#v/%#v/%#v", pollDue, deadline, lastPolled)
			}
			var deliveryTransport, deliveryDeployment, action string
			if err := s.db.QueryRow(`SELECT transport_status, deployment_status FROM deliveries WHERE id = 'delivery'`).Scan(&deliveryTransport, &deliveryDeployment); err != nil {
				t.Fatal(err)
			}
			if deliveryTransport != transport || deliveryDeployment != deployment {
				t.Fatalf("delivery = %s/%s", deliveryTransport, deliveryDeployment)
			}
			if err := s.db.QueryRow(`SELECT action FROM audit_logs WHERE attempt_id = 'attempt'`).Scan(&action); err != nil {
				t.Fatal(err)
			}
			if action != "active_binding_target_changed" {
				t.Fatalf("action = %q", action)
			}
		})
	}
}

func seedActiveBinding(t *testing.T, s *Store, eventID, deliveryID, attemptID, transport, deployment string, snapshot []byte) {
	t.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO events (id, provider, source_id, repository_id, delivery_id, received_at, event, routing_result) VALUES (?, 'test', 'source', 'repository', ?, 1, 'pipeline', 'deploy')`, eventID, eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO deliveries (id, event_id, target_id, target_snapshot, current_attempt_id, transport_status, deployment_status, created_at, updated_at) VALUES (?, ?, 'target', ?, ?, ?, ?, 1, 1)`, deliveryID, eventID, snapshot, attemptID, transport, deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO delivery_attempts (id, delivery_id, kind, transport_status, deployment_status, poll_due_at, monitoring_deadline_at, last_polled_at, created_at) VALUES (?, ?, 'initial', ?, ?, 10, 20, 5, 1)`, attemptID, deliveryID, transport, deployment); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
