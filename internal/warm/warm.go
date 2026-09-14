// Package warm fetches the enumerated URLs so that the site's caches fill.
//
// Nothing here is Drupal-specific beyond the cache header in the report.
// The first page is fetched alone, so that one request pays a cache rebuild
// rather than a herd of them;
// then every entry is fetched with bounded concurrency, as many times as asked,
// and the last run decides the outcome.
package warm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fgm/drupal_warmup/internal/sources"
)

// ErrNoEntries reports an enumeration with nothing to warm.
var ErrNoEntries = errors.New("nothing to warm")

// Warmer fetches entries and writes one report line per fetch.
//
// The report is tab separated: status, cache header, elapsed seconds, URL, note.
// The note carries the Location of a redirect, the reason an entry was skipped,
// or the error of a failed request, and is - otherwise.
type Warmer struct {
	// configuration
	Concurrency int
	// Follow makes a redirect a warmed target rather than a failure.
	Follow bool
	// Lax fetches entries outside their sitemap's scope instead of skipping them.
	Lax  bool
	Runs int

	// services
	// Main is the stage every run goes through: the anonymous one,
	// or the logged-in one when there is a session.
	Main Stage
	// Anonymous is a stage run after Main, Runs times as well, when set:
	// a logged-in warm fills the render cache and the account's dynamic page cache,
	// and a session keeps requests out of the page cache,
	// so an anonymous stage over the same URLs then fills it from those fragments.
	Anonymous *Stage
	// First fetches the serialized first page, with the longer timeout a rebuild needs.
	First *http.Client
	Log   *slog.Logger
	Out   io.Writer

	// mutable state
	mu sync.Mutex
}

// Stage is a client and the response header that proves what it warmed:
// X-Drupal-Cache for the page cache, X-Drupal-Dynamic-Cache for the dynamic page cache.
type Stage struct {
	CacheHeader string
	// Client fetches pages and follows redirects, or not, as Follow says.
	Client *http.Client
	Name   string
}

// outcome is one fetch, reported and counted.
type outcome struct {
	cache   string
	elapsed time.Duration
	failed  bool
	note    string
	status  string
}

// Warm fetches every entry Runs times and returns the failures of the last run.
//
// The first entry is fetched alone before any fan-out, whatever enumerated it:
// the render warmups after a rebuild are paid by the first page rendered,
// not by a sitemap request, and measured at 2 s each for four pages in flight together.
// Earlier runs' failures are logged, not returned:
// the first run after a cache rebuild is the cold pass and may time out.
func (w *Warmer) Warm(ctx context.Context, entries []sources.Entry) (int, error) {
	if len(entries) == 0 {
		return 0, ErrNoEntries
	}
	w.Log.Info("first fetch alone, to let one request pay the cache rebuild", "url", entries[0].URL)
	first := Stage{CacheHeader: w.Main.CacheHeader, Client: w.First, Name: w.Main.Name}
	w.report(entries[0], w.fetch(ctx, &first, entries[0]))
	stages := []*Stage{&w.Main}
	if w.Anonymous != nil {
		stages = append(stages, w.Anonymous)
	}
	failed := 0
	for i, stage := range stages {
		for run := 1; run <= w.Runs; run++ {
			t0 := time.Now()
			n, err := w.fanOut(ctx, stage, entries)
			if err != nil {
				return 0, err
			}
			failed = n
			attrs := []any{"stage", stage.Name, "run", run, "entries", len(entries), "failed", failed, "elapsed", time.Since(t0)}
			if failed > 0 && (run < w.Runs || i < len(stages)-1) {
				w.Log.Warn("run finished with failures, a later run decides", attrs...)
			} else {
				w.Log.Info("run finished", attrs...)
			}
		}
	}
	return failed, nil
}

// fanOut fetches every entry once, Concurrency at a time, and counts failures.
func (w *Warmer) fanOut(ctx context.Context, stage *Stage, entries []sources.Entry) (int, error) {
	sem := make(chan struct{}, max(w.Concurrency, 1))
	var failed atomic.Int64
	var wg sync.WaitGroup
	for _, e := range entries {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return 0, ctx.Err()
		}
		wg.Go(func() {
			defer func() { <-sem }()
			o := w.fetch(ctx, stage, e)
			w.report(e, o)
			if o.failed {
				failed.Add(1)
			}
		})
	}
	wg.Wait()
	return int(failed.Load()), ctx.Err()
}

// fetch requests one entry, or skips it when its scope says so, and drains the body.
//
// Draining matters: elapsed then covers the whole render,
// and BigPipe streams the authenticated page after the headers,
// so closing early would abort the render being warmed.
func (w *Warmer) fetch(ctx context.Context, stage *Stage, e sources.Entry) outcome {
	if e.OutsideBase || (e.Violation != "" && !w.Lax) {
		return outcome{cache: "-", failed: true, note: e.Violation, status: "SKIP"}
	}
	o := outcome{cache: "-", note: e.Violation}
	t0 := time.Now()
	req := (&http.Request{Header: http.Header{}, Method: http.MethodGet, URL: e.URL}).WithContext(ctx)
	resp, err := stage.Client.Do(req)
	if err != nil {
		return outcome{cache: "-", elapsed: time.Since(t0), failed: true, note: err.Error(), status: "ERR"}
	}
	defer func() { _ = resp.Body.Close() }()
	_, err = io.Copy(io.Discard, resp.Body)
	o.elapsed = time.Since(t0)
	o.status = strconv.Itoa(resp.StatusCode)
	if c := resp.Header.Get(stage.CacheHeader); c != "" {
		o.cache = c
	}
	switch {
	case err != nil:
		o.failed = true
		o.note = join(o.note, "reading body: "+err.Error())
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		// Only reached without Follow: a redirect the client did not take.
		o.failed = true
		o.note = join(o.note, resp.Header.Get("Location"))
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		o.failed = true
	case resp.Request.URL.String() != e.URL.String():
		// Followed: the target was warmed, the stale entry stays visible.
		o.note = join(o.note, resp.Request.URL.String())
	}
	if w.Log.Enabled(ctx, slog.LevelDebug) {
		w.Log.Debug("fetched", "url", e.URL, "status", resp.StatusCode)
	}
	return o
}

// report writes the line for one fetch.
func (w *Warmer) report(e sources.Entry, o outcome) {
	note := o.note
	if note == "" {
		note = "-"
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = fmt.Fprintf(w.Out, "%s\t%s\t%.3f\t%s\t%s\n", o.status, o.cache, o.elapsed.Seconds(), e.URL, note)
}

// join appends a note to an existing one.
func join(existing, note string) string {
	if existing == "" {
		return note
	}
	return existing + "; " + note
}
