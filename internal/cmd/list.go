package cmd

import (
	"context"
	"fmt"
)

// List enumerates the site and prints one URL per line, fetching none.
//
// An entry the warm command would skip is printed behind a # with its reason,
// which the list-file source ignores, so the output feeds a later run unchanged.
func List(ctx context.Context, rt *Runtime, args []string) int {
	fs := newFlagSet("list", "[-v N] list --base URL (--sitemap | --list-file FILE) [flags]")
	var site siteFlags
	site.register(fs)
	site.registerFirstTimeout(fs)
	var src sourceFlags
	src.register(fs)
	if status, done := parse(fs, args, false, rt); done {
		return status
	}
	base, err := site.baseURL(true)
	if err != nil {
		return usageError(fs, rt, err)
	}
	site.passwords(rt.Env)
	source, closer, err := src.open(rt, base, newClient(site.transport(rt), nil, site.firstTimeout, true, rt.Log))
	if err != nil {
		return usageError(fs, rt, err)
	}
	entries, err := source.Entries(ctx)
	_ = closer.Close()
	if err != nil {
		rt.Log.Error("enumerating", "err", err)
		return ExitUsage
	}
	status := ExitOK
	if len(entries) == 0 {
		rt.Log.Warn("nothing enumerated")
		status = ExitFailed
	}
	for _, e := range entries {
		if e.OutsideBase || (e.Violation != "" && !src.lax) {
			_, _ = fmt.Fprintf(rt.Stdout, "# %s\t%s\n", e.URL, e.Violation)
			status = ExitFailed
			continue
		}
		_, _ = fmt.Fprintln(rt.Stdout, e.URL)
	}
	return status
}
