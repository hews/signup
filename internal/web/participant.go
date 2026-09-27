package web

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
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

func (p participant) sheetPage(w http.ResponseWriter, r *http.Request) {
	s, err := sheet.BySlug(r.Context(), p.db, r.PathValue("slug"))
	if errors.Is(err, sheet.ErrNotFound) || (err == nil && s.Status == sheet.Draft) {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "No sheet here. Check the link, or ask whoever sent it.", http.StatusNotFound)
		return
	}
	if err != nil {
		p.fail(w, r, err)
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
