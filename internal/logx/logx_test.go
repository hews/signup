package logx

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactsPersonalData(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelDebug)

	logger.Info("claim created",
		"claim", "clm_01J9X",
		"note", "call jenny@example.com or (216) 555-0142 tomorrow",
	)
	logger.With("contact", "+1 216 555 0142").Warn("delivery failed")

	out := buf.String()
	for _, leak := range []string{"jenny@example.com", "555-0142", "555 0142"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log output leaked %q:\n%s", leak, out)
		}
	}
	for _, want := range []string{"clm_01J9X", "[email]", "[phone]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log output missing %q:\n%s", want, out)
		}
	}
}

func TestRedactLeavesIdentifiersAlone(t *testing.T) {
	for _, s := range []string{"sheet_k8x2", "2026-09-27T10:00:00Z", "2026-09-27 10:00", "route=/s/k8x2", "42", "ms=1234"} {
		if got := Redact(s); got != s {
			t.Errorf("Redact(%q) = %q, want unchanged", s, got)
		}
	}
}
