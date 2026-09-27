package sheet

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hews/signup/internal/db"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return d
}

func basics(format string) Basics {
	return Basics{Title: " Class party helpers ", OrganizerName: "Ms. Alvarez",
		TimeZone: "America/New_York", Format: format}
}

func mustCreate(t *testing.T, d *sql.DB, format string) (Sheet, string) {
	t.Helper()
	s, token, err := Create(context.Background(), d, basics(format))
	if err != nil {
		t.Fatal(err)
	}
	return s, token
}

func reload(t *testing.T, d *sql.DB, token string) Sheet {
	t.Helper()
	s, err := ByAdminToken(context.Background(), d, token)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreateStoresOnlyTheTokenHash(t *testing.T) {
	d := testDB(t)
	s, token := mustCreate(t, d, ByDate)
	if s.Title != "Class party helpers" || s.Status != Draft {
		t.Fatalf("created %+v", s)
	}
	if len(s.Slug) < 16 || len(token) < 40 {
		t.Fatalf("slug %q or token too short", s.Slug)
	}
	var stored []byte
	if err := d.QueryRow(`SELECT admin_token_hash FROM sheets WHERE id = ?`, s.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte(token)) || !bytes.Equal(stored, HashToken(token)) {
		t.Fatal("stored value is not the token's SHA-256")
	}
	if _, err := ByAdminToken(context.Background(), d, token+"x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong token: err = %v, want ErrNotFound", err)
	}
	if got, err := BySlug(context.Background(), d, s.Slug); err != nil || got.ID != s.ID {
		t.Fatalf("BySlug = %v, %v", got.ID, err)
	}
}

func TestCreateRejectsBadBasics(t *testing.T) {
	d := testDB(t)
	cases := map[string]Basics{
		"title":     {Title: "  ", TimeZone: "UTC", Format: ByDate},
		"time_zone": {Title: "T", TimeZone: "Mars/Olympus", Format: ByDate},
		"format":    {Title: "T", TimeZone: "UTC", Format: "rsvp"},
	}
	for field, b := range cases {
		_, _, err := Create(context.Background(), d, b)
		var bad Invalid
		if !errors.As(err, &bad) || bad[field] == "" {
			t.Errorf("%s: err = %v, want Invalid[%s]", field, err, field)
		}
	}
}

func TestByDateSheetBuildsInOrder(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	s, token := mustCreate(t, d, ByDate)
	for _, date := range []string{"2026-10-31", "2026-10-30"} {
		if err := AddSection(ctx, d, s, "", date); err != nil {
			t.Fatal(err)
		}
		s = reload(t, d, token)
	}
	if err := AddSection(ctx, d, s, "", "2026-10-30"); err == nil {
		t.Fatal("duplicate date accepted")
	}
	if got := []string{s.Sections[0].Date, s.Sections[1].Date}; got[0] != "2026-10-30" || got[1] != "2026-10-31" {
		t.Fatalf("sections = %v, want dates in order", got)
	}
	first := s.Sections[0].ID
	for _, in := range []SlotInput{
		{Title: "Clean up", Quantity: 2, Start: "14:30", End: "15:00"},
		{Title: "Crafts table", Quantity: 3, Start: "13:00", End: "14:30"},
		{Title: "Anything else"},
	} {
		if err := AddSlot(ctx, d, s, first, in); err != nil {
			t.Fatal(err)
		}
	}
	s = reload(t, d, token)
	slots := s.Sections[0].Slots
	if len(slots) != 3 || slots[0].Title != "Anything else" || slots[1].Title != "Crafts table" || slots[2].Title != "Clean up" {
		t.Fatalf("slots = %+v, want untimed first, then by start time", slots)
	}
	if slots[1].Quantity != 3 || slots[0].Quantity != 0 {
		t.Fatalf("quantities = %d, %d", slots[1].Quantity, slots[0].Quantity)
	}
	var date string
	if err := d.QueryRow(`SELECT date FROM occurrences WHERE id = ?`, slots[1].OccurrenceID).Scan(&date); err != nil || date != "2026-10-30" {
		t.Fatalf("occurrence date = %q, %v; want the section's date", date, err)
	}
}

