package web

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hews/signup/internal/sheet"
)

// timeZones offered when creating a sheet, most likely first. Any IANA name is accepted.
var timeZones = []string{
	"America/New_York", "America/Chicago", "America/Denver", "America/Phoenix",
	"America/Los_Angeles", "America/Anchorage", "Pacific/Honolulu", "America/Puerto_Rico",
	"Europe/London", "Europe/Dublin", "Europe/Paris", "Europe/Berlin", "Asia/Kolkata",
	"Asia/Tokyo", "Australia/Sydney", "Pacific/Auckland", "UTC",
}

// organiserPage is everything the organiser templates read.
type organiserPage struct {
	Sheet       sheet.Sheet
	Form        url.Values
	Errors      sheet.Invalid
	TimeZones   []string
	AdminPath   string
	SharePath   string
	ShareURL    string
	OpenSection int64
}

type organiser struct {
	db      *sql.DB
	logger  *slog.Logger
	pages   pages
	baseURL string
}

func (o organiser) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /new", o.newForm)
	mux.HandleFunc("POST /new", o.create)
	mux.HandleFunc("GET /o/{token}", o.manage)
	mux.HandleFunc("POST /o/{token}/sections", o.addSection)
	mux.HandleFunc("POST /o/{token}/sections/{id}/slots", o.addSlot)
	mux.HandleFunc("POST /o/{token}/sections/{id}/remove", o.removeSection)
	mux.HandleFunc("POST /o/{token}/slots/{id}/remove", o.removeSlot)
	mux.HandleFunc("POST /o/{token}/publish", o.publish)
}

func (o organiser) newForm(w http.ResponseWriter, r *http.Request) {
	o.pages.render(w, http.StatusOK, "new", organiserPage{
		Form: url.Values{"time_zone": {timeZones[0]}}, TimeZones: timeZones})
}

func (o organiser) create(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	s, token, err := sheet.Create(r.Context(), o.db, sheet.Basics{
		Title:         r.PostForm.Get("title"),
		Description:   r.PostForm.Get("description"),
		OrganizerName: r.PostForm.Get("organizer_name"),
		Location:      r.PostForm.Get("location"),
		TimeZone:      r.PostForm.Get("time_zone"),
		Format:        r.PostForm.Get("format"),
	})
	var bad sheet.Invalid
	if errors.As(err, &bad) {
		o.pages.render(w, http.StatusUnprocessableEntity, "new", organiserPage{
			Form: r.PostForm, Errors: bad, TimeZones: timeZones})
		return
	}
	if err != nil {
		o.fail(w, r, err)
		return
	}
	o.logger.Info("sheet created", "sheet", s.ID, "format", s.Format)
	http.Redirect(w, r, adminPath(token), http.StatusSeeOther)
}

func (o organiser) manage(w http.ResponseWriter, r *http.Request) {
	s, ok := o.sheet(w, r)
	if !ok {
		return
	}
	o.show(w, r, http.StatusOK, s, nil, nil, 0)
}

func (o organiser) addSection(w http.ResponseWriter, r *http.Request) {
	s, ok := o.sheetForPost(w, r)
	if !ok {
		return
	}
	err := sheet.AddSection(r.Context(), o.db, s, r.PostForm.Get("title"), r.PostForm.Get("date"))
	o.after(w, r, s, err, 0)
}

func (o organiser) addSlot(w http.ResponseWriter, r *http.Request) {
	s, ok := o.sheetForPost(w, r)
	if !ok {
		return
	}
	id := pathID(r)
	qty := 0
	if v := strings.TrimSpace(r.PostForm.Get("quantity")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			n = -1
		}
		qty = n
	}
	err := sheet.AddSlot(r.Context(), o.db, s, id, sheet.SlotInput{
		Title:       r.PostForm.Get("slot_title"),
		Description: r.PostForm.Get("slot_description"),
		Quantity:    qty,
		Start:       r.PostForm.Get("start"),
		End:         r.PostForm.Get("end"),
	})
	o.after(w, r, s, err, id)
}

