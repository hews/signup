package sheet

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// openSheet publishes a by_date sheet on date with one slot per quantity given (0 = no limit)
// and returns it reloaded, with its admin token.
func openSheet(t *testing.T, d *sql.DB, date string, quantities ...int) (Sheet, string) {
	t.Helper()
	ctx := context.Background()
	s, token := mustCreate(t, d, ByDate)
	if err := AddSection(ctx, d, s, "", date); err != nil {
		t.Fatal(err)
	}
	s = reload(t, d, token)
	for i, q := range quantities {
		if err := AddSlot(ctx, d, s, s.Sections[0].ID, SlotInput{Title: string(rune('A' + i)), Quantity: q}); err != nil {
			t.Fatal(err)
		}
	}
	s = reload(t, d, token)
	if err := Publish(ctx, d, s); err != nil {
		t.Fatal(err)
	}
	return reload(t, d, token), token
}

func occ(s Sheet, i int) int64 { return s.Sections[0].Slots[i].OccurrenceID }

var jenny = Person{First: "Jenny", Last: "Rivera", Phone: "(216) 555-0142"}

func person(first, phone string) Person { return Person{First: first, Phone: phone} }

func count(t *testing.T, d *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := d.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNormalizePhone(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"(216) 555-0142", "+12165550142", true},
		{"216.555.0142", "+12165550142", true},
		{"1 216 555 0142", "+12165550142", true},
		{"+44 20 7946 0958", "+442079460958", true},
		{"555-0142", "", false},
		{"(016) 555-0142", "", false},
		{"+0 20 7946 0958", "", false},
		{"216-555-O142", "", false}, // a letter O
		{"+1 216 555 0142 ext 3", "", false},
		{"٢١٦٥٥٥٠١٤٢", "", false}, // non-ASCII digits
	} {
		got, ok := NormalizePhone(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("NormalizePhone(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestClaimConfirmsAndTheTokenOpensIt(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	s, _ := openSheet(t, d, "2099-10-30", 3, 0)
	token, out, err := Claim(ctx, d, s, "2026-09-27", jenny, []Pick{{OccurrenceID: occ(s, 0), Comment: " bringing glue "}, {OccurrenceID: occ(s, 1)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Status != Confirmed || out[1].Status != Confirmed || token == "" {
		t.Fatalf("outcomes %+v, token %q", out, token)
	}
	var phone string
	if err := d.QueryRow(`SELECT phone FROM people`).Scan(&phone); err != nil || phone != "+12165550142" {
		t.Fatalf("stored phone %q, %v", phone, err)
	}
	m, err := ByManageToken(ctx, d, token)
	if err != nil {
		t.Fatal(err)
	}
	if m.First != "Jenny" || m.Sheet.ID != s.ID || len(m.Claims) != 2 || m.Claims[0].Comment != "bringing glue" {
		t.Fatalf("mine = %+v", m)
	}
	if _, err := ByManageToken(ctx, d, token+"x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong token: %v", err)
	}
	if n := count(t, d, `SELECT count(*) FROM claims WHERE manage_token_hash = ?`, []byte(token)); n != 0 {
		t.Fatal("a raw token was stored")
	}
}

func TestClaimIsAllOrNothingWhenASlotFills(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	s, _ := openSheet(t, d, "2099-10-30", 1, 5)
	if _, _, err := Claim(ctx, d, s, "2026-09-27", person("Sam", "2165550199"), []Pick{{OccurrenceID: occ(s, 0)}}); err != nil {
		t.Fatal(err)
	}
	// Jenny chose both slots before Sam took the last place on A.
	_, _, err := Claim(ctx, d, s, "2026-09-27", jenny, []Pick{{OccurrenceID: occ(s, 0)}, {OccurrenceID: occ(s, 1)}})
	var full *FullError
	if !errors.As(err, &full) || len(full.Occurrences) != 1 || full.Occurrences[0] != occ(s, 0) || !full.Waitlist {
		t.Fatalf("err = %v, want FullError for slot A with a waitlist offered", err)
	}
	if n := count(t, d, `SELECT count(*) FROM people WHERE first_name = 'Jenny'`); n != 0 {
		t.Fatal("a failed claim saved the person")
	}
	// She accepts the waitlist for A; B is confirmed as before.
	token, out, err := Claim(ctx, d, s, "2026-09-27", jenny, []Pick{{OccurrenceID: occ(s, 0), Waitlist: true}, {OccurrenceID: occ(s, 1)}})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Status != Waitlisted || out[1].Status != Confirmed {
		t.Fatalf("outcomes = %+v", out)
	}
	m, _ := ByManageToken(ctx, d, token)
	for _, c := range m.Claims {
		if c.Status == Waitlisted && c.Position != 1 {
			t.Errorf("waitlist position = %d, want 1", c.Position)
		}
	}
}

func TestClaimWithoutAWaitlistStaysFull(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	s, _ := openSheet(t, d, "2099-10-30", 1)
	if _, err := d.Exec(`UPDATE sheets SET allow_waitlist = 0 WHERE id = ?`, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Claim(ctx, d, s, "2026-09-27", person("Sam", "2165550199"), []Pick{{OccurrenceID: occ(s, 0)}}); err != nil {
		t.Fatal(err)
	}
	_, _, err := Claim(ctx, d, s, "2026-09-27", jenny, []Pick{{OccurrenceID: occ(s, 0), Waitlist: true}})
	var full *FullError
	if !errors.As(err, &full) || full.Waitlist {
		t.Fatalf("err = %v, want FullError with no waitlist", err)
	}
}

func TestClaimRefusals(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	s, _ := openSheet(t, d, "2099-10-30", 3)
	other, _ := openSheet(t, d, "2099-10-30", 3)
	var bad Invalid

	if _, _, err := Claim(ctx, d, s, "2026-09-27", jenny, []Pick{{OccurrenceID: occ(other, 0)}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("another sheet's slot: err = %v, want ErrNotFound", err)
	}
	if _, _, err := Claim(ctx, d, s, "2099-11-01", jenny, []Pick{{OccurrenceID: occ(s, 0)}}); !errors.As(err, &bad) {
		t.Errorf("past date: err = %v, want Invalid", err)
	}
	if _, _, err := Claim(ctx, d, s, "2026-09-27", jenny, nil); !errors.As(err, &bad) {
		t.Errorf("no picks: err = %v, want Invalid", err)
	}
	for field, p := range map[string]Person{
		"first": {Phone: "2165550142"},
		"phone": {First: "Jenny", Phone: "555-0142"},
		"email": {First: "Jenny", Email: "jenny at example"},
	} {
		if _, _, err := Claim(ctx, d, s, "2026-09-27", p, []Pick{{OccurrenceID: occ(s, 0)}}); !errors.As(err, &bad) || bad[field] == "" {
			t.Errorf("%s: err = %v, want Invalid[%s]", field, err, field)
		}
	}
	if _, _, err := Claim(ctx, d, s, "2026-09-27", Person{First: "Jenny"}, []Pick{{OccurrenceID: occ(s, 0)}}); !errors.As(err, &bad) || bad["phone"] == "" {
		t.Errorf("no contact: err = %v, want Invalid[phone]", err)
	}

	if _, _, err := Claim(ctx, d, s, "2026-09-27", jenny, []Pick{{OccurrenceID: occ(s, 0)}}); err != nil {
		t.Fatal(err)
	}
	same := Person{First: "Jen", Phone: "+1 216 555 0142"}
	if _, _, err := Claim(ctx, d, s, "2026-09-27", same, []Pick{{OccurrenceID: occ(s, 0)}}); !errors.As(err, &bad) {
		t.Errorf("same number twice on a slot: err = %v, want Invalid", err)
	}

	if _, err := d.Exec(`UPDATE sheets SET status = 'closed' WHERE id = ?`, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Claim(ctx, d, s, "2026-09-27", person("Sam", "2165550199"), []Pick{{OccurrenceID: occ(s, 0)}}); !errors.Is(err, ErrClosed) {
		t.Errorf("closed sheet: err = %v, want ErrClosed", err)
	}
}

func TestEmailOnlyAndExpiry(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	s, _ := openSheet(t, d, "2099-10-30", 0)
	token, _, err := Claim(ctx, d, s, "2026-09-27", Person{First: "Ana", Email: "ana@example.org"}, []Pick{{OccurrenceID: occ(s, 0)}})
	if err != nil {
		t.Fatal(err)
	}
	var exp string
	if err := d.QueryRow(`SELECT manage_expires_at FROM claims`).Scan(&exp); err != nil || exp != "2099-11-30T00:00:00.000Z" {
		t.Fatalf("expiry %q, %v; want 31 days after the last date", exp, err)
	}
	if _, err := d.Exec(`UPDATE claims SET manage_expires_at = '2000-01-01T00:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	if _, err := ByManageToken(ctx, d, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired token: err = %v, want ErrNotFound", err)
	}
}

func TestEmailRules(t *testing.T) {
	var bad Invalid
	if _, err := (Person{First: "Jen", Phone: "2165550142", Email: "not an email"}).normalised("off"); err != nil {
		t.Errorf("email ignored when the sheet collects none: %v", err)
	}
	if _, err := (Person{First: "Jen"}).normalised("required"); !errors.As(err, &bad) || bad["email"] == "" || bad["phone"] != "" {
		t.Errorf("required email with no contact: %v, want the email message", err)
	}
}

// TestLastPlaceGoesToExactlyOne races many people for a slot's last places through the real
// database (db.Open's DSN), all at once. Capacity is atomic: exactly the places available
// are confirmed, and everyone else is told the slot filled. Nothing else may fail.
func TestLastPlaceGoesToExactlyOne(t *testing.T) {
	for _, places := range []int{1, 3} {
		t.Run(fmt.Sprintf("%d places", places), func(t *testing.T) {
			d := testDB(t)
			s, _ := openSheet(t, d, "2099-10-30", places)
			const racers = 20
			var wg sync.WaitGroup
			start := make(chan struct{})
			errs := make([]error, racers)
			for i := range racers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					p := Person{First: fmt.Sprintf("Racer%d", i), Phone: fmt.Sprintf("+1216555%04d", i)}
					_, _, errs[i] = Claim(context.Background(), d, s, "2026-09-27", p, []Pick{{OccurrenceID: occ(s, 0)}})
				}()
			}
			close(start)
			wg.Wait()
			won, filled := 0, 0
			for _, err := range errs {
				var full *FullError
				switch {
				case err == nil:
					won++
				case errors.As(err, &full):
					filled++
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}
			if won != places || filled != racers-places {
				t.Fatalf("%d confirmed, %d told it filled; want %d and %d", won, filled, places, racers-places)
			}
			if n := count(t, d, `SELECT coalesce(sum(quantity), 0) FROM claims WHERE status = 'confirmed'`); n != places {
				t.Fatalf("%d places confirmed in the database, want %d", n, places)
			}
			if n := count(t, d, `SELECT count(*) FROM people`); n != places {
				t.Fatalf("%d people stored, want %d (losers leave nothing behind)", n, places)
			}
		})
	}
}
