package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	migrationfs "github.com/hoomoli/hookfly/migrations"
)

func TestOpenCreatesFreshV2Schema(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "hookfly.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var version int
	if err := store.db.QueryRowContext(ctx, "SELECT schema_version FROM hookfly_metadata").Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 2 {
		t.Fatalf("schema version = %d, want 2", version)
	}
	rows, err := store.db.QueryContext(ctx, "SELECT name FROM pragma_table_info('events') ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"commit_message", "delivery_id", "event", "external_id", "forward_request_json", "provider", "raw_headers_json", "raw_payload_json", "ref", "repository_id", "revision", "source_id", "status", "trigger"}
	for _, column := range want {
		index := sort.SearchStrings(columns, column)
		if index == len(columns) || columns[index] != column {
			t.Fatalf("events is missing canonical column %q", column)
		}
	}
	for _, column := range []string{"project_id", "project_name", "pipeline_id", "pipeline_source"} {
		index := sort.SearchStrings(columns, column)
		if index < len(columns) && columns[index] == column {
			t.Fatalf("events retains legacy column %q", column)
		}
	}
	for table, column := range map[string]string{
		"deliveries":        "dispatch_key",
		"delivery_attempts": "deployment_cursor",
	} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s is missing %s", table, column)
		}
	}
	for _, indexName := range []string{"events_pipeline_correlation_idx", "events_push_pipeline_correlation_idx"} {
		var count int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name=?", indexName).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s count = %d", indexName, count)
		}
	}
}

func TestListEventsUsesPushPipelineCorrelationIndex(t *testing.T) {
	store := openTestStore(t)
	where, args := eventWhere(EventQuery{})
	rows, err := store.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN SELECT e.id FROM events e "+where, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "events_push_pipeline_correlation_idx") {
		t.Fatalf("push pipeline correlation index is unused:\n%s", plan.String())
	}
}

func TestOpenAppliesCurrentMigrationsToExistingV2Database(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hookfly.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := migrationfs.FS.ReadFile("001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(initial)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE schema_migrations (name TEXT PRIMARY KEY);
		INSERT INTO schema_migrations (name) VALUES ('001_initial.sql');
		INSERT INTO events (
			id, provider, source_id, repository_id, delivery_id, received_at, event, routing_result
		) VALUES ('existing-event', 'gitlab', 'source', 'repository', 'delivery', 1, 'pipeline', 'ignored');
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for table, column := range map[string]string{
		"deliveries":        "dispatch_key",
		"delivery_attempts": "deployment_cursor",
	} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s is missing %s after upgrade", table, column)
		}
	}
	var existingEvents, migrationRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE id = 'existing-event'`).Scan(&existingEvents); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE name IN ('002_deployment_correlation.sql', '002_event_commit_message.sql', '003_pipeline_correlation_index.sql', '004_push_pipeline_correlation_index.sql', '005_forward_request.sql')`).Scan(&migrationRows); err != nil {
		t.Fatal(err)
	}
	if existingEvents != 1 || migrationRows != 5 {
		t.Fatalf("existing events/migration rows = %d/%d", existingEvents, migrationRows)
	}
}

func TestOpenRejectsLegacySchemaWithoutMutatingIt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hookfly.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE events (id TEXT PRIMARY KEY, project_id INTEGER, payload_json BLOB);
		CREATE TABLE schema_migrations (name TEXT PRIMARY KEY);
		INSERT INTO events (id, project_id, payload_json) VALUES ('sentinel', 7, X'01');
	`); err != nil {
		t.Fatalf("create legacy database: %v", err)
	}
	before := sqliteSchema(t, ctx, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(ctx, path); !errors.Is(err, ErrIncompatibleSchema) {
		t.Fatalf("Open(v1) error = %v", err)
	}

	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var projectID int
	var payload []byte
	if err := db.QueryRowContext(ctx, "SELECT project_id, payload_json FROM events WHERE id = 'sentinel'").Scan(&projectID, &payload); err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if projectID != 7 || string(payload) != string([]byte{1}) {
		t.Fatalf("sentinel = %d/%x", projectID, payload)
	}
	after := sqliteSchema(t, ctx, db)
	if len(after) != len(before) {
		t.Fatalf("schema changed: before=%v after=%v", before, after)
	}
	for index := range before {
		if before[index] != after[index] {
			t.Fatalf("schema changed: before=%v after=%v", before, after)
		}
	}
	var metadata int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE name = 'hookfly_metadata'").Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if metadata != 0 {
		t.Fatal("legacy database gained hookfly_metadata")
	}
}

func TestOpenRejectsLegacySchemaWithoutPersistentSQLiteChanges(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hookfly.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE events (project_id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeMode := journalMode(t, ctx, path)
	assertSQLiteSidecarsAbsent(t, path)

	if _, err := Open(ctx, path); !errors.Is(err, ErrIncompatibleSchema) {
		t.Fatalf("Open(v1) error = %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("legacy database bytes changed")
	}
	if afterMode := journalMode(t, ctx, path); afterMode != beforeMode {
		t.Fatalf("journal_mode = %q, want %q", afterMode, beforeMode)
	}
	assertSQLiteSidecarsAbsent(t, path)
}

func journalMode(t *testing.T, ctx context.Context, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	return mode
}

func assertSQLiteSidecarsAbsent(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected SQLite sidecar %s: %v", suffix, err)
		}
	}
}

func sqliteSchema(t *testing.T, ctx context.Context, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "SELECT type || ':' || name || ':' || coalesce(sql, '') FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var schema []string
	for rows.Next() {
		var statement string
		if err := rows.Scan(&statement); err != nil {
			t.Fatal(err)
		}
		schema = append(schema, statement)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return schema
}