func (o organiser) removeSection(w http.ResponseWriter, r *http.Request) {
	s, ok := o.sheetForPost(w, r)
	if !ok {
		return
	}
	o.after(w, r, s, sheet.RemoveSection(r.Context(), o.db, s, pathID(r)), 0)
}

func (o organiser) removeSlot(w http.ResponseWriter, r *http.Request) {
	s, ok := o.sheetForPost(w, r)
	if !ok {
		return
	}
	o.after(w, r, s, sheet.RemoveSlot(r.Context(), o.db, s, pathID(r)), 0)
}

func (o organiser) publish(w http.ResponseWriter, r *http.Request) {
	s, ok := o.sheetForPost(w, r)
	if !ok {
		return
	}
	err := sheet.Publish(r.Context(), o.db, s)
	if err == nil && s.Status == sheet.Draft {
		o.logger.Info("sheet published", "sheet", s.ID)
	}
	o.after(w, r, s, err, 0)
}

// after finishes a change: back to the manage page on success (so a reload never repeats
// the change), or the same page with the problem shown.
func (o organiser) after(w http.ResponseWriter, r *http.Request, s sheet.Sheet, err error, openSection int64) {
	var bad sheet.Invalid
	switch {
	case err == nil:
		http.Redirect(w, r, adminPath(r.PathValue("token")), http.StatusSeeOther)
	case errors.As(err, &bad):
		o.show(w, r, http.StatusUnprocessableEntity, s, r.PostForm, bad, openSection)
	case errors.Is(err, sheet.ErrHasClaims):
		o.show(w, r, http.StatusConflict, s, nil,
			sheet.Invalid{"remove": "People have signed up to that, so it stays. Move or remove them first."}, 0)
	case errors.Is(err, sheet.ErrNotFound):
		o.notFound(w)
	default:
		o.fail(w, r, err)
	}
}

func (o organiser) show(w http.ResponseWriter, r *http.Request, status int, s sheet.Sheet, form url.Values, bad sheet.Invalid, openSection int64) {
	if form == nil {
		form = url.Values{}
	}
	token := r.PathValue("token")
	w.Header().Set("Cache-Control", "no-store")
	o.pages.render(w, status, "manage", organiserPage{
		Sheet: s, Form: form, Errors: bad, OpenSection: openSection,
		AdminPath: adminPath(token), SharePath: sharePath(s.Slug), ShareURL: o.linkBase(r) + sharePath(s.Slug),
	})
}

// sheet loads the sheet the admin token in the path opens, or answers 404.
func (o organiser) sheet(w http.ResponseWriter, r *http.Request) (sheet.Sheet, bool) {
	s, err := sheet.ByAdminToken(r.Context(), o.db, r.PathValue("token"))
	if errors.Is(err, sheet.ErrNotFound) {
		o.notFound(w)
		return s, false
	}
	if err != nil {
		o.fail(w, r, err)
		return s, false
	}
	return s, true
}

func (o organiser) sheetForPost(w http.ResponseWriter, r *http.Request) (sheet.Sheet, bool) {
	if !parseForm(w, r) {
		return sheet.Sheet{}, false
	}
	return o.sheet(w, r)
}

func (o organiser) notFound(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "No sheet here. Check the link, or ask whoever sent it.", http.StatusNotFound)
}

func (o organiser) fail(w http.ResponseWriter, r *http.Request, err error) {
	o.logger.Error("request failed", "route", r.Pattern, "err", err)
	http.Error(w, "Something went wrong on our side. Please try again.", http.StatusInternalServerError)
}

// parseForm reads a small form body, answering 400 itself when it cannot.
func parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form could not be read.", http.StatusBadRequest)
		return false
	}
	return true
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func adminPath(token string) string { return "/o/" + url.PathEscape(token) }
func sharePath(slug string) string  { return "/s/" + url.PathEscape(slug) }

// linkBase is the scheme and host for links people copy: the configured base URL, else the
// one this request arrived on (trusting the proxy's Host and X-Forwarded-Proto).
func (o organiser) linkBase(r *http.Request) string {
	if o.baseURL != "" {
		return o.baseURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
