// Package db opens the SQLite database and applies migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Open opens (creating if needed) the database at path with the pragmas this
// application relies on: WAL for concurrent readers, foreign keys enforced,
// a busy timeout so writers queue instead of failing, and transactions that take
// the write lock when they begin, so a read-then-write never fails to upgrade.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_txlock=immediate", path)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite serialises writers; one connection avoids lock contention and
	// keeps transactions simple. Reads are cheap enough not to need a pool yet.
	d.SetMaxOpenConns(1)
	if err := d.Ping(); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// Migrate applies every migration in migrations/ that has not yet been
// applied, in filename order, each in its own transaction.
func Migrate(ctx context.Context, d *sql.DB) error {
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
	)`); err != nil {
		return err
	}
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		var n int
		if err := d.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE name = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, err := migrationFS.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (name) VALUES (?)`, name); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Applied returns the names of applied migrations, for diagnostics.
func Applied(ctx context.Context, d *sql.DB) ([]string, error) {
	rows, err := d.QueryContext(ctx, `SELECT name FROM schema_migrations ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, strings.TrimPrefix(s, "migrations/"))
	}
	return out, rows.Err()
}
