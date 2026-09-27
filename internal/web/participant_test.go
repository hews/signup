package web

import (
	"database/sql"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
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
	srv, _, d := newServerDB(t)
	admin := createSheet(t, srv.URL, "slots_only")
	wantStatus(t, post(t, srv.URL+admin+"/sections", url.Values{"title": {"Bring"}}), http.StatusSeeOther)
	_, page := get(t, srv.URL+admin)
	path, _ := firstSection(t, page)
	wantStatus(t, post(t, srv.URL+path+"/slots", url.Values{"slot_title": {"Napkins"}}), http.StatusSeeOther)
	_, page = get(t, srv.URL+admin)
	if m := regexp.MustCompile(`/s/[a-z0-9]+`).FindString(page); m != "" {
		t.Fatalf("draft manage page already shows a share path %q", m)
	}
	// The draft has a slug already; its link must not work until it is published.
	var slug string
	if err := d.QueryRow(`SELECT slug FROM sheets`).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	res, page := get(t, srv.URL+"/s/"+slug)
	wantStatus(t, res, http.StatusNotFound)
	if strings.Contains(page, "Napkins") {
		t.Fatal("draft content leaked on its participant link")
	}
	wantStatus(t, post(t, srv.URL+admin+"/publish", nil), http.StatusSeeOther)
	res, _ = get(t, srv.URL+"/s/"+slug)
	wantStatus(t, res, http.StatusOK)

	res, _ = get(t, srv.URL+"/s/doesnotexist0000")
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

// occurrences returns the sign-up toggle values on a sheet page, in page order.
func occurrences(t *testing.T, page string) []string {
	t.Helper()
	var out []string
	for _, m := range regexp.MustCompile(`name="o" value="(\d+)"`).FindAllStringSubmatch(page, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestParticipantSignsUp(t *testing.T) {
	srv, logs := newServer(t)
	_, share := publishedSheet(t, srv.URL, "2099-10-30", "Crafts table", "3", "Clean up", "2")
	_, page := get(t, srv.URL+share)
	occ := occurrences(t, page)

	res, form := get(t, srv.URL+share+"/claim?o="+occ[0]+"&o="+occ[1])
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{"2 slots selected", "Crafts table", "Clean up", `name="phone"`, "never shown to anyone else"} {
		if !strings.Contains(form, want) {
			t.Errorf("claim form missing %q", want)
		}
	}

	res = post(t, srv.URL+share+"/claim", url.Values{"o": {occ[0], occ[1]}, "comment_" + occ[0]: {"bringing glue"},
		"first": {"Jenny"}, "last": {"Rivera"}, "phone": {"(216) 555-0142"}})
	wantStatus(t, res, http.StatusSeeOther)
	mine := res.Header.Get("Location")
	if !strings.HasPrefix(mine, "/m/") || !strings.HasSuffix(mine, "?new=1") {
		t.Fatalf("redirect = %q, want the manage link", mine)
	}
	res, page = get(t, srv.URL+mine)
	wantStatus(t, res, http.StatusOK)
	for _, want := range []string{"You're signed up, Jenny", "Crafts table", "Clean up", "“bringing glue”", "Confirmed", "Keep this page's address"} {
		if !strings.Contains(page, want) {
			t.Errorf("confirmation missing %q", want)
		}
	}
	_, page = get(t, srv.URL+share)
	if !strings.Contains(page, "Jenny R.") || strings.Contains(page, "Rivera") {
		t.Error("sheet page should show Jenny R. and no surname")
	}

	token := strings.TrimSuffix(strings.TrimPrefix(mine, "/m/"), "?new=1")
	for _, leak := range []string{"Jenny", "Rivera", "2165550142", "555-0142", "glue", token} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("logs carry %q", leak)
		}
	}
	res, _ = get(t, srv.URL+"/m/"+token+"x")
	wantStatus(t, res, http.StatusNotFound)
}

func TestSlotThatJustFilledOffersTheWaitlist(t *testing.T) {
	srv, _ := newServer(t)
	_, share := publishedSheet(t, srv.URL, "2099-10-30", "Crafts table", "1", "Clean up", "2")
	_, page := get(t, srv.URL+share)
	occ := occurrences(t, page)
	// Sam takes the only Crafts place while Jenny has the form open.
	wantStatus(t, post(t, srv.URL+share+"/claim", url.Values{"o": {occ[0]}, "first": {"Sam"}, "phone": {"2165550199"}}), http.StatusSeeOther)

	jenny := url.Values{"o": {occ[0], occ[1]}, "first": {"Jenny"}, "phone": {"2165550142"}}
	res := post(t, srv.URL+share+"/claim", jenny)
	wantStatus(t, res, http.StatusConflict)
	page = body(t, res)
	for _, want := range []string{"Just filled.", `name="waitlist_` + occ[0] + `" checked`, `value="Jenny"`, "Nothing has been saved yet"} {
		if !strings.Contains(page, want) {
			t.Errorf("just-filled form missing %q", want)
		}
	}
	jenny.Set("waitlist_"+occ[0], "on")
	res = post(t, srv.URL+share+"/claim", jenny)
	wantStatus(t, res, http.StatusSeeOther)
	_, page = get(t, srv.URL+res.Header.Get("Location"))
	if !strings.Contains(page, "Waitlist #1") || !strings.Contains(page, "Confirmed") {
		t.Fatal("confirmation should show the waitlist place and the confirmed slot")
	}
}

func TestFullSlotWithoutAWaitlistIsTakenOut(t *testing.T) {
	srv, _, d := newServerDB(t)
	_, share := publishedSheet(t, srv.URL, "2099-10-30", "Crafts table", "1", "Clean up", "2")
	if _, err := d.Exec(`UPDATE sheets SET allow_waitlist = 0`); err != nil {
		t.Fatal(err)
	}
	_, page := get(t, srv.URL+share)
	occ := occurrences(t, page)
	wantStatus(t, post(t, srv.URL+share+"/claim", url.Values{"o": {occ[0]}, "first": {"Sam"}, "phone": {"2165550199"}}), http.StatusSeeOther)
	res := post(t, srv.URL+share+"/claim", url.Values{"o": {occ[0], occ[1]}, "first": {"Jenny"}, "phone": {"2165550142"}})
	wantStatus(t, res, http.StatusConflict)
	page = body(t, res)
	if !strings.Contains(page, "has been taken out") || strings.Contains(page, `value="`+occ[0]+`"`) || !strings.Contains(page, `value="`+occ[1]+`"`) {
		t.Fatal("the full slot should leave the form and the other stay")
	}
}

func TestClaimFormKeepsWhatWasTyped(t *testing.T) {
	srv, _ := newServer(t)
	_, share := publishedSheet(t, srv.URL, "2099-10-30", "Crafts table", "3")
	_, page := get(t, srv.URL+share)
	occ := occurrences(t, page)
	res := post(t, srv.URL+share+"/claim", url.Values{"o": {occ[0]}, "first": {"Jenny"}, "phone": {"555-0142"}, "comment_" + occ[0]: {"glue"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	page = body(t, res)
	for _, want := range []string{"Enter a mobile number like", `value="Jenny"`, `value="glue"`} {
		if !strings.Contains(page, want) {
			t.Errorf("form lost %q", want)
		}
	}
}

func TestClaimIgnoresSlotsFromOtherSheets(t *testing.T) {
	srv, _ := newServer(t)
	_, share := publishedSheet(t, srv.URL, "2099-10-30", "Crafts table", "3")
	_, other := publishedSheet(t, srv.URL, "2099-10-30", "Theirs", "3")
	_, page := get(t, srv.URL+other)
	theirs := occurrences(t, page)[0]

	res, _ := get(t, srv.URL+share+"/claim?o="+theirs)
	wantStatus(t, res, http.StatusSeeOther)
	res = post(t, srv.URL+share+"/claim", url.Values{"o": {theirs}, "first": {"Jenny"}, "phone": {"2165550142"}})
	wantStatus(t, res, http.StatusSeeOther)
	if loc := res.Header.Get("Location"); loc != share {
		t.Fatalf("redirect = %q, want back to the sheet", loc)
	}
	_, page = get(t, srv.URL+other)
	if strings.Contains(page, "Jenny") {
		t.Fatal("a claim landed on the other sheet")
	}
	res, _ = get(t, srv.URL+share+"/claim")
	wantStatus(t, res, http.StatusSeeOther)
}

func TestPickOnAPastDateIsRefusedNotDropped(t *testing.T) {
	srv, _, d := newServerDB(t)
	_, share := publishedSheet(t, srv.URL, "2099-10-30", "Crafts table", "3")
	_, page := get(t, srv.URL+share)
	future := occurrences(t, page)[0]
	// A second date that is already past by the time the form is sent.
	var sheetID int64
	if err := d.QueryRow(`SELECT id FROM sheets`).Scan(&sheetID); err != nil {
		t.Fatal(err)
	}
	res, err := d.Exec(`INSERT INTO sections (sheet_id, position, date) VALUES (?, 9, '2000-01-01')`, sheetID)
	if err != nil {
		t.Fatal(err)
	}
	sec, _ := res.LastInsertId()
	res, err = d.Exec(`INSERT INTO slots (section_id, position, title, quantity_wanted) VALUES (?, 0, 'Old', 3)`, sec)
	if err != nil {
		t.Fatal(err)
	}
	slot, _ := res.LastInsertId()
	res, err = d.Exec(`INSERT INTO occurrences (slot_id, date) VALUES (?, '2000-01-01')`, slot)
	if err != nil {
		t.Fatal(err)
	}
	pastID, _ := res.LastInsertId()
	past := strconv.FormatInt(pastID, 10)

	r := post(t, srv.URL+share+"/claim", url.Values{"o": {future, past}, "first": {"Jenny"}, "phone": {"2165550142"}})
	wantStatus(t, r, http.StatusUnprocessableEntity)
	if !strings.Contains(body(t, r), "One of those dates has passed") {
		t.Fatal("the past pick was not explained")
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM claims`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d claims saved, want none (all or nothing)", n)
	}
}
