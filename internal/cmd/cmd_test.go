package cmd_test

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/fgm/drupal_warmup/internal/cmd"
)

const (
	session = "SESS0123456789abcdef0123456789abcdef"
	form    = `<form><input name="name"><input name="pass">` +
		`<input type="hidden" name="form_build_id" value="form-abc">` +
		`<input type="hidden" name="form_id" value="user_login_form">` +
		`<input type="submit" name="op" value="Log in"></form>`
)

// siteOptions shape the fake site.
type siteOptions struct {
	// basicUser and basicPass, when set, guard every path with basic auth.
	basicPass string
	basicUser string
	// sitemap lists the paths of the sitemap; nil serves none.
	sitemap []string
}

// newSite serves a site with a sitemap, a login form, a redirect and an Xdebug echo.
//
// Pages answer X-Drupal-Cache: PAGE anonymously and
// X-Drupal-Dynamic-Cache: DYN with a session.
func newSite(t *testing.T, o siteOptions) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		if o.sitemap == nil {
			http.NotFound(w, r)
			return
		}
		var b strings.Builder
		b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
		for _, p := range o.sitemap {
			b.WriteString("<url><loc>http://" + r.Host + p + "</loc></url>")
		}
		b.WriteString("</urlset>")
		_, _ = io.WriteString(w, b.String())
	})
	mux.HandleFunc("/user/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, form)
			return
		}
		if r.FormValue("pass") != "pass" || r.FormValue("form_build_id") != "form-abc" {
			_, _ = io.WriteString(w, form)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: session, Value: "s", Path: "/"})
		http.Redirect(w, r, "/user/1", http.StatusSeeOther)
	})
	mux.HandleFunc("/r", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/a", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/truncated", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "short")
	})
	mux.HandleFunc("/xdebug", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("XDEBUG_SESSION"); err == nil {
			w.Header().Set("X-Got-Trigger", c.Value)
		}
		_, _ = io.WriteString(w, "debug")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/a" {
			http.NotFound(w, r)
			return
		}
		if _, err := r.Cookie(session); err == nil {
			w.Header().Set("X-Drupal-Cache", "UNCACHEABLE (request policy)")
			w.Header().Set("X-Drupal-Dynamic-Cache", "DYN")
		} else {
			w.Header().Set("X-Drupal-Cache", "PAGE")
		}
		_, _ = io.WriteString(w, "page")
	})
	var h http.Handler = mux
	if o.basicUser != "" {
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u, p, ok := r.BasicAuth(); !ok || u != o.basicUser || p != o.basicPass {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			mux.ServeHTTP(w, r)
		})
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// run drives RealMain the way main does, with everything captured.
type run struct {
	env    []string
	fsys   fs.FS
	stdin  string
	status int
	stdout string
	stderr string
}

func (r *run) do(t *testing.T, args ...string) *run {
	t.Helper()
	var out, errb bytes.Buffer
	fsys := r.fsys
	if fsys == nil {
		fsys = fstest.MapFS{}
	}
	r.status = cmd.RealMain(t.Context(), append([]string{"drupal_warmup"}, args...), r.env,
		strings.NewReader(r.stdin), &out, &errb, fsys, http.DefaultTransport)
	r.stdout, r.stderr = out.String(), errb.String()
	return r
}

func TestGlobal(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantStatus int
		wantOut    string
		wantErr    string
	}{
		{name: "no command", args: nil, wantStatus: cmd.ExitUsage, wantErr: "no command"},
		{name: "help on stdout", args: []string{"-h"}, wantStatus: cmd.ExitOK, wantOut: "The commands are"},
		{name: "unknown flag", args: []string{"-x", "warm"}, wantStatus: cmd.ExitUsage, wantErr: "-x"},
		{name: "unknown command", args: []string{"bogus"}, wantStatus: cmd.ExitUsage, wantErr: `unknown command "bogus"`},
		{name: "version", args: []string{"version"}, wantStatus: cmd.ExitOK, wantOut: "drupal_warmup "},
		{name: "command help", args: []string{"warm", "-h"}, wantStatus: cmd.ExitOK, wantOut: "Usage: drupal_warmup"},
		{name: "list help", args: []string{"list", "-h"}, wantStatus: cmd.ExitOK, wantOut: "] list --base"},
		{name: "debug help", args: []string{"debug", "-h"}, wantStatus: cmd.ExitOK, wantOut: "] debug ["},
		{name: "rejected command flag", args: []string{"warm", "--bogus"}, wantStatus: cmd.ExitUsage, wantErr: "-bogus"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := (&run{}).do(t, c.args...)
			if r.status != c.wantStatus {
				t.Errorf("status = %d, want %d; stderr %q", r.status, c.wantStatus, r.stderr)
			}
			if !strings.Contains(r.stdout, c.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", r.stdout, c.wantOut)
			}
			if !strings.Contains(r.stderr, c.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", r.stderr, c.wantErr)
			}
		})
	}
}

