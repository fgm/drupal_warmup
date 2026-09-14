package warm_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fgm/drupal_warmup/internal/sources"
	"github.com/fgm/drupal_warmup/internal/warm"
)

// site counts requests per path and tracks how many are in flight.
type site struct {
	// configuration
	delay     time.Duration
	failFirst bool
	redirects map[string]string

	// mutable state
	hits      map[string]int
	inFlight  atomic.Int64
	maxAtOnce atomic.Int64
	mu        sync.Mutex
	// duringFirst counts requests that started while the very first one was in flight.
	duringFirst atomic.Int64
	firstDone   atomic.Bool
	started     atomic.Int64
}

func (s *site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	nth := s.started.Add(1)
	if nth > 1 && !s.firstDone.Load() {
		s.duringFirst.Add(1)
	}
	n := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	for {
		m := s.maxAtOnce.Load()
		if n <= m || s.maxAtOnce.CompareAndSwap(m, n) {
			break
		}
	}
	s.mu.Lock()
	s.hits[r.URL.Path]++
	count := s.hits[r.URL.Path]
	s.mu.Unlock()
	time.Sleep(s.delay)
	if nth == 1 {
		s.firstDone.Store(true)
	}
	if to, ok := s.redirects[r.URL.Path]; ok {
		http.Redirect(w, r, to, http.StatusMovedPermanently)
		return
	}
	if s.failFirst && count == 1 {
		http.Error(w, "cold", http.StatusInternalServerError)
		return
	}
	if r.URL.Path == "/missing" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/truncated" {
		// A body shorter than announced: the client's read fails with an unexpected EOF.
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
		return
	}
	if r.URL.Path != "/no-header" {
		w.Header().Set("X-Main", "main-value")
		w.Header().Set("X-Anon", "anon-value")
	}
	_, _ = w.Write([]byte("page"))
}

func (s *site) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func newSite(t *testing.T, s *site) *httptest.Server {
	t.Helper()
	s.hits = map[string]int{}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return srv
}

func noFollow() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func entries(t *testing.T, srv *httptest.Server, paths ...string) []sources.Entry {
	t.Helper()
	es := make([]sources.Entry, 0, len(paths))
	for _, p := range paths {
		u, err := url.Parse(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		es = append(es, sources.Entry{URL: u})
	}
	return es
}

// newWarmer builds a warmer on the test server, with the report captured in out.
func newWarmer(client *http.Client, out *bytes.Buffer) warm.Warmer {
	return warm.Warmer{
		Concurrency: 4,
		Runs:        1,
		Main:        warm.Stage{CacheHeader: "X-Main", Client: client, Name: "main"},
		First:       client,
		Log:         slog.New(slog.DiscardHandler),
		Out:         out,
	}
}

// line is one parsed report line.
type line struct {
	status, cache, url, note string
	elapsed                  float64
}

func lines(t *testing.T, out *bytes.Buffer) []line {
	t.Helper()
	var ls []line
	for l := range strings.Lines(out.String()) {
		cols := strings.Split(strings.TrimSuffix(l, "\n"), "\t")
		if len(cols) != 5 {
			t.Fatalf("line %q has %d columns, want 5", l, len(cols))
		}
		elapsed, err := strconv.ParseFloat(cols[2], 64)
		if err != nil {
			t.Fatalf("line %q: elapsed %q is not a number", l, cols[2])
		}
		ls = append(ls, line{status: cols[0], cache: cols[1], elapsed: elapsed, url: cols[3], note: cols[4]})
	}
	return ls
}

func TestWarmNoEntries(t *testing.T) {
	var out bytes.Buffer
	w := newWarmer(noFollow(), &out)
	if _, err := w.Warm(t.Context(), nil); !errors.Is(err, warm.ErrNoEntries) {
		t.Fatalf("err = %v, want %v", err, warm.ErrNoEntries)
	}
}

func TestWarmFirstAloneThenBounded(t *testing.T) {
	s := &site{delay: 20 * time.Millisecond}
	srv := newSite(t, s)
	var out bytes.Buffer
	w := newWarmer(srv.Client(), &out)
	w.Concurrency = 2
	failed, err := w.Warm(t.Context(), entries(t, srv, "/a", "/b", "/c", "/d", "/e", "/f"))
	if err != nil || failed != 0 {
		t.Fatalf("failed = %d, err = %v", failed, err)
	}
	if n := s.duringFirst.Load(); n != 0 {
		t.Errorf("%d requests started while the first one was in flight", n)
	}
	if m := s.maxAtOnce.Load(); m != 2 {
		t.Errorf("max in flight = %d, want 2", m)
	}
	if got := len(lines(t, &out)); got != 7 {
		t.Errorf("%d report lines, want 7: the first fetch and one run of six", got)
	}
}

func TestWarmRunsIncludeTheFirstURL(t *testing.T) {
	s := &site{}
	srv := newSite(t, s)
	var out bytes.Buffer
	w := newWarmer(srv.Client(), &out)
	w.Runs = 2
	if _, err := w.Warm(t.Context(), entries(t, srv, "/a", "/b")); err != nil {
		t.Fatal(err)
	}
	if got := s.count("/a"); got != 3 {
		t.Errorf("/a fetched %d times, want 3: alone, then once per run", got)
	}
	if got := s.count("/b"); got != 2 {
		t.Errorf("/b fetched %d times, want 2", got)
	}
}

func TestWarmRedirect(t *testing.T) {
	cases := []struct {
		name       string
		follow     bool
		wantFailed int
		wantStatus string
		wantTarget int
	}{
		{name: "reported and not followed", wantFailed: 1, wantStatus: "301", wantTarget: 0},
		{name: "followed with the flag", follow: true, wantFailed: 0, wantStatus: "200", wantTarget: 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &site{redirects: map[string]string{"/r": "/target"}}
			srv := newSite(t, s)
			var out bytes.Buffer
			client := noFollow()
			if c.follow {
				client = srv.Client()
			}
			w := newWarmer(client, &out)
			w.Follow = c.follow
			failed, err := w.Warm(t.Context(), entries(t, srv, "/r"))
			if err != nil {
				t.Fatal(err)
			}
			if failed != c.wantFailed {
				t.Errorf("failed = %d, want %d", failed, c.wantFailed)
			}
			ls := lines(t, &out)
			if ls[1].status != c.wantStatus || !strings.HasSuffix(ls[1].note, "/target") {
				t.Errorf("line = %+v, want status %s and a note ending in /target", ls[1], c.wantStatus)
			}
			if got := s.count("/target"); got != c.wantTarget {
				t.Errorf("/target fetched %d times, want %d", got, c.wantTarget)
			}
		})
	}
}

