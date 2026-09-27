// Package web wires HTTP routes.
package web

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Handler returns the application's root handler.
func Handler(d *sql.DB, logger *slog.Logger) http.Handler {
	p := loadPages()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz(d))
	mux.HandleFunc("GET /robots.txt", robots)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		p.render(w, http.StatusOK, "home", nil)
	})
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", cacheFor(time.Hour, http.StripPrefix("/static/", http.FileServerFS(static))))
	organiser{db: d, logger: logger, pages: p}.routes(mux)
	// Refuse cross-site form posts: browsers mark them with Sec-Fetch-Site / Origin.
	return requestLog(logger, secureHeaders(http.NewCrossOriginProtection().Handler(mux)))
}

// pages holds one parsed template set per page, each wrapped in the shared layout.
type pages map[string]*template.Template

var templateFuncs = template.FuncMap{
	"fmtDay":      func(d string) string { return fmtDate(d, "Mon") },
	"fmtDate":     func(d string) string { return fmtDate(d, "Jan 2") },
	"fmtSlotTime": fmtSlotTime,
}

func loadPages() pages {
	p := pages{}
	for _, name := range []string{"home", "new", "manage"} {
		p[name] = template.Must(template.New("").Funcs(templateFuncs).ParseFS(templateFS,
			"templates/layout.html", "templates/"+name+".html"))
	}
	return p
}

// render writes a page, buffering it first so a template error never sends half a page.
func (p pages) render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := p[name].ExecuteTemplate(&buf, "layout", data); err != nil {
		http.Error(w, "Something went wrong on our side. Please try again.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

func fmtDate(d, layout string) string {
	t, err := time.Parse(time.DateOnly, d)
	if err != nil {
		return d
	}
	return t.Format(layout)
}

// fmtSlotTime renders "1:00 – 2:30 PM", "1:00 PM" or "" for a slot's optional times.
func fmtSlotTime(start, end string) string {
	s, err := time.Parse("15:04", start)
	if err != nil {
		return ""
	}
	e, err := time.Parse("15:04", end)
	if err != nil {
		return s.Format("3:04 PM")
	}
	if s.Format("PM") == e.Format("PM") {
		return fmt.Sprintf("%s – %s", s.Format("3:04"), e.Format("3:04 PM"))
	}
	return fmt.Sprintf("%s – %s", s.Format("3:04 PM"), e.Format("3:04 PM"))
}

func cacheFor(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(d.Seconds())))
		next.ServeHTTP(w, r)
	})
}

// robots tells every crawler to stay out. Sheets are unlisted by design and
// every response also carries X-Robots-Tag.
func robots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("User-agent: *\nDisallow: /\n"))
}

// healthz reports liveness and that the database answers.
func healthz(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := d.PingContext(ctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	}
}

// secureHeaders sets the headers every response carries. No third-party
// origins are ever allowed by the content security policy.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Robots-Tag", "noindex, nofollow, noarchive, noimageindex")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// requestLog logs one line per request: method, route pattern, status,
// duration. Never the query string or body, which may carry personal data.
func requestLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		logger.Info("request",
			"method", r.Method,
			"route", r.Pattern,
			"status", sw.status,
			"ms", time.Since(start).Milliseconds(),
		)
	})
}