func TestSlotsOnlySheetUsesHeadings(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	s, token := mustCreate(t, d, SlotsOnly)
	if err := AddSection(ctx, d, s, "", ""); err == nil {
		t.Fatal("heading without a name accepted")
	}
	if err := AddSection(ctx, d, s, "Things to bring", "2026-10-30"); err != nil {
		t.Fatal(err)
	}
	s = reload(t, d, token)
	if s.Sections[0].Date != "" || s.Sections[0].Title != "Things to bring" {
		t.Fatalf("section = %+v, want a heading with no date", s.Sections[0])
	}
}

func TestAddSlotValidates(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	s, token := mustCreate(t, d, ByDate)
	if err := AddSection(ctx, d, s, "", "2026-10-30"); err != nil {
		t.Fatal(err)
	}
	s = reload(t, d, token)
	sec := s.Sections[0].ID
	cases := map[string]SlotInput{
		"slot_title": {Title: " "},
		"quantity":   {Title: "X", Quantity: -1},
		"start":      {Title: "X", Start: "25:00"},
		"end":        {Title: "X", End: "13:00"},
	}
	for field, in := range cases {
		var bad Invalid
		if err := AddSlot(ctx, d, s, sec, in); !errors.As(err, &bad) || bad[field] == "" {
			t.Errorf("%s: err = %v, want Invalid[%s]", field, err, field)
		}
	}
	if err := AddSlot(ctx, d, s, sec, SlotInput{Title: "Late", Start: "22:00", End: "01:00"}); err != nil {
		t.Errorf("overnight slot: %v", err)
	}
}

