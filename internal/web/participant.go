package web

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/hews/signup/internal/sheet"
)

type participant struct {
	db     *sql.DB
	logger *slog.Logger
	pages  pages
}

func (p participant) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /s/{slug}", p.sheetPage)
	mux.HandleFunc("GET /s/{slug}/claim", p.claimForm)
	mux.HandleFunc("POST /s/{slug}/claim", p.claim)
	mux.HandleFunc("GET /m/{token}", p.mine)
}

// sheetPage is what a participant sees: the sheet as it stands, with places still needed.
type sheetPage struct {
	Sheet     sheet.Sheet
	Sections  []sectionView
	Wanted    int  // places on slots with a limit
	Filled    int  // of those, taken
	Unlimited bool // some slot has no limit
	Open      bool
	Dates     string // "Oct 30" or "Oct 30 – Nov 2"
}

type sectionView struct {
	sheet.Section
	Needed int
	Slots  []slotView
}

type slotView struct {
	sheet.Slot
	Taken  sheet.Taken
	Needed int // 0 when full or unlimited
	Full   bool
}

// published loads the sheet a participant link opens, answering 404 itself for an unknown
// slug or a sheet not yet published.
func (p participant) published(w http.ResponseWriter, r *http.Request) (sheet.Sheet, bool) {
	s, err := sheet.BySlug(r.Context(), p.db, r.PathValue("slug"))
	if errors.Is(err, sheet.ErrNotFound) || (err == nil && s.Status == sheet.Draft) {
		p.notFound(w)
		return s, false
	}
	if err != nil {
		p.fail(w, r, err)
		return s, false
	}
	return s, true
}

func (p participant) notFound(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "No sheet here. Check the link, or ask whoever sent it.", http.StatusNotFound)
}

func (p participant) sheetPage(w http.ResponseWriter, r *http.Request) {
	s, ok := p.published(w, r)
	if !ok {
		return
	}
	taken, err := sheet.TakenBy(r.Context(), p.db, s)
	if err != nil {
		p.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	p.pages.render(w, http.StatusOK, "sheet", buildSheetPage(s, taken, today(s.TimeZone)))
}

// buildSheetPage works out what is still needed, hiding dates that have passed.
func buildSheetPage(s sheet.Sheet, taken map[int64]sheet.Taken, today string) sheetPage {
	pg := sheetPage{Sheet: s, Open: s.Status == sheet.Open}
	var first, last string
	for _, sec := range s.Sections {
		if sec.Date != "" && sec.Date < today {
			continue
		}
		if sec.Date != "" {
			if first == "" {
				first = sec.Date
			}
			last = sec.Date
		}
		sv := sectionView{Section: sec}
		for _, sl := range sec.Slots {
			v := slotView{Slot: sl, Taken: taken[sl.OccurrenceID]}
			if sl.Quantity > 0 {
				v.Needed = max(sl.Quantity-v.Taken.Count, 0)
				v.Full = v.Needed == 0
				pg.Wanted += sl.Quantity
				pg.Filled += min(v.Taken.Count, sl.Quantity)
				sv.Needed += v.Needed
			} else {
				pg.Unlimited = true
			}
			sv.Slots = append(sv.Slots, v)
		}
		pg.Sections = append(pg.Sections, sv)
	}
	switch {
	case first != "" && first != last:
		pg.Dates = fmtDate(first, "Jan 2") + " – " + fmtDate(last, "Jan 2")
	case first != "":
		pg.Dates = fmtDate(first, "Mon, Jan 2")
	}
	return pg
}

// today is the date in the sheet's time zone, so "past" means past where the event happens.
func today(zone string) string {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = time.UTC
	}
	return time.Now().In(loc).Format(time.DateOnly)
}

func (p participant) fail(w http.ResponseWriter, r *http.Request, err error) {
	p.logger.Error("request failed", "route", r.Pattern, "err", err)
	http.Error(w, "Something went wrong on our side. Please try again.", http.StatusInternalServerError)
}

// claimPage is the form for the slots a participant picked.
type claimPage struct {
	Sheet  sheet.Sheet
	Picks  []pickView
	Form   url.Values
	Errors sheet.Invalid
	// JustFilled is set when the last submit found some picks full.
	JustFilled bool
}

type pickView struct {
	sheet.Slot
	Date     string
	Comment  string
	Full     bool // filled while the form was open
	Waitlist bool // the person is offered, or has chosen, the waitlist
	Dropped  bool // full with no waitlist: it will not be included
}

