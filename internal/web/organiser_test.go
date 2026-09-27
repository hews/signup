package web

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var addSlotAction = regexp.MustCompile(`action="(/o/[^"]+/sections/(\d+))/slots"`)

// firstSection finds the first add-slot form on a manage page: its section path and id.
func firstSection(t *testing.T, page string) (path, id string) {
	t.Helper()
	m := addSlotAction.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no add-slot form on the page")
	}
	return m[1], m[2]
}

// noRedirect is a client that reports redirects instead of following them.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

func post(t *testing.T, target string, form url.Values) *http.Response {
	t.Helper()
	res, err := noRedirect.PostForm(target, form)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func get(t *testing.T, target string) (*http.Response, string) {
	t.Helper()
	res, err := noRedirect.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func body(t *testing.T, res *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(res.Body)
	return string(b)
}

func wantStatus(t *testing.T, res *http.Response, want int) {
	t.Helper()
	if res.StatusCode != want {
		t.Fatalf("%s %s: status = %d, want %d", res.Request.Method, res.Request.URL.Path, res.StatusCode, want)
	}
}

// createSheet posts the basics and returns the admin path it redirects to.
func createSheet(t *testing.T, base, format string) string {
	t.Helper()
	res := post(t, base+"/new", url.Values{"title": {"Class party helpers"}, "organizer_name": {"Ms. Alvarez"},
		"time_zone": {"America/New_York"}, "format": {format}})
	wantStatus(t, res, http.StatusSeeOther)
	admin := res.Header.Get("Location")
	if !strings.HasPrefix(admin, "/o/") || len(admin) < 40 {
		t.Fatalf("redirect = %q, want an admin link", admin)
	}
	return admin
}

func TestOrganiserBuildsAndPublishesASheet(t *testing.T) {
	srv, logs := newServer(t)
	res, page := get(t, srv.URL+"/new")
	wantStatus(t, res, http.StatusOK)
	if !strings.Contains(page, `name="title"`) {
		t.Fatal("new sheet form missing")
	}

	admin := createSheet(t, srv.URL, "by_date")
	res, page = get(t, srv.URL+admin)
	wantStatus(t, res, http.StatusOK)
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("manage page Cache-Control = %q, want no-store", res.Header.Get("Cache-Control"))
	}
	for _, want := range []string{"Class party helpers", "is your key", "Add a date", "disabled"} {
		if !strings.Contains(page, want) {
			t.Errorf("manage page missing %q", want)
		}
	}

	wantStatus(t, post(t, srv.URL+admin+"/sections", url.Values{"date": {"2026-10-30"}}), http.StatusSeeOther)
	_, page = get(t, srv.URL+admin)
	sectionPath, _ := firstSection(t, page)
	wantStatus(t, post(t, srv.URL+sectionPath+"/slots", url.Values{"slot_title": {"Crafts table"},
		"quantity": {"3"}, "start": {"13:00"}, "end": {"14:30"}}), http.StatusSeeOther)

	_, page = get(t, srv.URL+admin)
	for _, want := range []string{"Oct 30", "Crafts table", "1:00 – 2:30 PM", "3 wanted", "1 slot ready"} {
		if !strings.Contains(page, want) {
			t.Errorf("manage page missing %q after adding a slot", want)
		}
	}

	wantStatus(t, post(t, srv.URL+admin+"/publish", nil), http.StatusSeeOther)
	_, page = get(t, srv.URL+admin)
	if !strings.Contains(page, "Share this link") || !strings.Contains(page, srv.URL+"/s/") {
		t.Fatal("published sheet does not show its share link")
	}

	token := strings.TrimPrefix(admin, "/o/")
	if out := logs.String(); strings.Contains(out, token) || strings.Contains(out, "Class party") {
		t.Fatalf("logs carry the admin token or sheet text:\n%s", out)
	}
}

func TestOrganiserSeesProblemsInPlace(t *testing.T) {
	srv, _ := newServer(t)
	res := post(t, srv.URL+"/new", url.Values{"title": {"  "}, "time_zone": {"UTC"}, "format": {"by_date"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if page := body(t, res); !strings.Contains(page, "Give the sheet a title.") {
		t.Fatal("missing title error")
	}

	admin := createSheet(t, srv.URL, "by_date")
	res = post(t, srv.URL+admin+"/publish", nil)
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if page := body(t, res); !strings.Contains(page, "Add at least one slot first.") {
		t.Fatal("missing publish error")
	}
	res = post(t, srv.URL+admin+"/sections", url.Values{"date": {"not a date"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
}

func TestAdminLinksAreTheOnlyWayIn(t *testing.T) {
	srv, _ := newServer(t)
	admin := createSheet(t, srv.URL, "slots_only")
	res, _ := get(t, srv.URL+admin+"x")
	wantStatus(t, res, http.StatusNotFound)
	wantStatus(t, post(t, srv.URL+"/o/not-a-token/sections", url.Values{"title": {"Bring"}}), http.StatusNotFound)

	// Another sheet's section id, posted through this sheet's admin link, is refused.
	other := createSheet(t, srv.URL, "slots_only")
	wantStatus(t, post(t, srv.URL+other+"/sections", url.Values{"title": {"Theirs"}}), http.StatusSeeOther)
	_, page := get(t, srv.URL+other)
	_, id := firstSection(t, page)
	wantStatus(t, post(t, srv.URL+admin+"/sections/"+id+"/slots", url.Values{"slot_title": {"Intruder"}}), http.StatusNotFound)
	wantStatus(t, post(t, srv.URL+admin+"/sections/"+id+"/remove", nil), http.StatusNotFound)
}

func TestCrossSitePostsAreRefused(t *testing.T) {
	srv, _ := newServer(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/new", strings.NewReader("title=x&time_zone=UTC&format=by_date"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	res, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	wantStatus(t, res, http.StatusForbidden)
}

func TestSheetTextIsEscaped(t *testing.T) {
	srv, _ := newServer(t)
	res := post(t, srv.URL+"/new", url.Values{"title": {`<script>alert(1)</script>`},
		"time_zone": {"UTC"}, "format": {"slots_only"}})
	wantStatus(t, res, http.StatusSeeOther)
	_, page := get(t, srv.URL+res.Header.Get("Location"))
	if strings.Contains(page, "<script>alert") {
		t.Fatal("title rendered unescaped")
	}
}

func TestStaticAssetsAreServed(t *testing.T) {
	srv, _ := newServer(t)
	for _, f := range []string{"/static/tokens.css", "/static/app.css"} {
		res, css := get(t, srv.URL+f)
		wantStatus(t, res, http.StatusOK)
		if !strings.Contains(res.Header.Get("Content-Type"), "text/css") || len(css) < 100 {
			t.Errorf("%s: type %q, %d bytes", f, res.Header.Get("Content-Type"), len(css))
		}
	}
}

func TestFmtSlotTime(t *testing.T) {
	for _, c := range []struct{ start, end, want string }{
		{"13:00", "14:30", "1:00 – 2:30 PM"},
		{"11:30", "13:00", "11:30 AM – 1:00 PM"},
		{"09:00", "", "9:00 AM"},
		{"", "", ""},
	} {
		if got := fmtSlotTime(c.start, c.end); got != c.want {
			t.Errorf("fmtSlotTime(%q, %q) = %q, want %q", c.start, c.end, got, c.want)
		}
	}
}