func TestVerbosity(t *testing.T) {
	srv := newSite(t, siteOptions{sitemap: []string{"/"}})
	silent := (&run{}).do(t, "-v", "0", "warm", "--base="+srv.URL, "--sitemap")
	if silent.status != cmd.ExitOK || silent.stderr != "" {
		t.Errorf("-v 0: status %d, stderr %q, want 0 and nothing", silent.status, silent.stderr)
	}
	loud := (&run{}).do(t, "-v", "3", "warm", "--base="+srv.URL, "--sitemap")
	if !strings.Contains(loud.stderr, "level=DEBUG") {
		t.Errorf("-v 3: stderr %q carries no debug line", loud.stderr)
	}
}

func TestNewLogger(t *testing.T) {
	cases := []struct {
		verbosity int
		enabled   slog.Level
		disabled  slog.Level
	}{
		{0, slog.LevelError + 100, slog.LevelError},
		{1, slog.LevelWarn, slog.LevelInfo},
		{2, slog.LevelInfo, slog.LevelDebug},
		{3, slog.LevelDebug, slog.LevelDebug - 1},
	}
	for _, c := range cases {
		log := cmd.NewLogger(io.Discard, c.verbosity)
		if !log.Enabled(context.Background(), c.enabled) && c.verbosity > 0 {
			t.Errorf("-v %d: level %v disabled", c.verbosity, c.enabled)
		}
		if log.Enabled(context.Background(), c.disabled) {
			t.Errorf("-v %d: level %v enabled", c.verbosity, c.disabled)
		}
	}
}

func TestWarmUsage(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "no base", args: []string{"--sitemap"}, want: "--base is required"},
		{name: "base without scheme", args: []string{"--base=example.com", "--sitemap"}, want: "needs a scheme"},
		{name: "base unparsable", args: []string{"--base=http://[::1", "--sitemap"}, want: "parsing --base"},
		{name: "base with query", args: []string{"--base=http://example.com/?x", "--sitemap"}, want: "no query"},
		{name: "no source", args: []string{"--base=http://example.com"}, want: "one of --sitemap and --list-file"},
		{name: "both sources", args: []string{"--base=http://example.com", "--sitemap", "--list-file=-"}, want: "exclusive"},
		{name: "operand", args: []string{"--base=http://example.com", "--sitemap", "extra"}, want: `unexpected argument "extra"`},
		{name: "no-anon without druser", args: []string{"--base=http://example.com", "--sitemap", "--no-anon"}, want: "--no-anon needs --druser"},
		{name: "list-file missing", args: []string{"--base=http://example.com", "--list-file=none.txt"}, want: "opening --list-file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := (&run{}).do(t, append([]string{"warm"}, c.args...)...)
			if r.status != cmd.ExitUsage || !strings.Contains(r.stderr, c.want) {
				t.Errorf("status = %d, stderr = %q; want %d and %q", r.status, r.stderr, cmd.ExitUsage, c.want)
			}
		})
	}
}

