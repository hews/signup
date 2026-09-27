package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hews/signup/internal/db"
	"github.com/hews/signup/internal/logx"
)

func newServer(t *testing.T) (*httptest.Server, *bytes.Buffer) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	srv := httptest.NewServer(Handler(d, logx.New(&logs, slog.LevelDebug), ""))
	t.Cleanup(srv.Close)
	return srv, &logs
}

func TestHealthz(t *testing.T) {
	srv, _ := newServer(t)
	res, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("missing CSP, got %q", csp)
	}
	if got := res.Header.Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
		t.Fatalf("X-Robots-Tag = %q", got)
	}
}

func TestRobotsDisallowsEverything(t *testing.T) {
	srv, _ := newServer(t)
	res, err := http.Get(srv.URL + "/robots.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body := make([]byte, 64)
	n, _ := res.Body.Read(body)
	if got := string(body[:n]); !strings.Contains(got, "Disallow: /") {
		t.Fatalf("robots.txt = %q", got)
	}
}

func TestRequestLogCarriesNoQueryString(t *testing.T) {
	srv, logs := newServer(t)
	res, err := http.Get(srv.URL + "/healthz?phone=2165550142&who=jenny@example.com")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	out := logs.String()
	for _, leak := range []string{"2165550142", "jenny@example.com", "phone="} {
		if strings.Contains(out, leak) {
			t.Fatalf("request log leaked %q:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, `"route":"GET /healthz"`) {
		t.Fatalf("request log missing route pattern:\n%s", out)
	}
}
