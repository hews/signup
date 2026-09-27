// Package sheet stores sign-up sheets: their sections, slots and occurrences, and the admin
// link an organiser uses to manage one. Every function here is scoped to one sheet; nothing
// reads across sheets.
package sheet

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Formats a sheet can take in M1.
const (
	ByDate    = "by_date"
	SlotsOnly = "slots_only"
)

// Statuses a sheet moves through.
const (
	Draft  = "draft"
	Open   = "open"
	Closed = "closed"
)

// Length limits for free text, enforced before anything is stored.
const (
	maxTitle       = 120
	maxDescription = 2000
	maxShort       = 120
	maxQuantity    = 10000
)

var (
	// ErrNotFound means no sheet matches the token or slug, or the row is not on this sheet.
	ErrNotFound = errors.New("not found")
	// ErrHasClaims means a slot or date cannot be removed because people have signed up to it.
	ErrHasClaims = errors.New("people have signed up")
)

// Invalid reports input that failed validation, field by field, in words a person can act on.
type Invalid map[string]string

func (v Invalid) Error() string { return fmt.Sprintf("invalid input: %d field(s)", len(v)) }

// Basics are what an organiser types before a sheet has any slots.
type Basics struct {
	Title         string
	Description   string
	OrganizerName string
	Location      string
	TimeZone      string
	Format        string
}

// Sheet is a sheet with everything under it, in display order.
type Sheet struct {
	ID            int64
	Slug          string
	Title         string
	Description   string
	OrganizerName string
	Location      string
	TimeZone      string
	Format        string
	Status        string
	Sections      []Section
}

// Section is a date (by_date) or a heading (slots_only).
type Section struct {
	ID    int64
	Title string
	Date  string // YYYY-MM-DD, by_date only
	Slots []Slot
}

// Slot is one row people sign up to. In M1 each slot has exactly one occurrence.
type Slot struct {
	ID           int64
	OccurrenceID int64
	Title        string
	Description  string
	Quantity     int    // 0 means unlimited
	Start, End   string // HH:MM, optional
}

// SlotInput is what an organiser types to add a slot.
type SlotInput struct {
	Title       string
	Description string
	Quantity    int // 0 means unlimited
	Start, End  string
}

// Slots reports how many slots the sheet has in all.
func (s Sheet) Slots() int {
	n := 0
	for _, sec := range s.Sections {
		n += len(sec.Slots)
	}
	return n
}