func TestWarm(t *testing.T) {
	cases := []struct {
		name       string
		site       siteOptions
		run        run
		args       []string
		wantStatus int
		wantLines  int
		wantCache  string
	}{
		{
			name: "sitemap", site: siteOptions{sitemap: []string{"/", "/a"}},
			args: []string{"--sitemap", "--runs=2"}, wantStatus: cmd.ExitOK, wantLines: 5, wantCache: "PAGE PAGE PAGE PAGE PAGE",
		},
		{
			name: "list file", site: siteOptions{},
			run:  run{fsys: fstest.MapFS{"urls.txt": {Data: []byte("/\n/a\n")}}},
			args: []string{"--list-file=urls.txt"}, wantStatus: cmd.ExitOK, wantLines: 3, wantCache: "PAGE PAGE PAGE",
		},
		{
			name: "list on stdin", site: siteOptions{},
			run:  run{stdin: "/\n"},
			args: []string{"--list-file=-"}, wantStatus: cmd.ExitOK, wantLines: 2, wantCache: "PAGE PAGE",
		},
		{
			name: "sitemap missing", site: siteOptions{},
			args: []string{"--sitemap"}, wantStatus: cmd.ExitUsage,
		},
		{
			name: "sitemap empty", site: siteOptions{sitemap: []string{}},
			args: []string{"--sitemap"}, wantStatus: cmd.ExitFailed,
		},
		{
			name: "redirect fails", site: siteOptions{sitemap: []string{"/r"}},
			args: []string{"--sitemap"}, wantStatus: cmd.ExitFailed, wantLines: 2, wantCache: "- -",
		},
		{
			name: "redirect followed", site: siteOptions{sitemap: []string{"/r"}},
			args: []string{"--sitemap", "--follow"}, wantStatus: cmd.ExitOK, wantLines: 2, wantCache: "PAGE PAGE",
		},
		{
			name: "redirect loop under follow", site: siteOptions{sitemap: []string{"/loop"}},
			args: []string{"--sitemap", "--follow"}, wantStatus: cmd.ExitFailed, wantLines: 2, wantCache: "- -",
		},
		{
			name: "login then anonymous stage", site: siteOptions{sitemap: []string{"/", "/a"}},
			args: []string{"--sitemap", "--druser=warmer", "--drpass=pass"}, wantStatus: cmd.ExitOK,
			wantLines: 5, wantCache: "DYN DYN DYN PAGE PAGE",
		},
		{
			name: "login without the anonymous stage", site: siteOptions{sitemap: []string{"/"}},
			args: []string{"--sitemap", "--druser=warmer", "--drpass=pass", "--no-anon"}, wantStatus: cmd.ExitOK,
			wantLines: 2, wantCache: "DYN DYN",
		},
		{
			name: "password from the environment", site: siteOptions{sitemap: []string{"/"}},
			run:  run{env: []string{"OTHER=1", cmd.EnvDrupalPassword + "=pass"}},
			args: []string{"--sitemap", "--druser=warmer", "--no-anon"}, wantStatus: cmd.ExitOK, wantLines: 2, wantCache: "DYN DYN",
		},
		{
			name: "login fails", site: siteOptions{sitemap: []string{"/"}},
			args: []string{"--sitemap", "--druser=warmer", "--drpass=wrong"}, wantStatus: cmd.ExitLogin,
		},
		{
			name: "basic auth required", site: siteOptions{sitemap: []string{"/"}, basicUser: "u", basicPass: "p"},
			args: []string{"--sitemap"}, wantStatus: cmd.ExitUsage,
		},
		{
			name: "basic auth given", site: siteOptions{sitemap: []string{"/"}, basicUser: "u", basicPass: "p"},
			run:  run{env: []string{cmd.EnvBasicAuthPassword + "=p"}},
			args: []string{"--sitemap", "--bauser=u"}, wantStatus: cmd.ExitOK, wantLines: 2, wantCache: "PAGE PAGE",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newSite(t, c.site)
			r := c.run
			r.do(t, append([]string{"warm", "--base=" + srv.URL}, c.args...)...)
			if r.status != c.wantStatus {
				t.Fatalf("status = %d, want %d; stderr %q", r.status, c.wantStatus, r.stderr)
			}
			var cache []string
			n := 0
			for l := range strings.Lines(r.stdout) {
				n++
				cache = append(cache, strings.Split(l, "\t")[1])
			}
			if n != c.wantLines {
				t.Errorf("%d report lines, want %d:\n%s", n, c.wantLines, r.stdout)
			}
			if c.wantCache != "" && strings.Join(cache, " ") != c.wantCache {
				t.Errorf("cache columns %v, want %s", cache, c.wantCache)
			}
		})
	}
}

