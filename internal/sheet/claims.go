package sheet

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Claim statuses.
const (
	Confirmed  = "confirmed"
	Waitlisted = "waitlisted"
	Cancelled  = "cancelled"
)

const maxComment = 500

// Person is who is signing up, as typed.
type Person struct {
	First, Last string
	Phone       string // as typed; normalised to E.164 before storing
	Email       string
}

// Pick is one slot a person chose, with anything they added for it.
type Pick struct {
	OccurrenceID int64
	Comment      string
	Waitlist     bool // if it is full, join its waitlist rather than fail
}

// Outcome is what one pick became.
type Outcome struct {
	OccurrenceID int64
	Status       string // Confirmed or Waitlisted
}

// FullError means some picks filled up before the person submitted and they did not ask to
// wait for them. Nothing was saved; the person can choose again.
type FullError struct {
	Occurrences []int64
	Waitlist    bool // whether the sheet offers a waitlist
}

func (e *FullError) Error() string { return fmt.Sprintf("%d slot(s) just filled", len(e.Occurrences)) }

// ErrClosed means the sheet stopped taking sign-ups.
var ErrClosed = errors.New("sheet is not open")

// Claim signs a person up for their picks, all or nothing, in one transaction that holds the
// write lock from the start: every pick is checked against this sheet, today's date in the
// sheet's zone, and the places left, at the moment of writing. It returns one manage token
// (shown to the person once; only its hash is stored) and what each pick became.
func Claim(ctx context.Context, d *sql.DB, s Sheet, today string, p Person, picks []Pick) (string, []Outcome, error) {
	p, err := p.normalised(s.CollectEmail)
	if err != nil {
		return "", nil, err
	}
	if len(picks) == 0 {
		return "", nil, Invalid{"picks": "Choose at least one slot."}
	}
	for i := range picks {
		picks[i].Comment = strings.TrimSpace(picks[i].Comment)
		if utf8.RuneCountInString(picks[i].Comment) > maxComment {
			return "", nil, Invalid{"comment": fmt.Sprintf("Keep comments to %d characters or fewer.", maxComment)}
		}
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()

	var status string
	var allowWaitlist bool
	if err := tx.QueryRowContext(ctx, `SELECT status, allow_waitlist FROM sheets WHERE id = ?`, s.ID).Scan(&status, &allowWaitlist); err != nil {
		return "", nil, err
	}
	if status != Open {
		return "", nil, ErrClosed
	}

	seen := map[int64]bool{}
	outcomes := make([]Outcome, 0, len(picks))
	var full []int64
	for _, pk := range picks {
		if seen[pk.OccurrenceID] {
			continue
		}
		seen[pk.OccurrenceID] = true
		var date sql.NullString
		var wanted sql.NullInt64
		var taken int
		err := tx.QueryRowContext(ctx, `SELECT o.date, sl.quantity_wanted,
			(SELECT coalesce(sum(c.quantity), 0) FROM claims c WHERE c.occurrence_id = o.id AND c.status = 'confirmed')
			FROM occurrences o
			JOIN slots sl ON sl.id = o.slot_id
			JOIN sections se ON se.id = sl.section_id
			WHERE o.id = ? AND se.sheet_id = ?`, pk.OccurrenceID, s.ID).Scan(&date, &wanted, &taken)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, ErrNotFound
		}
		if err != nil {
			return "", nil, err
		}
		if date.Valid && date.String < today {
			return "", nil, Invalid{"picks": "One of those dates has passed. Go back to the sheet and choose again."}
		}
		var dup int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM claims c JOIN people pp ON pp.id = c.person_id
			WHERE c.occurrence_id = ? AND c.status <> 'cancelled' AND pp.sheet_id = ?
			AND ((pp.phone IS NOT NULL AND pp.phone = ?) OR (pp.email IS NOT NULL AND lower(pp.email) = lower(?)))`,
			pk.OccurrenceID, s.ID, p.Phone, p.Email).Scan(&dup); err != nil {
			return "", nil, err
		}
		if dup > 0 {
			return "", nil, Invalid{"picks": "You are already signed up for one of those. Your confirmation has the link to manage it."}
		}
		o := Outcome{OccurrenceID: pk.OccurrenceID, Status: Confirmed}
		if wanted.Valid && taken+1 > int(wanted.Int64) {
			if !pk.Waitlist || !allowWaitlist {
				full = append(full, pk.OccurrenceID)
				continue
			}
			o.Status = Waitlisted
		}
		outcomes = append(outcomes, o)
	}
	if len(full) > 0 {
		return "", nil, &FullError{Occurrences: full, Waitlist: allowWaitlist}
	}

	res, err := tx.ExecContext(ctx, `INSERT INTO people (sheet_id, first_name, last_name, phone, email)
		VALUES (?, ?, ?, nullif(?, ''), nullif(?, ''))`, s.ID, p.First, p.Last, p.Phone, p.Email)
	if err != nil {
		return "", nil, err
	}
	personID, err := res.LastInsertId()
	if err != nil {
		return "", nil, err
	}
	expires, err := manageExpiry(ctx, tx, s.ID)
	if err != nil {
		return "", nil, err
	}
	comments := map[int64]string{}
	for _, pk := range picks {
		comments[pk.OccurrenceID] = pk.Comment
	}
	var first string
	for _, o := range outcomes {
		token, hash, err := NewToken()
		if err != nil {
			return "", nil, err
		}
		if first == "" {
			first = token
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO claims
			(occurrence_id, person_id, comment, status, manage_token_hash, manage_expires_at)
			VALUES (?, ?, ?, ?, ?, ?)`, o.OccurrenceID, personID, comments[o.OccurrenceID], o.Status, hash, expires); err != nil {
			return "", nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", nil, err
	}
	return first, outcomes, nil
}