// Create validates the basics and stores a draft sheet. It returns the sheet and the admin
// token; the token is shown to the organiser once and only its hash is kept.
func Create(ctx context.Context, d *sql.DB, b Basics) (Sheet, string, error) {
	b = b.trimmed()
	if err := b.validate(); err != nil {
		return Sheet{}, "", err
	}
	token, hash, err := NewToken()
	if err != nil {
		return Sheet{}, "", err
	}
	slug, err := newSlug()
	if err != nil {
		return Sheet{}, "", err
	}
	res, err := d.ExecContext(ctx, `INSERT INTO sheets
		(slug, admin_token_hash, title, description, organizer_name, location, time_zone, format)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		slug, hash, b.Title, b.Description, b.OrganizerName, b.Location, b.TimeZone, b.Format)
	if err != nil {
		return Sheet{}, "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Sheet{}, "", err
	}
	return Sheet{ID: id, Slug: slug, Title: b.Title, Description: b.Description,
		OrganizerName: b.OrganizerName, Location: b.Location, TimeZone: b.TimeZone,
		Format: b.Format, Status: Draft}, token, nil
}

// ByAdminToken loads the sheet an admin token opens.
func ByAdminToken(ctx context.Context, d *sql.DB, token string) (Sheet, error) {
	return load(ctx, d, `admin_token_hash = ?`, HashToken(token))
}

// BySlug loads the sheet a participant link opens.
func BySlug(ctx context.Context, d *sql.DB, slug string) (Sheet, error) {
	return load(ctx, d, `slug = ?`, slug)
}

func load(ctx context.Context, d *sql.DB, where string, arg any) (Sheet, error) {
	var s Sheet
	err := d.QueryRowContext(ctx, `SELECT id, slug, title, description, organizer_name, location,
		time_zone, format, status FROM sheets WHERE `+where, arg).Scan(
		&s.ID, &s.Slug, &s.Title, &s.Description, &s.OrganizerName, &s.Location,
		&s.TimeZone, &s.Format, &s.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return Sheet{}, ErrNotFound
	}
	if err != nil {
		return Sheet{}, err
	}
	rows, err := d.QueryContext(ctx, `SELECT se.id, se.title, coalesce(se.date, ''),
		sl.id, o.id, sl.title, sl.description, coalesce(sl.quantity_wanted, 0),
		coalesce(o.start_time, ''), coalesce(o.end_time, '')
		FROM sections se
		LEFT JOIN slots sl ON sl.section_id = se.id
		LEFT JOIN occurrences o ON o.slot_id = sl.id
		WHERE se.sheet_id = ?
		ORDER BY coalesce(se.date, ''), se.position, coalesce(o.start_time, ''), sl.position`, s.ID)
	if err != nil {
		return Sheet{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var sec Section
		var slotID, occID sql.NullInt64
		var slot Slot
		var title, desc, start, end sql.NullString
		var qty sql.NullInt64
		if err := rows.Scan(&sec.ID, &sec.Title, &sec.Date, &slotID, &occID,
			&title, &desc, &qty, &start, &end); err != nil {
			return Sheet{}, err
		}
		if n := len(s.Sections); n == 0 || s.Sections[n-1].ID != sec.ID {
			s.Sections = append(s.Sections, sec)
		}
		if slotID.Valid {
			slot = Slot{ID: slotID.Int64, OccurrenceID: occID.Int64, Title: title.String,
				Description: desc.String, Quantity: int(qty.Int64), Start: start.String, End: end.String}
			last := &s.Sections[len(s.Sections)-1]
			last.Slots = append(last.Slots, slot)
		}
	}
	return s, rows.Err()
}

// AddSection adds a date (by_date) or a heading (slots_only) to the end of the sheet.
func AddSection(ctx context.Context, d *sql.DB, s Sheet, title, date string) error {
	title, date = strings.TrimSpace(title), strings.TrimSpace(date)
	bad := Invalid{}
	switch s.Format {
	case ByDate:
		if _, err := time.Parse(time.DateOnly, date); err != nil {
			bad["date"] = "Choose a date."
		}
		for _, sec := range s.Sections {
			if sec.Date == date {
				bad["date"] = "That date is already on the sheet."
			}
		}
		title = ""
	default:
		if title == "" {
			bad["title"] = "Give the heading a name."
		}
		date = ""
	}
	if utf8.RuneCountInString(title) > maxShort {
		bad["title"] = fmt.Sprintf("Keep it to %d characters or fewer.", maxShort)
	}
	if len(bad) > 0 {
		return bad
	}
	_, err := d.ExecContext(ctx, `INSERT INTO sections (sheet_id, position, title, date)
		VALUES (?, (SELECT coalesce(max(position), -1) + 1 FROM sections WHERE sheet_id = ?), ?, nullif(?, ''))`,
		s.ID, s.ID, title, date)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: sections.sheet_id, sections.date") {
		return Invalid{"date": "That date is already on the sheet."}
	}
	return err
}

// AddSlot adds a slot, with its one occurrence, to a section of this sheet.
func AddSlot(ctx context.Context, d *sql.DB, s Sheet, sectionID int64, in SlotInput) error {
	sec, ok := s.section(sectionID)
	if !ok {
		return ErrNotFound
	}
	in.Title, in.Description = strings.TrimSpace(in.Title), strings.TrimSpace(in.Description)
	in.Start, in.End = strings.TrimSpace(in.Start), strings.TrimSpace(in.End)
	bad := Invalid{}
	if in.Title == "" {
		bad["slot_title"] = "Name the slot, e.g. “Crafts table”."
	} else if utf8.RuneCountInString(in.Title) > maxShort {
		bad["slot_title"] = fmt.Sprintf("Keep it to %d characters or fewer.", maxShort)
	}
	if utf8.RuneCountInString(in.Description) > maxDescription {
		bad["slot_description"] = fmt.Sprintf("Keep it to %d characters or fewer.", maxDescription)
	}
	if in.Quantity < 0 || in.Quantity > maxQuantity {
		bad["quantity"] = fmt.Sprintf("Between 1 and %d, or leave blank for no limit.", maxQuantity)
	}
	if in.Start != "" && !validTime(in.Start) {
		bad["start"] = "Use a time like 13:00."
	}
	if in.End != "" && (!validTime(in.End) || in.Start == "" || in.End == in.Start) {
		bad["end"] = "Needs a start time, and must differ from it."
	}
	if len(bad) > 0 {
		return bad
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var qty any
	if in.Quantity > 0 {
		qty = in.Quantity
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO slots (section_id, position, title, description, quantity_wanted)
		VALUES (?, (SELECT coalesce(max(position), -1) + 1 FROM slots WHERE section_id = ?), ?, ?, ?)`,
		sec.ID, sec.ID, in.Title, in.Description, qty)
	if err != nil {
		return err
	}
	slotID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO occurrences (slot_id, date, start_time, end_time)
		VALUES (?, nullif(?, ''), nullif(?, ''), nullif(?, ''))`, slotID, sec.Date, in.Start, in.End); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveSlot deletes a slot of this sheet, refusing while anyone holds a place on it.
func RemoveSlot(ctx context.Context, d *sql.DB, s Sheet, slotID int64) error {
	if !s.hasSlot(slotID) {
		return ErrNotFound
	}
	return removeUnclaimed(ctx, d, s.ID, `DELETE FROM slots WHERE id = ?`, slotID,
		`SELECT count(*) FROM claims c JOIN occurrences o ON o.id = c.occurrence_id
		 WHERE o.slot_id = ? AND c.status <> 'cancelled'`)
}

// RemoveSection deletes a date or heading of this sheet and its slots, refusing while anyone
// holds a place on one of them.
func RemoveSection(ctx context.Context, d *sql.DB, s Sheet, sectionID int64) error {
	if _, ok := s.section(sectionID); !ok {
		return ErrNotFound
	}
	return removeUnclaimed(ctx, d, s.ID, `DELETE FROM sections WHERE id = ?`, sectionID,
		`SELECT count(*) FROM claims c JOIN occurrences o ON o.id = c.occurrence_id
		 JOIN slots sl ON sl.id = o.slot_id WHERE sl.section_id = ? AND c.status <> 'cancelled'`)
}

func removeUnclaimed(ctx context.Context, d *sql.DB, sheetID int64, del string, id int64, count string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, count, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrHasClaims
	}
	if _, err := tx.ExecContext(ctx, del, id); err != nil {
		return err
	}
	// The cascade took any cancelled claims with it; nobody's details stay without a claim.
	if _, err := tx.ExecContext(ctx, `DELETE FROM people WHERE sheet_id = ?
		AND NOT EXISTS (SELECT 1 FROM claims WHERE claims.person_id = people.id)`, sheetID); err != nil {
		return err
	}
	return tx.Commit()
}

// Publish opens a draft sheet to sign-ups. A sheet needs at least one slot first; publishing
// an open sheet again changes nothing, and a closed sheet stays closed.
func Publish(ctx context.Context, d *sql.DB, s Sheet) error {
	switch s.Status {
	case Open:
		return nil
	case Closed:
		return Invalid{"publish": "This sheet is closed."}
	}
	if s.Slots() == 0 {
		return Invalid{"publish": "Add at least one slot first."}
	}
	_, err := d.ExecContext(ctx, `UPDATE sheets SET status = 'open',
		published_at = coalesce(published_at, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ? AND status = 'draft'`, s.ID)
	return err
}

func (s Sheet) section(id int64) (Section, bool) {
	for _, sec := range s.Sections {
		if sec.ID == id {
			return sec, true
		}
	}
	return Section{}, false
}

func (s Sheet) hasSlot(id int64) bool {
	for _, sec := range s.Sections {
		for _, sl := range sec.Slots {
			if sl.ID == id {
				return true
			}
		}
	}
	return false
}

func (b Basics) trimmed() Basics {
	b.Title = strings.TrimSpace(b.Title)
	b.Description = strings.TrimSpace(b.Description)
	b.OrganizerName = strings.TrimSpace(b.OrganizerName)
	b.Location = strings.TrimSpace(b.Location)
	b.TimeZone = strings.TrimSpace(b.TimeZone)
	return b
}

func (b Basics) validate() error {
	bad := Invalid{}
	if b.Title == "" {
		bad["title"] = "Give the sheet a title."
	} else if utf8.RuneCountInString(b.Title) > maxTitle {
		bad["title"] = fmt.Sprintf("Keep it to %d characters or fewer.", maxTitle)
	}
	if utf8.RuneCountInString(b.Description) > maxDescription {
		bad["description"] = fmt.Sprintf("Keep it to %d characters or fewer.", maxDescription)
	}
	if utf8.RuneCountInString(b.OrganizerName) > maxShort {
		bad["organizer_name"] = fmt.Sprintf("Keep it to %d characters or fewer.", maxShort)
	}
	if utf8.RuneCountInString(b.Location) > maxShort {
		bad["location"] = fmt.Sprintf("Keep it to %d characters or fewer.", maxShort)
	}
	if _, err := time.LoadLocation(b.TimeZone); b.TimeZone == "" || b.TimeZone == "Local" || err != nil {
		bad["time_zone"] = "Choose a time zone."
	}
	if b.Format != ByDate && b.Format != SlotsOnly {
		bad["format"] = "Choose how the sheet is laid out."
	}
	if len(bad) > 0 {
		return bad
	}
	return nil
}

func validTime(s string) bool {
	_, err := time.Parse("15:04", s)
	return err == nil && len(s) == 5
}

// NewToken returns a random token for a link and the SHA-256 hash that is stored instead.
func NewToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken is the stored form of a link token.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// newSlug returns an unguessable, lower-case slug for a participant link: 80 random bits.
func newSlug() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)), nil
}