func TestList(t *testing.T) {
	cases := []struct {
		name       string
		site       siteOptions
		args       []string
		wantStatus int
		wantOut    string
	}{
		{name: "urls", site: siteOptions{sitemap: []string{"/", "/a"}}, args: []string{"--sitemap"}, wantStatus: cmd.ExitOK, wantOut: "HOST/\nHOST/a\n"},
		{name: "out of scope marked", site: siteOptions{sitemap: []string{"/"}}, args: []string{"--list-file=-"}, wantStatus: cmd.ExitFailed, wantOut: "# https://other.example/x\toutside base HOST/\n"},
		{name: "both sources", site: siteOptions{}, args: []string{"--sitemap", "--list-file=-"}, wantStatus: cmd.ExitUsage},
		{name: "no base", site: siteOptions{}, args: []string{"--sitemap"}, wantStatus: cmd.ExitUsage},
		{name: "sitemap missing", site: siteOptions{}, args: []string{"--sitemap"}, wantStatus: cmd.ExitUsage},
		{name: "nothing enumerated", site: siteOptions{sitemap: []string{}}, args: []string{"--sitemap"}, wantStatus: cmd.ExitFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newSite(t, c.site)
			args := []string{"list"}
			if c.name != "no base" {
				args = append(args, "--base="+srv.URL)
			}
			r := (&run{stdin: "https://other.example/x\n"}).do(t, append(args, c.args...)...)
			if r.status != c.wantStatus {
				t.Fatalf("status = %d, want %d; stderr %q", r.status, c.wantStatus, r.stderr)
			}
			if want := strings.ReplaceAll(c.wantOut, "HOST", srv.URL); r.stdout != want {
				t.Errorf("stdout = %q, want %q", r.stdout, want)
			}
		})
	}
}

func TestDebug(t *testing.T) {
	srv := newSite(t, siteOptions{})
	closed := newSite(t, siteOptions{})
	closed.Close()
	cases := []struct {
		name       string
		args       []string
		wantStatus int
		wantOut    string
	}{
		{name: "no url", args: nil, wantStatus: cmd.ExitUsage},
		{name: "relative without base", args: []string{"xdebug"}, wantStatus: cmd.ExitUsage},
		{name: "druser without base", args: []string{"--druser=warmer", srv.URL + "/xdebug"}, wantStatus: cmd.ExitUsage},
		{name: "bad base", args: []string{"--base=nope", "xdebug"}, wantStatus: cmd.ExitUsage},
		{name: "trigger sent", args: []string{srv.URL + "/xdebug"}, wantStatus: cmd.ExitOK, wantOut: "X-Got-Trigger: PHPSTORM\n"},
		{name: "trigger chosen, relative to base", args: []string{"--base=" + srv.URL, "--trigger=IDE", "xdebug"}, wantStatus: cmd.ExitOK, wantOut: "X-Got-Trigger: IDE\n"},
		{name: "logged in first", args: []string{"--base=" + srv.URL, "--druser=warmer", "--drpass=pass", "/"}, wantStatus: cmd.ExitOK, wantOut: "X-Drupal-Dynamic-Cache: DYN\n"},
		{name: "unreachable", args: []string{closed.URL + "/"}, wantStatus: cmd.ExitFailed},
		{name: "body cut short", args: []string{srv.URL + "/truncated"}, wantStatus: cmd.ExitFailed},
		{name: "bad url", args: []string{"http://[::1"}, wantStatus: cmd.ExitUsage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := (&run{}).do(t, append([]string{"debug"}, c.args...)...)
			if r.status != c.wantStatus {
				t.Fatalf("status = %d, want %d; stderr %q", r.status, c.wantStatus, r.stderr)
			}
			if !strings.Contains(r.stdout, c.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", r.stdout, c.wantOut)
			}
			if c.wantStatus == cmd.ExitOK && !strings.Contains(r.stdout, "body: ") {
				t.Errorf("stdout = %q, no body length", r.stdout)
			}
		})
	}
}

func TestOSFS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "urls.txt")
	if err := os.WriteFile(path, []byte("/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := cmd.OSFS{}.Open(path)
	if err != nil {
		t.Fatalf("absolute path rejected: %v", err)
	}
	_ = f.Close()
	if _, err := (cmd.OSFS{}).Open(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Fatal("missing file opened")
	}
}
