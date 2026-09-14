// Command drupal_warmup warms a site's caches after a cache rebuild.
//
// It enumerates the pages of a site, from its XML sitemap or from a list,
// and fetches them through the public edge so that visitors never pay the cold path.
// The first request is made alone, so that one request pays the rebuild
// rather than a herd of them; the rest run with bounded concurrency.
//
// The warming itself is not Drupal-specific: any site behind a sitemap qualifies.
// The login, which fills the dynamic page cache for one account,
// and the x-drupal-cache column of the report are.
//
// Usage:
//
//	drupal_warmup [-v N] <command> [flags]
//
// The commands are warm, list, debug and version; each takes -h.
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"

	"github.com/fgm/drupal_warmup/internal/cmd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	status := cmd.RealMain(ctx, os.Args, os.Environ(), os.Stdin, os.Stdout, os.Stderr,
		cmd.OSFS{}, http.DefaultTransport)
	// Not deferred: os.Exit runs no deferred call.
	stop()
	os.Exit(status)
}