func TestWarmLastRunDecides(t *testing.T) {
	cases := []struct {
		runs       int
		wantFailed int
	}{
		{runs: 1, wantFailed: 1},
		{runs: 2, wantFailed: 0},
	}
	for _, c := range cases {
		t.Run(strconv.Itoa(c.runs), func(t *testing.T) {
			srv := newSite(t, &site{failFirst: true})
			var out bytes.Buffer
			w := newWarmer(srv.Client(), &out)
			w.Runs = c.runs
			// The first fetch alone takes /a's failure; run 1 then fails on /b only.
			failed, err := w.Warm(t.Context(), entries(t, srv, "/a", "/b"))
			if err != nil {
				t.Fatal(err)
			}
			if failed != c.wantFailed {
				t.Errorf("failed = %d, want %d", failed, c.wantFailed)
			}
		})
	}
}

func TestWarmFailures(t *testing.T) {
	srv := newSite(t, &site{})
	var out bytes.Buffer
	w := newWarmer(srv.Client(), &out)
	es := entries(t, srv, "/a", "/missing", "/no-header")
	es = append(es, sources.Entry{URL: es[0].URL, OutsideBase: true, Violation: "outside base"})
	es = append(es, sources.Entry{URL: es[0].URL, Violation: "outside scope"})
	failed, err := w.Warm(t.Context(), es)
	if err != nil {
		t.Fatal(err)
	}
	if failed != 3 {
		t.Errorf("failed = %d, want 3: a 404, an entry outside the base, an entry out of scope", failed)
	}
	ls := lines(t, &out)[1:]
	has := func(name string, want func(line) bool) {
		t.Helper()
		if slices.ContainsFunc(ls, want) {
			return
		}
		t.Errorf("no report line for %s in:\n%s", name, out.String())
	}
	has("the warmed page", func(l line) bool {
		return l.status == "200" && l.cache == "main-value" && strings.HasSuffix(l.url, "/a") && l.note == "-"
	})
	has("the missing page", func(l line) bool { return l.status == "404" && strings.HasSuffix(l.url, "/missing") })
	has("the page without the header", func(l line) bool {
		return l.status == "200" && l.cache == "-" && strings.HasSuffix(l.url, "/no-header")
	})
	has("the entry outside the base", func(l line) bool { return l.status == "SKIP" && l.note == "outside base" })
	has("the entry out of scope", func(l line) bool { return l.status == "SKIP" && l.note == "outside scope" })
}

func TestWarmLaxFetchesOutOfScope(t *testing.T) {
	s := &site{}
	srv := newSite(t, s)
	var out bytes.Buffer
	w := newWarmer(srv.Client(), &out)
	w.Lax = true
	es := entries(t, srv, "/a")
	es[0].Violation = "outside scope"
	failed, err := w.Warm(t.Context(), es)
	if err != nil || failed != 0 {
		t.Fatalf("failed = %d, err = %v", failed, err)
	}
	if got := s.count("/a"); got != 2 {
		t.Errorf("/a fetched %d times, want 2", got)
	}
	if l := lines(t, &out)[1]; l.status != "200" || l.note != "outside scope" {
		t.Errorf("line = %+v, want 200 with the violation as note", l)
	}
}

