package web

import (
	"database/sql"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/hews/signup/internal/sheet"
)

// publishedSheet builds and publishes a by_date sheet with one date and the given slots
// (title → quantity), returning the admin path and the participant path.
func publishedSheet(t *testing.T, base, date string, slots ...string) (admin, share string) {
	t.Helper()
	admin = createSheet(t, base, "by_date")
	wantStatus(t, post(t, base+admin+"/sections", url.Values{"date": {date}}), http.StatusSeeOther)
	_, page := get(t, base+admin)
	path, _ := firstSection(t, page)
	for i := 0; i+1 < len(slots); i += 2 {
		wantStatus(t, post(t, base+path+"/slots", url.Values{"slot_title": {slots[i]}, "quantity": {slots[i+1]}}), http.StatusSeeOther)
	}
	wantStatus(t, post(t, base+admin+"/publish", nil), http.StatusSeeOther)
	_, page = get(t, base+admin)
	m := regexp.MustCompile(`href="(/s/[a-z0-9]+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no share path on the published sheet")
	}
	return admin, m[1]
}

// claim seeds a confirmed claim for a new person on the occurrence of the named slot.
func claim(t *testing.T, d *sql.DB, share, slot, first, last, phone string, qty int) {
	t.Helper()
	s, err := sheet.BySlug(t.Context(), d, strings.TrimPrefix(share, "/s/"))
	if err != nil {
		t.Fatal(err)
	}
	var occ int64
	for _, sec := range s.Sections {
		for _, sl := range sec.Slots {
			if sl.Title == slot {
				occ = sl.OccurrenceID
			}
		}
	}
	res, err := d.Exec(`INSERT INTO people (sheet_id, first_name, last_name, phone) VALUES (?, ?, ?, ?)`, s.ID, first, last, phone)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()
	_, hash, _ := sheet.NewToken()
	if _, err := d.Exec(`INSERT INTO claims (occurrence_id, person_id, quantity, status, manage_token_hash, manage_expires_at)
		VALUES (?, ?, ?, 'confirmed', ?, '2099-01-01T00:00:00.000Z')`, occ, pid, qty, hash); err != nil {
		t.Fatal(err)
	}
}

func TestParticipantSeesWhatIsNeeded(t *testing.T) {
	srv, _, d := newServerDB(t)
	_, share := publishedSheet(t, srv.URL, "2099-10-30", "Crafts table", "3", "Clean up", "1", "Napkins", "")
	claim(t, d, share, "Crafts table", "Jenny", "rivera", "+12165550142", 1)
	claim(t, d, share, "Clean up", "Sam", "", "+12165550199", 1)

	res, page := get(t, srv.URL+share)
	wantStatus(t, res, http.StatusOK)
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", res.Header.Get("Cache-Control"))
	}
	for _, want := range []string{
		"Class party helpers", "Ms. Alvarez", "Fri, Oct 30",
		"2 of 4 filled", "2 more needed", // header: limited slots only
		"Jenny R.", "Sam", "Full", "No one yet",
		`action="` + share + `/claim"`, "Continue",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("sheet page missing %q", want)
		}
	}
	for _, leak := range []string{"Rivera", "rivera", "2165550142", "2165550199", "/o/"} {
		if strings.Contains(page, leak) {
			t.Errorf("sheet page shows %q to participants", leak)
		}
	}
	// Two open slots can be picked (Crafts table and Napkins); the full one cannot.
	if n := strings.Count(page, `name="o"`); n != 2 {
		t.Errorf("%d sign-up toggles, want 2", n)
	}
}

func TestParticipantCannotSeeDraftsOrUnknownSheets(t *testing.T) {
	srv, _ := newServer(t)
	admin := createSheet(t, srv.URL, "slots_only")
	_, page := get(t, srv.URL+admin)
	m := regexp.MustCompile(`/s/[a-z0-9]+`).FindString(page)
	if m != "" {
		t.Fatalf("draft manage page already shows a share path %q", m)
	}
	res, _ := get(t, srv.URL+"/s/doesnotexist0000")
	wantStatus(t, res, http.StatusNotFound)
}

func TestClosedSheetOffersNothing(t *testing.T) {
	srv, _, d := newServerDB(t)
	_, share := publishedSheet(t, srv.URL, "2099-10-30", "Crafts table", "3")
	if _, err := d.Exec(`UPDATE sheets SET status = 'closed' WHERE slug = ?`, strings.TrimPrefix(share, "/s/")); err != nil {
		t.Fatal(err)
	}
	res, page := get(t, srv.URL+share)
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(page, "This sheet is closed.") || strings.Contains(page, `name="o"`) || strings.Contains(page, "Continue") {
		t.Fatal("closed sheet still offers sign-ups")
	}
}

func TestBuildSheetPageHidesPastDatesAndCounts(t *testing.T) {
	s := sheet.Sheet{Status: sheet.Open, Sections: []sheet.Section{
		{ID: 1, Date: "2026-10-29", Slots: []sheet.Slot{{OccurrenceID: 1, Title: "Old", Quantity: 5}}},
		{ID: 2, Date: "2026-10-30", Slots: []sheet.Slot{
			{OccurrenceID: 2, Title: "A", Quantity: 3},
			{OccurrenceID: 3, Title: "B", Quantity: 2},
			{OccurrenceID: 4, Title: "C"},
		}},
		{ID: 3, Date: "2026-11-02", Slots: []sheet.Slot{{OccurrenceID: 5, Title: "D", Quantity: 1}}},
	}}
	taken := map[int64]sheet.Taken{2: {Count: 1}, 3: {Count: 3}, 5: {Count: 1}}
	pg := buildSheetPage(s, taken, "2026-10-30")
	if len(pg.Sections) != 2 || pg.Sections[0].Date != "2026-10-30" {
		t.Fatalf("sections = %+v, want the past date hidden", pg.Sections)
	}
	if pg.Wanted != 6 || pg.Filled != 4 || !pg.Unlimited {
		t.Fatalf("wanted %d filled %d unlimited %v, want 6, 4 (A 1, over-full B capped at 2, D 1), true", pg.Wanted, pg.Filled, pg.Unlimited)
	}
	if a, b := pg.Sections[0].Slots[0], pg.Sections[0].Slots[1]; a.Needed != 2 || a.Full || b.Needed != 0 || !b.Full {
		t.Fatalf("A = %+v, B = %+v", a, b)
	}
	if pg.Sections[0].Needed != 2 || pg.Dates != "Oct 30 – Nov 2" {
		t.Fatalf("section needed %d, dates %q", pg.Sections[0].Needed, pg.Dates)
	}
}