// manageExpiry is when manage links for this sheet stop working: 30 days after its last date,
// or a year from now for a sheet with no dates.
func manageExpiry(ctx context.Context, tx *sql.Tx, sheetID int64) (string, error) {
	var last sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT max(o.date) FROM occurrences o
		JOIN slots sl ON sl.id = o.slot_id JOIN sections se ON se.id = sl.section_id
		WHERE se.sheet_id = ?`, sheetID).Scan(&last); err != nil {
		return "", err
	}
	t := time.Now().UTC().AddDate(1, 0, 0)
	if last.Valid {
		if d, err := time.Parse(time.DateOnly, last.String); err == nil {
			t = d.AddDate(0, 0, 31) // through the 30th day after
		}
	}
	return t.Format("2006-01-02T15:04:05.000Z"), nil
}

func (p Person) normalised(collectEmail string) (Person, error) {
	p.First, p.Last = strings.TrimSpace(p.First), strings.TrimSpace(p.Last)
	p.Phone, p.Email = strings.TrimSpace(p.Phone), strings.TrimSpace(p.Email)
	bad := Invalid{}
	if p.First == "" {
		bad["first"] = "Enter your first name."
	} else if utf8.RuneCountInString(p.First) > maxShort {
		bad["first"] = fmt.Sprintf("Keep it to %d characters or fewer.", maxShort)
	}
	if utf8.RuneCountInString(p.Last) > maxShort {
		bad["last"] = fmt.Sprintf("Keep it to %d characters or fewer.", maxShort)
	}
	if p.Phone != "" {
		e164, ok := NormalizePhone(p.Phone)
		if !ok {
			bad["phone"] = "Enter a mobile number like (216) 555-0142."
		}
		p.Phone = e164
	}
	if p.Email != "" {
		a, err := mail.ParseAddress(p.Email)
		if err != nil || a.Address != p.Email || !strings.Contains(p.Email, ".") {
			bad["email"] = "Enter an email address like name@example.org."
		}
	}
	if collectEmail == "off" {
		p.Email = ""
	}
	switch {
	case p.Phone == "" && p.Email == "" && bad["phone"] == "" && bad["email"] == "":
		bad["phone"] = "Enter a mobile number, or an email address if you have no mobile."
	case collectEmail == "required" && p.Email == "" && bad["email"] == "":
		bad["email"] = "This sheet asks for an email address."
	}
	if len(bad) > 0 {
		return p, bad
	}
	return p, nil
}

// NormalizePhone turns a typed number into E.164. Ten digits are read as a US number; any
// other number needs its country code with a leading +.
func NormalizePhone(s string) (string, bool) {
	plus := strings.HasPrefix(strings.TrimSpace(s), "+")
	var digits strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsDigit(r) && r < 128:
			digits.WriteRune(r)
		case strings.ContainsRune(" ()-.+", r):
		default:
			return "", false
		}
	}
	n := digits.String()
	switch {
	case plus && len(n) >= 8 && len(n) <= 15 && n[0] != '0':
		return "+" + n, true
	case !plus && len(n) == 10 && n[0] >= '2':
		return "+1" + n, true
	case !plus && len(n) == 11 && n[0] == '1' && n[1] >= '2':
		return "+" + n, true
	}
	return "", false
}

// Mine is what a manage link opens: one person's sign-ups on one sheet.
type Mine struct {
	Sheet  Sheet
	First  string
	Claims []MyClaim
}

// MyClaim is one of a person's sign-ups, described for them.
type MyClaim struct {
	ID           int64
	OccurrenceID int64
	SlotTitle    string
	Date         string
	Start, End   string
	Status       string
	Comment      string
	Position     int // place on the waitlist, 1-based; 0 when confirmed
}

// ByManageToken opens the sign-ups a manage token belongs to, while the token is current.
// Any one of a person's tokens opens all of that person's live sign-ups on the sheet.
func ByManageToken(ctx context.Context, d *sql.DB, token string) (Mine, error) {
	var personID, sheetID int64
	var first string
	err := d.QueryRowContext(ctx, `SELECT p.id, p.sheet_id, p.first_name FROM claims c
		JOIN people p ON p.id = c.person_id
		WHERE c.manage_token_hash = ? AND c.manage_expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`,
		HashToken(token)).Scan(&personID, &sheetID, &first)
	if errors.Is(err, sql.ErrNoRows) {
		return Mine{}, ErrNotFound
	}
	if err != nil {
		return Mine{}, err
	}
	s, err := load(ctx, d, `id = ?`, sheetID)
	if err != nil {
		return Mine{}, err
	}
	m := Mine{Sheet: s, First: first}
	rows, err := d.QueryContext(ctx, `SELECT c.id, c.occurrence_id, sl.title, coalesce(o.date, ''),
		coalesce(o.start_time, ''), coalesce(o.end_time, ''), c.status, c.comment,
		CASE WHEN c.status = 'waitlisted' THEN
			(SELECT count(*) FROM claims w WHERE w.occurrence_id = c.occurrence_id
			 AND w.status = 'waitlisted' AND (w.created_at, w.id) <= (c.created_at, c.id))
		ELSE 0 END
		FROM claims c
		JOIN occurrences o ON o.id = c.occurrence_id
		JOIN slots sl ON sl.id = o.slot_id
		WHERE c.person_id = ? AND c.status <> 'cancelled'
		ORDER BY coalesce(o.date, ''), coalesce(o.start_time, ''), sl.title`, personID)
	if err != nil {
		return Mine{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var c MyClaim
		if err := rows.Scan(&c.ID, &c.OccurrenceID, &c.SlotTitle, &c.Date, &c.Start, &c.End,
			&c.Status, &c.Comment, &c.Position); err != nil {
			return Mine{}, err
		}
		m.Claims = append(m.Claims, c)
	}
	return m, rows.Err()
}