func TestWarmLaxNoteJoinsRedirect(t *testing.T) {
	srv := newSite(t, &site{redirects: map[string]string{"/r": "/target"}})
	var out bytes.Buffer
	w := newWarmer(noFollow(), &out)
	w.Lax = true
	es := entries(t, srv, "/r")
	es[0].Violation = "outside scope"
	if _, err := w.Warm(t.Context(), es); err != nil {
		t.Fatal(err)
	}
	if l := lines(t, &out)[1]; !strings.HasPrefix(l.note, "outside scope; ") || !strings.HasSuffix(l.note, "/target") {
		t.Errorf("note = %q, want the violation then the Location", l.note)
	}
}

func TestWarmBodyError(t *testing.T) {
	srv := newSite(t, &site{})
	var out bytes.Buffer
	w := newWarmer(srv.Client(), &out)
	failed, err := w.Warm(t.Context(), entries(t, srv, "/a", "/truncated"))
	if err != nil || failed != 1 {
		t.Fatalf("failed = %d, err = %v", failed, err)
	}
	var found bool
	for _, l := range lines(t, &out) {
		found = found || (l.status == "200" && strings.HasPrefix(l.note, "reading body: "))
	}
	if !found {
		t.Errorf("no line reporting the truncated body:\n%s", out.String())
	}
}

func TestWarmDebugLog(t *testing.T) {
	srv := newSite(t, &site{})
	var out, logs bytes.Buffer
	w := newWarmer(srv.Client(), &out)
	w.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if _, err := w.Warm(t.Context(), entries(t, srv, "/a")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "msg=fetched") {
		t.Errorf("no per-request debug line in:\n%s", logs.String())
	}
}

func TestWarmRequestError(t *testing.T) {
	srv := newSite(t, &site{})
	es := entries(t, srv, "/a")
	srv.Close()
	var out bytes.Buffer
	w := newWarmer(noFollow(), &out)
	failed, err := w.Warm(t.Context(), es)
	if err != nil || failed != 1 {
		t.Fatalf("failed = %d, err = %v", failed, err)
	}
	if l := lines(t, &out)[1]; l.status != "ERR" || l.note == "-" {
		t.Errorf("line = %+v, want ERR with the error as note", l)
	}
}

func TestWarmAnonymousStage(t *testing.T) {
	s := &site{}
	srv := newSite(t, s)
	var out bytes.Buffer
	w := newWarmer(srv.Client(), &out)
	w.Runs = 2
	w.Anonymous = &warm.Stage{CacheHeader: "X-Anon", Client: srv.Client(), Name: "anonymous"}
	failed, err := w.Warm(t.Context(), entries(t, srv, "/a"))
	if err != nil || failed != 0 {
		t.Fatalf("failed = %d, err = %v", failed, err)
	}
	ls := lines(t, &out)
	got := make([]string, 0, len(ls))
	for _, l := range ls {
		got = append(got, l.cache)
	}
	want := "main-value main-value main-value anon-value anon-value"
	if strings.Join(got, " ") != want {
		t.Errorf("cache columns = %v, want %s", got, want)
	}
}

// failRT fails every request, standing in for a stage whose pages error.
type failRT struct{}

func (failRT) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("stage down")
}

func TestWarmEarlierStageFailureDoesNotDecide(t *testing.T) {
	srv := newSite(t, &site{})
	var out bytes.Buffer
	w := newWarmer(srv.Client(), &out)
	// The main stage fails every page even on its last run; the anonymous
	// stage that follows succeeds, so it, not the main stage, decides.
	w.Main.Client = &http.Client{Transport: failRT{}}
	w.Anonymous = &warm.Stage{CacheHeader: "X-Anon", Client: srv.Client(), Name: "anonymous"}
	failed, err := w.Warm(t.Context(), entries(t, srv, "/a", "/b"))
	if err != nil {
		t.Fatal(err)
	}
	if failed != 0 {
		t.Errorf("failed = %d, want 0: the later stage decides", failed)
	}
	var main, anon int
	for _, l := range lines(t, &out)[1:] {
		switch l.cache {
		case "-":
			main++ // the failing stage reports ERR with no cache header
		case "anon-value":
			anon++
		}
	}
	if main != 2 || anon != 2 {
		t.Errorf("main %d, anon %d; want 2 and 2", main, anon)
	}
}

func TestWarmCancel(t *testing.T) {
	srv := newSite(t, &site{delay: 200 * time.Millisecond})
	var out bytes.Buffer
	w := newWarmer(srv.Client(), &out)
	w.Concurrency = 1
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := w.Warm(ctx, entries(t, srv, "/a", "/b", "/c", "/d"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want %v", err, context.DeadlineExceeded)
	}
}
