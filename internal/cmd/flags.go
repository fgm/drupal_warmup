package cmd

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fgm/drupal_warmup/internal/sources"
)

// Environment variables standing in for the password flags,
// which are visible to ps and land in shell history.
const (
	EnvBasicAuthPassword = "DRUPAL_WARMUP_BAPASS"
	EnvDrupalPassword    = "DRUPAL_WARMUP_DRPASS"
)

// Flag defaults.
const (
	defaultConcurrency  = 4
	defaultFirstTimeout = 60 * time.Second
	defaultLoginTimeout = 180 * time.Second
	defaultTimeout      = 10 * time.Second
)

// siteFlags are the flags naming the site and how to authenticate against it.
type siteFlags struct {
	baPass       string
	baUser       string
	base         string
	drPass       string
	drUser       string
	firstTimeout time.Duration
	loginTimeout time.Duration
	timeout      time.Duration
}

// sourceFlags are the flags choosing where the URLs come from.
type sourceFlags struct {
	lax      bool
	listFile string
	sitemap  bool
}

// envValue looks a key up in an environment of KEY=value strings.
func envValue(env []string, key string) string {
	for _, kv := range env {
		if v, found := strings.CutPrefix(kv, key+"="); found {
			return v
		}
	}
	return ""
}

// newFlagSet returns a flag set reporting nothing on its own: parse does.
func newFlagSet(name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(fs.Output(), "Usage: drupal_warmup %s\n\nThe flags are:\n", usage)
		fs.PrintDefaults()
	}
	return fs
}

// parse parses args and says whether the command is over, and with what status.
//
// -h prints usage on stdout and ends with ExitOK.
// A rejected command line, or operands where none are taken,
// prints the reason and usage on stderr and ends with ExitUsage.
func parse(fs *flag.FlagSet, args []string, operands bool, rt *Runtime) (int, bool) {
	err := fs.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fs.SetOutput(rt.Stdout)
		fs.Usage()
		return ExitOK, true
	case err != nil:
		return usageError(fs, rt, err), true
	case !operands && fs.NArg() > 0:
		return usageError(fs, rt, fmt.Errorf("unexpected argument %q", fs.Arg(0))), true
	}
	return ExitOK, false
}

// usageError reports a bad command line on stderr, with usage.
func usageError(fs *flag.FlagSet, rt *Runtime, err error) int {
	_, _ = fmt.Fprintf(rt.Stderr, "drupal_warmup %s: %v\n", fs.Name(), err)
	fs.SetOutput(rt.Stderr)
	fs.Usage()
	return ExitUsage
}

// baseURL parses --base, required unless the command can do without one.
func (s *siteFlags) baseURL(required bool) (*url.URL, error) {
	if s.base == "" {
		if required {
			return nil, errors.New("--base is required")
		}
		return nil, nil
	}
	u, err := url.Parse(s.base)
	if err != nil {
		return nil, fmt.Errorf("parsing --base: %w", err)
	}
	if !u.IsAbs() || u.Host == "" {
		return nil, fmt.Errorf("--base %q needs a scheme and a host", s.base)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("--base %q takes no query or fragment", s.base)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

// passwords fills the password flags from the environment where they were not given.
func (s *siteFlags) passwords(env []string) {
	if s.baPass == "" {
		s.baPass = envValue(env, EnvBasicAuthPassword)
	}
	if s.drPass == "" {
		s.drPass = envValue(env, EnvDrupalPassword)
	}
}

// register declares the site and authentication flags.
func (s *siteFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&s.base, "base", "",
		"The site: scheme, host and, for a site that is not rooted, its path prefix, as in https://example.com/blog")
	fs.StringVar(&s.baUser, "bauser", "", "The user name for web server basic auth; empty means no basic auth")
	fs.StringVar(&s.baPass, "bapass", "", "The password for basic auth, or set "+EnvBasicAuthPassword)
	fs.StringVar(&s.drUser, "druser", "", "The Drupal account to log in as; empty means anonymous")
	fs.StringVar(&s.drPass, "drpass", "", "The password of the Drupal account, or set "+EnvDrupalPassword)
	fs.DurationVar(&s.loginTimeout, "login-timeout", defaultLoginTimeout, "How long to wait for the login")
	fs.DurationVar(&s.timeout, "timeout", defaultTimeout, "How long to wait for a page")
}

// registerFirstTimeout declares the timeout of the one request made alone after a cache rebuild.
func (s *siteFlags) registerFirstTimeout(fs *flag.FlagSet) {
	fs.DurationVar(&s.firstTimeout, "first-timeout", defaultFirstTimeout,
		"How long to wait for the first request, which pays the cache rebuild")
}

// transport wraps the injected transport with basic auth when asked for.
func (s *siteFlags) transport(rt *Runtime) http.RoundTripper {
	if s.baUser == "" {
		return rt.Transport
	}
	return &basicAuth{next: rt.Transport, pass: s.baPass, user: s.baUser}
}

// open returns the chosen source, and what to close once it is read.
//
// The sitemap client is the caller's:
// its fetch is the one request made alone after a cache rebuild.
func (f *sourceFlags) open(rt *Runtime, base *url.URL, sitemapClient *http.Client) (sources.Source, io.Closer, error) {
	switch {
	case f.sitemap && f.listFile != "":
		return nil, nil, errors.New("--sitemap and --list-file are exclusive")
	case f.sitemap:
		return &sources.Sitemap{Base: base, Client: sitemapClient, Lax: f.lax, Log: rt.Log}, io.NopCloser(nil), nil
	case f.listFile == "-":
		return &sources.File{Base: base, In: rt.Stdin}, io.NopCloser(nil), nil
	case f.listFile != "":
		in, err := rt.FS.Open(f.listFile)
		if err != nil {
			return nil, nil, fmt.Errorf("opening --list-file: %w", err)
		}
		return &sources.File{Base: base, In: in}, in, nil
	default:
		return nil, nil, errors.New("one of --sitemap and --list-file is required")
	}
}

// register declares the source flags.
func (f *sourceFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.listFile, "list-file", "", "A text file of URLs or paths, one per line; - for stdin")
	fs.BoolVar(&f.sitemap, "sitemap", false,
		"Use the site's XML sitemap: the Sitemap: lines of robots.txt, else <base>/sitemap.xml")
	fs.BoolVar(&f.lax, "lax", false, "Report sitemap entries outside their sitemap's scope instead of failing on them")
}