// picked resolves the occurrence ids a participant sent to this sheet's slots that can still
// be chosen (not past), in sheet order. Ids from another sheet are ignored.
func picked(s sheet.Sheet, ids []string, today string) []pickView {
	want := map[int64]bool{}
	for _, v := range ids {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			want[id] = true
		}
	}
	var out []pickView
	for _, sec := range s.Sections {
		if sec.Date != "" && sec.Date < today {
			continue
		}
		for _, sl := range sec.Slots {
			if want[sl.OccurrenceID] {
				out = append(out, pickView{Slot: sl, Date: sec.Date})
			}
		}
	}
	return out
}

func (p participant) claimForm(w http.ResponseWriter, r *http.Request) {
	s, ok := p.published(w, r)
	if !ok {
		return
	}
	picks := picked(s, r.URL.Query()["o"], today(s.TimeZone))
	if len(picks) == 0 || s.Status != sheet.Open {
		http.Redirect(w, r, sharePath(s.Slug), http.StatusSeeOther)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	p.pages.render(w, http.StatusOK, "claim", claimPage{Sheet: s, Picks: picks, Form: url.Values{}})
}

func (p participant) claim(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	s, ok := p.published(w, r)
	if !ok {
		return
	}
	day := today(s.TimeZone)
	picks := picked(s, r.PostForm["o"], day)
	if len(picks) == 0 {
		http.Redirect(w, r, sharePath(s.Slug), http.StatusSeeOther)
		return
	}
	var in []sheet.Pick
	for i, pk := range picks {
		id := strconv.FormatInt(pk.OccurrenceID, 10)
		picks[i].Comment = r.PostForm.Get("comment_" + id)
		picks[i].Waitlist = r.PostForm.Get("waitlist_"+id) == "on"
		in = append(in, sheet.Pick{OccurrenceID: pk.OccurrenceID, Comment: picks[i].Comment, Waitlist: picks[i].Waitlist})
	}
	person := sheet.Person{First: r.PostForm.Get("first"), Last: r.PostForm.Get("last"),
		Phone: r.PostForm.Get("phone"), Email: r.PostForm.Get("email")}

	token, outcomes, err := sheet.Claim(r.Context(), p.db, s, day, person, in)
	var bad sheet.Invalid
	var full *sheet.FullError
	switch {
	case err == nil:
		waiting := 0
		for _, o := range outcomes {
			if o.Status == sheet.Waitlisted {
				waiting++
			}
		}
		p.logger.Info("claim made", "sheet", s.ID, "confirmed", len(outcomes)-waiting, "waitlisted", waiting)
		http.Redirect(w, r, minePath(token)+"?new=1", http.StatusSeeOther)
	case errors.As(err, &full):
		for _, id := range full.Occurrences {
			for i := range picks {
				if picks[i].OccurrenceID == id {
					picks[i].Full = true
					picks[i].Waitlist = full.Waitlist
					picks[i].Dropped = !full.Waitlist
				}
			}
		}
		// Anything that can no longer be had leaves the form; everything typed stays.
		kept := picks[:0]
		for _, pk := range picks {
			if !pk.Dropped {
				kept = append(kept, pk)
			}
		}
		dropped := len(picks) - len(kept)
		pg := claimPage{Sheet: s, Picks: kept, Form: r.PostForm, JustFilled: true}
		if dropped > 0 {
			pg.Errors = sheet.Invalid{"picks": "Some of what you chose filled up while you were typing and has no waitlist, so it has been taken out."}
		}
		if len(kept) == 0 {
			pg.Errors = sheet.Invalid{"picks": "Everything you chose filled up while you were typing. Go back to the sheet to choose again."}
		}
		w.Header().Set("Cache-Control", "no-store")
		p.pages.render(w, http.StatusConflict, "claim", pg)
	case errors.As(err, &bad):
		w.Header().Set("Cache-Control", "no-store")
		p.pages.render(w, http.StatusUnprocessableEntity, "claim", claimPage{Sheet: s, Picks: picks, Form: r.PostForm, Errors: bad})
	case errors.Is(err, sheet.ErrClosed):
		http.Redirect(w, r, sharePath(s.Slug), http.StatusSeeOther)
	case errors.Is(err, sheet.ErrNotFound):
		p.notFound(w)
	default:
		p.fail(w, r, err)
	}
}

// minePage is a person's own view of what they signed up for.
type minePage struct {
	sheet.Mine
	New       bool
	SharePath string
}

func (p participant) mine(w http.ResponseWriter, r *http.Request) {
	m, err := sheet.ByManageToken(r.Context(), p.db, r.PathValue("token"))
	if errors.Is(err, sheet.ErrNotFound) {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "This link has expired or does not exist. Ask the organiser if you need to change a sign-up.", http.StatusNotFound)
		return
	}
	if err != nil {
		p.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	p.pages.render(w, http.StatusOK, "mine", minePage{Mine: m, New: r.URL.Query().Get("new") == "1", SharePath: sharePath(m.Sheet.Slug)})
}

func minePath(token string) string { return "/m/" + url.PathEscape(token) }
