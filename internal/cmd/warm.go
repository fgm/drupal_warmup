package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http/cookiejar"

	"github.com/fgm/drupal_warmup/internal/warm"
)

// Warm enumerates the site and fetches every URL, the production path.
func Warm(ctx context.Context, rt *Runtime, args []string) int {
	fs := newFlagSet("warm", "[-v N] warm --base URL (--sitemap | --list-file FILE) [flags]")
	var site siteFlags
	site.register(fs)
	site.registerFirstTimeout(fs)
	var src sourceFlags
	src.register(fs)
	concurrency := fs.Int("c", defaultConcurrency, "How many requests to run at once")
	follow := fs.Bool("follow", false, "Follow redirects and warm their target instead of failing on them")
	noAnon := fs.Bool("no-anon", false,
		"With --druser, skip the anonymous stage that fills the page cache after the logged-in runs")
	runs := fs.Int("runs", 1, "How many times to fetch every URL; 2 proves the first pass warmed")
	if status, done := parse(fs, args, false, rt); done {
		return status
	}
	base, err := site.baseURL(true)
	if err != nil {
		return usageError(fs, rt, err)
	}
	if *noAnon && site.drUser == "" {
		return usageError(fs, rt, errors.New("--no-anon needs --druser: without a login the warm is anonymous already"))
	}
	site.passwords(rt.Env)
	transport := site.transport(rt)
	jar, err := cookiejar.New(nil)
	if err != nil {
		rt.Log.Error("building cookie jar", "err", err)
		return ExitFailed
	}
	if status, done := site.loginIfAsked(ctx, rt, base, transport, jar); done {
		return status
	}

	// Enumeration follows redirects: it is not warming,
	// and a redirect there says nothing about the pages.
	source, closer, err := src.open(rt, base, newClient(transport, jar, site.firstTimeout, true, rt.Log))
	if err != nil {
		return usageError(fs, rt, err)
	}
	entries, err := source.Entries(ctx)
	_ = closer.Close()
	if err != nil {
		rt.Log.Error("enumerating", "err", err)
		return ExitUsage
	}

	w := warm.Warmer{
		Concurrency: *concurrency,
		Follow:      *follow,
		Lax:         src.lax,
		Runs:        *runs,
		Main: warm.Stage{
			CacheHeader: "X-Drupal-Cache",
			Client:      newClient(transport, jar, site.timeout, *follow, rt.Log),
			Name:        "anonymous",
		},
		First: newClient(transport, jar, site.firstTimeout, *follow, rt.Log),
		Log:   rt.Log,
		Out:   rt.Stdout,
	}
	if site.drUser != "" {
		// A session keeps a request out of the page cache,
		// so a logged-in warm proves itself on the dynamic page cache,
		// and an anonymous stage without the jar then fills the page cache.
		w.Main.CacheHeader = "X-Drupal-Dynamic-Cache"
		w.Main.Name = site.drUser
		if !*noAnon {
			w.Anonymous = &warm.Stage{
				CacheHeader: "X-Drupal-Cache",
				Client:      newClient(transport, nil, site.timeout, *follow, rt.Log),
				Name:        "anonymous",
			}
		}
	}
	failed, err := w.Warm(ctx, entries)
	if err != nil {
		rt.Log.Error("warming", "err", err)
		return ExitFailed
	}
	if failed > 0 {
		rt.Log.Warn("last run finished with failures", "failed", failed, "entries", len(entries))
		return ExitFailed
	}
	return ExitOK
}

// usageErrorf reports a bad command line found after parsing, without usage.
func usageErrorf(rt *Runtime, format string, a ...any) int {
	_, _ = fmt.Fprintf(rt.Stderr, "drupal_warmup: "+format+"\n", a...)
	return ExitUsage
}
