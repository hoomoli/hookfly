// Package store provides the SQLite durability boundary for Hookfly.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/hoomoli/hookfly/migrations"
	_ "modernc.org/sqlite"
)

// ErrIncompatibleSchema reports a database that cannot be safely opened as schema v2.
var ErrIncompatibleSchema = errors.New("incompatible database schema")

// Store owns the single-process SQLite connection pool.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens path, configures SQLite, and applies embedded migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := classifySchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := configure(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, now: time.Now}, nil
}

func configure(ctx context.Context, db *sql.DB) error {
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite: %w", err)
		}
	}
	return nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migrations: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY)"); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "embed.go" {
			continue
		}
		var applied bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name = ?)", entry.Name()).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		if applied {
			continue
		}
		sqlText, err := migrations.FS.ReadFile(entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, string(sqlText)); err != nil {
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (name) VALUES (?)", entry.Name()); err != nil {
			return fmt.Errorf("record migration %s: %w", entry.Name(), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

// classifySchema runs before migration bookkeeping so rejected databases remain untouched.
func classifySchema(ctx context.Context, db *sql.DB) error {
	var objects int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM sqlite_schema
		WHERE name NOT LIKE 'sqlite_%'`).Scan(&objects); err != nil {
		return fmt.Errorf("inspect database schema: %w", err)
	}
	if objects == 0 {
		return nil
	}

	var metadataExists bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = 'hookfly_metadata')").Scan(&metadataExists); err != nil {
		return fmt.Errorf("inspect database schema: %w", err)
	}
	if !metadataExists {
		return incompatibleSchemaError()
	}

	rows, err := db.QueryContext(ctx, "SELECT schema_version, typeof(schema_version) FROM hookfly_metadata")
	if err != nil {
		return incompatibleSchemaError()
	}
	defer rows.Close()
	versions := 0
	for rows.Next() {
		var version int
		var kind string
		if err := rows.Scan(&version, &kind); err != nil {
			return incompatibleSchemaError()
		}
		if version != 2 || kind != "integer" {
			return incompatibleSchemaError()
		}
		versions++
	}
	if err := rows.Err(); err != nil || versions != 1 {
		return incompatibleSchemaError()
	}
	return nil
}

func incompatibleSchemaError() error {
	return fmt.Errorf("%w: database schema is incompatible; back up and remove the database before restarting", ErrIncompatibleSchema)
}

// Close releases the SQLite database.
func (s *Store) Close() error {
	return s.db.Close()
}
