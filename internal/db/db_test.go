package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenAndMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := Migrate(ctx, d); err != nil {
			t.Fatalf("migrate run %d: %v", i+1, err)
		}
	}
	applied, err := Applied(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) == 0 || applied[0] != "0001_init.sql" {
		t.Fatalf("applied = %v, want 0001_init.sql first", applied)
	}

	var mode string
	if err := d.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

// migrated opens a fresh, fully migrated database for a test.
func migrated(t *testing.T) *sql.DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return d
}

// hash32 stands in for a SHA-256 token hash; n keeps each one unique.
func hash32(n byte) []byte {
	h := make([]byte, 32)
	h[0] = n
	return h
}

// seedSheet inserts a by_date sheet with one section, slot, occurrence, person and
// confirmed claim, and returns the sheet id.
func seedSheet(t *testing.T, d *sql.DB) int64 {
	t.Helper()
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO sheets (id, slug, admin_token_hash, title, time_zone, format)
		  VALUES (1, 'abcdefghij', ?, 'Class party helpers', 'America/New_York', 'by_date')`, []any{hash32(1)}},
		{`INSERT INTO sections (id, sheet_id, position, date) VALUES (1, 1, 0, '2026-10-30')`, nil},
		{`INSERT INTO slots (id, section_id, position, title, quantity_wanted) VALUES (1, 1, 0, 'Crafts table', 3)`, nil},
		{`INSERT INTO occurrences (id, slot_id, date, start_time, end_time) VALUES (1, 1, '2026-10-30', '13:00', '14:30')`, nil},
		{`INSERT INTO people (id, first_name, last_name, phone) VALUES (1, 'Jenny', 'R', '+12165550142')`, nil},
		{`INSERT INTO claims (id, occurrence_id, person_id, status, manage_token_hash, manage_expires_at)
		  VALUES (1, 1, 1, 'confirmed', ?, '2026-11-29T00:00:00Z')`, []any{hash32(2)}},
	}
	for _, s := range stmts {
		if _, err := d.Exec(s.q, s.args...); err != nil {
			t.Fatalf("seed %q: %v", s.q, err)
		}
	}
	return 1
}

func TestSheetsMigrationApplies(t *testing.T) {
	d := migrated(t)
	var v string
	if err := d.QueryRow(`SELECT value FROM meta WHERE key = 'schema'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "2" {
		t.Fatalf("meta schema = %q, want 2", v)
	}
	seedSheet(t, d)
}

func TestSheetsSchemaRejectsBadRows(t *testing.T) {
	cases := []struct {
		name string
		q    string
		args []any
	}{
		{"unknown format", `INSERT INTO sheets (slug, admin_token_hash, title, time_zone, format)
			VALUES ('zzzzzzzzzz', ?, 'T', 'UTC', 'rsvp')`, []any{hash32(9)}},
		{"short slug", `INSERT INTO sheets (slug, admin_token_hash, title, time_zone, format)
			VALUES ('short', ?, 'T', 'UTC', 'slots_only')`, []any{hash32(9)}},
		{"duplicate slug", `INSERT INTO sheets (slug, admin_token_hash, title, time_zone, format)
			VALUES ('abcdefghij', ?, 'T', 'UTC', 'slots_only')`, []any{hash32(9)}},
		{"blank title", `INSERT INTO sheets (slug, admin_token_hash, title, time_zone, format)
			VALUES ('zzzzzzzzzz', ?, '  ', 'UTC', 'slots_only')`, []any{hash32(9)}},
		{"token hash not 32 bytes", `INSERT INTO sheets (slug, admin_token_hash, title, time_zone, format)
			VALUES ('zzzzzzzzzz', x'00', 'T', 'UTC', 'slots_only')`, nil},
		{"malformed date", `INSERT INTO sections (sheet_id, position, date) VALUES (1, 5, '30/10/2026')`, nil},
		{"zero quantity", `INSERT INTO slots (section_id, position, title, quantity_wanted) VALUES (1, 5, 'X', 0)`, nil},
		{"end before start", `INSERT INTO occurrences (slot_id, start_time, end_time) VALUES (1, '14:00', '13:00')`, nil},
		{"end without start", `INSERT INTO occurrences (slot_id, end_time) VALUES (1, '13:00')`, nil},
		{"person without contact", `INSERT INTO people (first_name) VALUES ('Sam')`, nil},
		{"phone not E.164", `INSERT INTO people (first_name, phone) VALUES ('Sam', '216-555-0142')`, nil},
		{"cancelled without cancelled_at", `INSERT INTO claims (occurrence_id, person_id, status, manage_token_hash, manage_expires_at)
			VALUES (1, 1, 'cancelled', ?, '2026-11-29T00:00:00Z')`, []any{hash32(9)}},
		{"claim for missing occurrence", `INSERT INTO claims (occurrence_id, person_id, status, manage_token_hash, manage_expires_at)
			VALUES (99, 1, 'confirmed', ?, '2026-11-29T00:00:00Z')`, []any{hash32(9)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := migrated(t)
			seedSheet(t, d)
			if _, err := d.Exec(c.q, c.args...); err == nil {
				t.Fatal("insert succeeded, want a constraint error")
			}
		})
	}
}

func TestDeletingSheetCascades(t *testing.T) {
	d := migrated(t)
	id := seedSheet(t, d)
	if _, err := d.Exec(`DELETE FROM sheets WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"sections", "slots", "occurrences", "claims"} {
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s has %d rows after deleting the sheet, want 0", table, n)
		}
	}
}
