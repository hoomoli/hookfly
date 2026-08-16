package store

import (
	"context"
	"testing"
)

func TestPruneAppliesRepositoryThenGlobalLimitsAndCascades(t *testing.T) {
	// Break caught: applying global retention first or deleting active deliveries.
	s := openTestStore(t)
	seedRetentionEvents(t, s)
	result, err := s.Prune(context.Background(), map[RepositoryKey]int{{SourceID: "gitlab-a", RepositoryID: "app"}: 1}, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 2 || !sameStrings(result.EventIDs, []string{"p4-old", "p4-new"}) {
		t.Fatalf("Prune() = %#v", result)
	}
	assertEventExists(t, s, "p4-old", false)
	assertEventExists(t, s, "p4-new", false)
	assertEventExists(t, s, "p5-old", true)
	assertEventExists(t, s, "unknown-old", true)
	assertEventExists(t, s, "active-old", true)
	assertEventExists(t, s, "unrecognized-old", true)
	for table, want := range map[string]int{"deliveries": 2, "delivery_attempts": 2, "audit_logs": 0} {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s rows after cascade = %d, want %d", table, count, want)
		}
	}
}

func TestPruneTreatsNonPositiveLimitsAsUnlimited(t *testing.T) {
	// Break caught: default zero configuration unexpectedly deleting all terminal history.
	s := openTestStore(t)
	seedRetentionEvents(t, s)
	result, err := s.Prune(context.Background(), map[RepositoryKey]int{{SourceID: "gitlab-a", RepositoryID: "app"}: 0}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 0 {
		t.Fatalf("Prune() deleted %d events", result.Deleted)
	}
}

func TestPruneRemovesRepositoryExcessBeforeGlobalLimit(t *testing.T) {
	// Break caught: repository-excess rows consuming global keep slots and deleting an unrelated older repository event.
	s := openTestStore(t)
	for _, event := range []struct {
		id, source string
		received   int
	}{
		{"p4-new", "gitlab-a", 11}, {"p4-excess", "gitlab-a", 10}, {"p5-old", "gitlab-b", 1},
	} {
		if _, err := s.db.Exec(`INSERT INTO events (id, provider, source_id, repository_id, delivery_id, received_at, event, routing_result) VALUES (?, 'gitlab', ?, 'app', ?, ?, 'pipeline', 'deploy')`, event.id, event.source, event.id, event.received); err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.Prune(context.Background(), map[RepositoryKey]int{{SourceID: "gitlab-a", RepositoryID: "app"}: 1}, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(result.EventIDs, []string{"p4-excess"}) {
		t.Fatalf("Prune() deleted %#v, want only repository excess", result.EventIDs)
	}
	assertEventExists(t, s, "p4-new", true)
	assertEventExists(t, s, "p4-excess", false)
	assertEventExists(t, s, "p5-old", true)
}

func TestPruneExcludesDeferredEventsFromRepositoryAndGlobalPasses(t *testing.T) {
	s := openTestStore(t)
	for _, event := range []struct {
		id      string
		routing string
		at      int
	}{
		{"deferred", "deferred", 1},
		{"terminal-new", "unmatched", 3},
		{"terminal-old", "unmatched", 2},
	} {
		if _, err := s.db.Exec(`INSERT INTO events (id, provider, source_id, repository_id, delivery_id, received_at, event, routing_result) VALUES (?, 'gitlab', 'gitlab-a', 'app', ?, ?, 'pipeline', ?)`, event.id, event.id, event.at, event.routing); err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.Prune(context.Background(), map[RepositoryKey]int{{SourceID: "gitlab-a", RepositoryID: "app"}: 1}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(result.EventIDs, []string{"terminal-old"}) {
		t.Fatalf("deleted = %#v", result.EventIDs)
	}
	assertEventExists(t, s, "deferred", true)
}

func TestPruneScopesSameRepositoryIDBySourceAndUsesDefaultForUnconfiguredHistory(t *testing.T) {
	s := openTestStore(t)
	for _, event := range []struct {
		id, provider, source string
		received             int
	}{
		{"a-old", "gitlab", "gitlab-a", 1},
		{"a-new", "gitlab", "gitlab-a", 2},
		{"b-old", "gitlab", "gitlab-b", 3},
		{"b-middle", "gitlab", "gitlab-b", 4},
		{"b-new", "gitlab", "gitlab-b", 5},
	} {
		if _, err := s.db.Exec(`INSERT INTO events (id, provider, source_id, repository_id, delivery_id, received_at, event, routing_result) VALUES (?, ?, ?, 'app', ?, ?, 'pipeline', 'unmatched')`, event.id, event.provider, event.source, event.id, event.received); err != nil {
			t.Fatal(err)
		}
	}

	result, err := s.Prune(context.Background(), map[RepositoryKey]int{{SourceID: "gitlab-a", RepositoryID: "app"}: 1}, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(result.EventIDs, []string{"a-old", "b-old"}) {
		t.Fatalf("Prune() deleted %#v", result.EventIDs)
	}
	for _, event := range []struct {
		id   string
		want bool
	}{
		{"a-old", false}, {"a-new", true}, {"b-old", false}, {"b-middle", true}, {"b-new", true},
	} {
		assertEventExists(t, s, event.id, event.want)
	}
}

func TestPruneCombinesMixedProviderEvidenceIntoOneRepositoryBudgetBeforeGlobalLimit(t *testing.T) {
	s := openTestStore(t)
	for _, event := range []struct {
		id, provider, source string
		received             int
	}{
		{"shared-new", "gitlab", "shared", 10},
		{"shared-middle", "github", "shared", 9},
		{"shared-old", "gitlab", "shared", 8},
		{"other-new", "gitlab", "other", 7},
		{"other-old", "gitlab", "other", 6},
	} {
		if _, err := s.db.Exec(`INSERT INTO events (id, provider, source_id, repository_id, delivery_id, received_at, event, routing_result) VALUES (?, ?, ?, 'app', ?, ?, 'push', 'unmatched')`, event.id, event.provider, event.source, event.id, event.received); err != nil {
			t.Fatal(err)
		}
	}

	result, err := s.Prune(context.Background(), map[RepositoryKey]int{{SourceID: "shared", RepositoryID: "app"}: 2}, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(result.EventIDs, []string{"shared-old", "other-old"}) {
		t.Fatalf("Prune() deleted %#v", result.EventIDs)
	}
	for _, event := range []struct {
		id   string
		want bool
	}{
		{"shared-new", true}, {"shared-middle", true}, {"shared-old", false},
		{"other-new", true}, {"other-old", false},
	} {
		assertEventExists(t, s, event.id, event.want)
	}
}

func seedRetentionEvents(t *testing.T, s *Store) {
	t.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	for _, event := range []struct {
		id, source, repository string
		received               int
	}{
		{"p4-old", "gitlab-a", "app", 1}, {"p4-new", "gitlab-a", "app", 2},
		{"p5-old", "gitlab-b", "app", 3}, {"unknown-old", "gitlab-c", "unknown", 4},
		{"active-old", "gitlab-d", "app", 5}, {"unrecognized-old", "gitlab-e", "app", 6},
	} {
		if _, err := tx.Exec(`INSERT INTO events (id, provider, source_id, repository_id, delivery_id, received_at, event, routing_result) VALUES (?, 'gitlab', ?, ?, ?, ?, 'pipeline', 'deploy')`, event.id, event.source, event.repository, event.id, event.received); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO deliveries (id, event_id, target_id, current_attempt_id, transport_status, deployment_status, created_at, updated_at) VALUES ('p4-delivery', 'p4-old', 'target', 'p4-attempt', 'failed', 'not_started', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO delivery_attempts (id, delivery_id, kind, transport_status, deployment_status, created_at) VALUES ('p4-attempt', 'p4-delivery', 'initial', 'failed', 'not_started', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO audit_logs (id, event_id, delivery_id, attempt_id, action, created_at) VALUES ('p4-audit', 'p4-old', 'p4-delivery', 'p4-attempt', 'seed', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO deliveries (id, event_id, target_id, current_attempt_id, transport_status, deployment_status, created_at, updated_at) VALUES ('active-delivery', 'active-old', 'target', 'active-attempt', 'pending', 'not_started', 5, 5)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO delivery_attempts (id, delivery_id, kind, transport_status, deployment_status, created_at) VALUES ('active-attempt', 'active-delivery', 'initial', 'pending', 'not_started', 5)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO deliveries (id, event_id, target_id, current_attempt_id, transport_status, deployment_status, created_at, updated_at) VALUES ('unrecognized-delivery', 'unrecognized-old', 'target', 'unrecognized-attempt', 'enqueued', 'unrecognized', 6, 6)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO delivery_attempts (id, delivery_id, kind, transport_status, deployment_status, created_at) VALUES ('unrecognized-attempt', 'unrecognized-delivery', 'initial', 'enqueued', 'unrecognized', 6)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func assertEventExists(t *testing.T, s *Store, eventID string, want bool) {
	t.Helper()
	var exists bool
	if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM events WHERE id = ?)`, eventID).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != want {
		t.Fatalf("event %q exists = %v, want %v", eventID, exists, want)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
