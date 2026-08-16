package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/domain"
)

func TestClearHistoryDeletesAllEventHistoryAndKeepsSchemaUsable(t *testing.T) {
	s := openTestStore(t)
	beforeMigrations := migrationNames(t, s)
	beforeSchema := schemaDefinitions(t, s)
	eventID := queryIngest(t, s, "old", time.UnixMilli(1_000), []domain.NewDelivery{{TargetID: "production"}})
	querySetState(t, s, eventID, "production", "enqueued", "done")

	var deliveryID, attemptID string
	if err := s.db.QueryRow("SELECT id, current_attempt_id FROM deliveries WHERE event_id = ?", eventID).Scan(&deliveryID, &attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO audit_logs (id, event_id, delivery_id, attempt_id, action, created_at)
		VALUES (?, ?, ?, ?, 'deployment_done', ?)`, "audit-old", eventID, deliveryID, attemptID, 1_000); err != nil {
		t.Fatal(err)
	}

	result, err := s.ClearHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 {
		t.Fatalf("deleted = %d, want 1", result.Deleted)
	}
	for _, table := range []string{"events", "deliveries", "delivery_attempts", "audit_logs", "routing_queue"} {
		assertRowCount(t, s, table, 0)
	}
	assertRowCount(t, s, "hookfly_metadata", 1)
	if got := migrationNames(t, s); !reflect.DeepEqual(got, beforeMigrations) {
		t.Fatalf("migration names after clear = %#v, want %#v", got, beforeMigrations)
	}
	if got := schemaDefinitions(t, s); !reflect.DeepEqual(got, beforeSchema) {
		t.Fatalf("schema definitions after clear = %#v, want %#v", got, beforeSchema)
	}

	newEventID := queryIngest(t, s, "new", time.UnixMilli(2_000), nil)
	page, err := s.ListEvents(context.Background(), EventQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != newEventID {
		t.Fatalf("events after clear = %#v", page)
	}
}

func TestClearHistoryKeepsExistingRowsWhenWorkIsActive(t *testing.T) {
	tests := []struct {
		name string
		seed func(*testing.T, *Store)
	}{
		{
			name: "active delivery",
			seed: func(t *testing.T, s *Store) {
				queryIngest(t, s, "active", time.UnixMilli(1_000), []domain.NewDelivery{{TargetID: "production"}})
			},
		},
		{
			name: "queued deferred event",
			seed: func(t *testing.T, s *Store) {
				if _, err := s.IngestDeferred(context.Background(), deferredCommand("queued", time.UnixMilli(1_000))); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := openTestStore(t)
			test.seed(t, s)

			_, err := s.ClearHistory(context.Background())
			if !errors.Is(err, ErrHistoryActive) {
				t.Fatalf("ClearHistory() error = %v, want %v", err, ErrHistoryActive)
			}
			assertRowCount(t, s, "events", 1)
		})
	}
}

func migrationNames(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.Query("SELECT name FROM schema_migrations ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}

type schemaDefinition struct {
	Type string
	Name string
	SQL  string
}

func schemaDefinitions(t *testing.T, s *Store) []schemaDefinition {
	t.Helper()
	rows, err := s.db.Query(`
		SELECT type, name, sql
		FROM sqlite_schema
		WHERE type IN ('table', 'index') AND name NOT LIKE 'sqlite_%'
		ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var definitions []schemaDefinition
	for rows.Next() {
		var definition schemaDefinition
		if err := rows.Scan(&definition.Type, &definition.Name, &definition.SQL); err != nil {
			t.Fatal(err)
		}
		definitions = append(definitions, definition)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return definitions
}
