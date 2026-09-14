package sources_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fgm/drupal_warmup/internal/sources"
)

// encode reduces an entry to one string: the URL, prefixed by ! when outside
// the base and by ~ when outside its sitemap's scope.
func encode(e sources.Entry) string {
	switch {
	case e.OutsideBase:
		return "!" + e.URL.String()
	case e.Violation != "":
		return "~" + e.URL.String()
	default:
		return e.URL.String()
	}
}

func encodeAll(entries []sources.Entry) []string {
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		got = append(got, encode(e))
	}
	return got
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestFile(t *testing.T) {
	cases := []struct {
		name    string
		base    string
		in      string
		want    []string
		wantErr bool
	}{
		{
			name: "absolute above the base path is outside the base",
			base: "https://example.com/blog",
			in:   "https://example.com/x\n",
			want: []string{"!https://example.com/x"},
		},
		{
			name: "absolute on another host is outside the base",
			base: "https://example.com",
			in:   "https://other.example/x\n",
			want: []string{"!https://other.example/x"},
		},
		{
			name: "absolute under the base passes through",
			base: "https://example.com/blog",
			in:   "https://example.com/blog/x?q=1\n",
			want: []string{"https://example.com/blog/x?q=1"},
		},
		{
			name:    "bad line is an error",
			base:    "https://example.com",
			in:      "http://[::1\n",
			wantErr: true,
		},
		{
			name: "blank and comment lines are skipped",
			base: "https://example.com",
			in:   "\n# https://example.com/z\n  \n/a\n",
			want: []string{"https://example.com/a"},
		},
		{
			name: "relative joins onto the base path, query and trailing slash kept",
			base: "https://example.com/blog",
			in:   "/a\nb/\nc?x=1\n",
			want: []string{"https://example.com/blog/a", "https://example.com/blog/b/", "https://example.com/blog/c?x=1"},
		},
		{
			name: "scheme mismatch is a scope violation, not outside the base",
			base: "https://example.com",
			in:   "http://example.com/x\n",
			want: []string{"~http://example.com/x"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := sources.File{Base: mustParse(t, c.base), In: strings.NewReader(c.in)}
			entries, err := f.Entries(t.Context())
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if c.wantErr {
				if entries != nil {
					t.Fatalf("entries = %v alongside an error", entries)
				}
				return
			}
			if got := encodeAll(entries); !slices.Equal(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestFileReadError(t *testing.T) {
	f := sources.File{Base: mustParse(t, "https://example.com"), In: errReader{}}
	if _, err := f.Entries(t.Context()); err == nil {
		t.Fatal("no error from a failing reader")
	}
}

// site serves sitemap documents in which HOST stands for the server's own origin.
type site struct {
	files     map[string]string
	redirects map[string]string
	hits      map[string]*atomic.Int64
}

func (s *site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if n, ok := s.hits[r.URL.Path]; ok {
		n.Add(1)
	}
	if to, ok := s.redirects[r.URL.Path]; ok {
		http.Redirect(w, r, to, http.StatusFound)
		return
	}
	body, ok := s.files[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if body == "TRUNCATED" {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("<urlset>"))
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(strings.ReplaceAll(body, "HOST", "http://"+r.Host)))
}

func urlset(locs ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, l := range locs {
		b.WriteString("<url><loc> " + l + " </loc><priority>0.5</priority></url>")
	}
	b.WriteString("</urlset>")
	return b.String()
}

func index(locs ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, l := range locs {
		b.WriteString("<sitemap><loc>" + l + "</loc></sitemap>")
	}
	b.WriteString("</sitemapindex>")
	return b.String()
}

func TestSitemap(t *testing.T) {
	cases := []struct {
		name      string
		files     map[string]string
		redirects map[string]string
		basePath  string
		lax       bool
		want      []string
		wantErr   error
		anyErr    bool
		unfetched []string
	}{
		{
			name:  "default path, entries in scope, an empty path is the root",
			files: map[string]string{"/sitemap.xml": urlset("HOST/a", "HOST/b", "HOST")},
			want:  []string{"HOST/a", "HOST/b", "HOST"},
		},
		{
			name: "robots.txt names the sitemap, whole host in scope, other host ignored",
			files: map[string]string{
				"/robots.txt": "User-agent: *\nDisallow: /admin\nsitemap: HOST/maps/x.xml\nSitemap: https://other.example/s.xml\nSitemap: http://[::1\n",
				"/maps/x.xml": urlset("HOST/a", "HOST/elsewhere/b"),
			},
			want:      []string{"HOST/a", "HOST/elsewhere/b"},
			unfetched: []string{"/sitemap.xml"},
		},
		{
			name: "nested base ignores the host root's robots line and joins on its path",
			files: map[string]string{
				"/robots.txt":       "Sitemap: HOST/sitemap.xml\n",
				"/sitemap.xml":      urlset("HOST/root"),
				"/blog/sitemap.xml": urlset("HOST/blog/a", "HOST/other"),
			},
			basePath:  "/blog",
			want:      []string{"HOST/blog/a", "!HOST/other"},
			unfetched: []string{"/sitemap.xml"},
		},
		{
			name:      "scope follows the final URL after a redirect",
			files:     map[string]string{"/maps/sitemap.xml": urlset("HOST/maps/a", "HOST/b")},
			redirects: map[string]string{"/sitemap.xml": "/maps/sitemap.xml"},
			want:      []string{"HOST/maps/a", "~HOST/b"},
		},
		{
			name: "index children: in scope read, out of scope marked and not read, other host outside base",
			files: map[string]string{
				"/maps/index.xml": index("HOST/maps/s1.xml", "HOST/s2.xml", "https://other.example/s3.xml"),
				"/maps/s1.xml":    urlset("HOST/maps/a", "HOST/b"),
				"/s2.xml":         urlset("HOST/c"),
			},
			redirects: map[string]string{"/sitemap.xml": "/maps/index.xml"},
			want:      []string{"HOST/maps/a", "~HOST/b", "~HOST/s2.xml", "!https://other.example/s3.xml"},
			unfetched: []string{"/s2.xml"},
		},
		{
			name: "lax reads an out-of-scope child and holds its entries to the index's scope",
			files: map[string]string{
				"/maps/index.xml": index("HOST/s2.xml"),
				"/s2.xml":         urlset("HOST/c", "HOST/maps/d"),
			},
			redirects: map[string]string{"/sitemap.xml": "/maps/index.xml"},
			lax:       true,
			want:      []string{"~HOST/c", "HOST/maps/d"},
		},
		{
			name:    "missing sitemap",
			files:   map[string]string{},
			wantErr: sources.ErrStatus,
		},
		{
			name:    "not a sitemap",
			files:   map[string]string{"/sitemap.xml": "<html></html>"},
			wantErr: sources.ErrNotSitemap,
		},
		{
			name:    "malformed XML",
			files:   map[string]string{"/sitemap.xml": "<urlset><url>"},
			wantErr: sources.ErrNotSitemap,
		},
		{
			name: "an index inside an index",
			files: map[string]string{
				"/sitemap.xml": index("HOST/inner.xml"),
				"/inner.xml":   index("HOST/deep.xml"),
			},
			wantErr: sources.ErrNotSitemap,
		},
		{
			name:    "missing child",
			files:   map[string]string{"/sitemap.xml": index("HOST/gone.xml")},
			wantErr: sources.ErrStatus,
		},
		{
			name:   "body cut short",
			files:  map[string]string{"/sitemap.xml": "TRUNCATED"},
			anyErr: true,
		},
		{
			name: "child not a sitemap",
			files: map[string]string{
				"/sitemap.xml": index("HOST/child.xml"),
				"/child.xml":   "<html></html>",
			},
			wantErr: sources.ErrNotSitemap,
		},
		{
			name: "bad location in a child",
			files: map[string]string{
				"/sitemap.xml": index("HOST/child.xml"),
				"/child.xml":   urlset("http://[::1"),
			},
			anyErr: true,
		},
		{
			name:   "bad location",
			files:  map[string]string{"/sitemap.xml": urlset("http://[::1")},
			anyErr: true,
		},
		{
			name:   "bad child location",
			files:  map[string]string{"/sitemap.xml": index("http://[::1")},
			anyErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &site{files: c.files, redirects: c.redirects, hits: map[string]*atomic.Int64{}}
			for _, p := range c.unfetched {
				s.hits[p] = &atomic.Int64{}
			}
			srv := httptest.NewServer(s)
			defer srv.Close()
			sm := sources.Sitemap{
				Base:   mustParse(t, srv.URL+c.basePath),
				Client: srv.Client(),
				Lax:    c.lax,
				Log:    slog.New(slog.DiscardHandler),
			}
			entries, err := sm.Entries(t.Context())
			switch {
			case c.anyErr:
				if err == nil {
					t.Fatal("no error")
				}
			case c.wantErr != nil:
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}
			case err != nil:
				t.Fatal(err)
			}
			if err != nil {
				if entries != nil {
					t.Fatalf("entries = %v alongside an error", entries)
				}
				return
			}
			want := make([]string, 0, len(c.want))
			for _, w := range c.want {
				want = append(want, strings.ReplaceAll(w, "HOST", srv.URL))
			}
			if got := encodeAll(entries); !slices.Equal(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			for p, n := range s.hits {
				if n.Load() != 0 {
					t.Errorf("%s was fetched %d times", p, n.Load())
				}
			}
		})
	}
}

func TestSitemapNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	sm := sources.Sitemap{Base: mustParse(t, srv.URL), Client: srv.Client(), Log: slog.New(slog.DiscardHandler)}
	if _, err := sm.Entries(context.Background()); err == nil {
		t.Fatal("no error from a closed server")
	}
}
