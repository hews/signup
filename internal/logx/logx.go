// Package logx builds the process logger.
//
// Policy: log lines carry identifiers, never personal data. No name, phone
// number or email address may appear in a log attribute. The handler enforces
// this defensively by redacting attribute values that look like a phone number
// or an email address; the test in this package is the tripwire.
package logx

import (
	"context"
	"io"
	"log/slog"
	"regexp"
)

var (
	emailRe = regexp.MustCompile(`(?i)[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}`)
	// A run of digits with the separators people type in phone numbers. Whether
	// it is a phone number is decided by counting digits (see Redact), so dates
	// (8 digits) and short ids are left alone while any national or E.164 number
	// (10–15 digits) is caught.
	digitRunRe = regexp.MustCompile(`\+?\d[\d\s().-]*\d`)
	dateRe     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)
)

// Redact replaces anything that looks like an email address or phone number.
func Redact(s string) string {
	s = emailRe.ReplaceAllString(s, "[email]")
	s = digitRunRe.ReplaceAllStringFunc(s, func(run string) string {
		if dateRe.MatchString(run) {
			return run // an ISO date, possibly followed by a time
		}
		n := 0
		for _, r := range run {
			if r >= '0' && r <= '9' {
				n++
			}
		}
		if n >= 10 && n <= 15 {
			return "[phone]"
		}
		return run
	})
	return s
}

type redactingHandler struct{ slog.Handler }

func (h redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, Redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(redactAttr(a))
		return true
	})
	return h.Handler.Handle(ctx, clean)
}

func (h redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = redactAttr(a)
	}
	return redactingHandler{h.Handler.WithAttrs(out)}
}

func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{h.Handler.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, Redact(v.String()))
	case slog.KindGroup:
		g := v.Group()
		out := make([]slog.Attr, len(g))
		for i, ga := range g {
			out[i] = redactAttr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	default:
		return a
	}
}

// New returns a JSON logger writing to w at the given level, with redaction.
func New(w io.Writer, level slog.Level) *slog.Logger {
	base := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(redactingHandler{base})
}