func TestOneSheetCannotTouchAnother(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	a, tokenA := mustCreate(t, d, ByDate)
	b, tokenB := mustCreate(t, d, ByDate)
	if err := AddSection(ctx, d, b, "", "2026-10-30"); err != nil {
		t.Fatal(err)
	}
	b = reload(t, d, tokenB)
	if err := AddSlot(ctx, d, b, b.Sections[0].ID, SlotInput{Title: "B's slot"}); err != nil {
		t.Fatal(err)
	}
	b = reload(t, d, tokenB)
	a = reload(t, d, tokenA)
	if err := AddSlot(ctx, d, a, b.Sections[0].ID, SlotInput{Title: "Intruder"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("AddSlot into another sheet's section: err = %v", err)
	}
	if err := RemoveSlot(ctx, d, a, b.Sections[0].Slots[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("RemoveSlot on another sheet: err = %v", err)
	}
	if err := RemoveSection(ctx, d, a, b.Sections[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("RemoveSection on another sheet: err = %v", err)
	}
	if got := reload(t, d, tokenB).Slots(); got != 1 {
		t.Fatalf("sheet B has %d slots, want 1", got)
	}
}

func TestRemoveRefusesWhileClaimed(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	s, token := mustCreate(t, d, ByDate)
	if err := AddSection(ctx, d, s, "", "2026-10-30"); err != nil {
		t.Fatal(err)
	}
	s = reload(t, d, token)
	if err := AddSlot(ctx, d, s, s.Sections[0].ID, SlotInput{Title: "Crafts", Quantity: 3}); err != nil {
		t.Fatal(err)
	}
	s = reload(t, d, token)
	slot := s.Sections[0].Slots[0]
	_, hash, _ := NewToken()
	if _, err := d.Exec(`INSERT INTO people (id, sheet_id, first_name, phone) VALUES (1, ?, 'Jenny', '+12165550142')`, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO claims (occurrence_id, person_id, status, manage_token_hash, manage_expires_at)
		VALUES (?, 1, 'confirmed', ?, '2026-11-29T00:00:00.000Z')`, slot.OccurrenceID, hash); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSlot(ctx, d, s, slot.ID); !errors.Is(err, ErrHasClaims) {
		t.Errorf("RemoveSlot: err = %v, want ErrHasClaims", err)
	}
	if err := RemoveSection(ctx, d, s, s.Sections[0].ID); !errors.Is(err, ErrHasClaims) {
		t.Errorf("RemoveSection: err = %v, want ErrHasClaims", err)
	}
	if _, err := d.Exec(`UPDATE claims SET status = 'cancelled', cancelled_at = '2026-10-01T09:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	// Someone on the sheet with no claim yet (as mid-way through adding them) is left alone.
	if _, err := d.Exec(`INSERT INTO people (id, sheet_id, first_name, phone) VALUES (2, ?, 'Sam', '+12165550199')`, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSlot(ctx, d, s, slot.ID); err != nil {
		t.Errorf("RemoveSlot after cancellation: %v", err)
	}
	var people int
	if err := d.QueryRow(`SELECT count(*) FROM people WHERE sheet_id = ?`, s.ID).Scan(&people); err != nil {
		t.Fatal(err)
	}
	if people != 1 {
		t.Errorf("%d people on the sheet after removing the slot, want 1 (Jenny gone, Sam kept)", people)
	}
	var name string
	if err := d.QueryRow(`SELECT first_name FROM people WHERE sheet_id = ?`, s.ID).Scan(&name); err != nil || name != "Sam" {
		t.Errorf("remaining person = %q, %v; want Sam", name, err)
	}
}

func TestPublishNeedsASlot(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	s, token := mustCreate(t, d, SlotsOnly)
	var bad Invalid
	if err := Publish(ctx, d, s); !errors.As(err, &bad) {
		t.Fatalf("publishing an empty sheet: err = %v", err)
	}
	if err := AddSection(ctx, d, s, "Things to bring", ""); err != nil {
		t.Fatal(err)
	}
	s = reload(t, d, token)
	if err := AddSlot(ctx, d, s, s.Sections[0].ID, SlotInput{Title: "Napkins"}); err != nil {
		t.Fatal(err)
	}
	s = reload(t, d, token)
	if err := Publish(ctx, d, s); err != nil {
		t.Fatal(err)
	}
	if got := reload(t, d, token).Status; got != Open {
		t.Fatalf("status = %q, want open", got)
	}
	if err := Publish(ctx, d, reload(t, d, token)); err != nil {
		t.Errorf("publishing an open sheet again: %v", err)
	}
	if _, err := d.Exec(`UPDATE sheets SET status = 'closed' WHERE id = ?`, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := Publish(ctx, d, reload(t, d, token)); !errors.As(err, &bad) {
		t.Errorf("publishing a closed sheet: err = %v, want Invalid", err)
	}
}

func TestLimitsCountCharactersNotBytes(t *testing.T) {
	d := testDB(t)
	b := basics(SlotsOnly)
	b.Title = strings.Repeat("班", 120) // 360 bytes, 120 characters
	if _, _, err := Create(context.Background(), d, b); err != nil {
		t.Fatalf("120-character title: %v", err)
	}
	b.Title = strings.Repeat("班", 121)
	var bad Invalid
	if _, _, err := Create(context.Background(), d, b); !errors.As(err, &bad) || bad["title"] == "" {
		t.Fatalf("121-character title: err = %v, want Invalid[title]", err)
	}
}

func TestSameDateTwiceIsRefusedByTheDatabase(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	s, _ := mustCreate(t, d, ByDate)
	// Both calls use the sheet as loaded before either ran, as two concurrent requests would.
	if err := AddSection(ctx, d, s, "", "2026-10-30"); err != nil {
		t.Fatal(err)
	}
	var bad Invalid
	if err := AddSection(ctx, d, s, "", "2026-10-30"); !errors.As(err, &bad) || bad["date"] == "" {
		t.Fatalf("second add of the same date: err = %v, want Invalid[date]", err)
	}
}
