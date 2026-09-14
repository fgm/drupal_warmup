# drupal_warmup

[![GoDoc](https://pkg.go.dev/badge/github.com/fgm/drupal_warmup)](https://pkg.go.dev/github.com/fgm/drupal_warmup)
[![CI](https://github.com/fgm/drupal_warmup/actions/workflows/tests.yml/badge.svg)](https://github.com/fgm/drupal_warmup/actions/workflows/tests.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/fgm/drupal_warmup/badge)](https://scorecard.dev/viewer/?uri=github.com/fgm/drupal_warmup)

Warm a site's caches after a cache rebuild,
so that visitors never pay the cold path.

The tool enumerates the pages of a site, from its XML sitemap or from a list,
and fetches them through the public edge.
The first request is made alone,
so that one request pays the container rebuild rather than a herd of them;
the rest run with bounded concurrency, as many times as asked.

## Install

```console
$ go install github.com/fgm/drupal_warmup@latest
```

Run the installed binary rather than `go run`:
`go run` collapses every non-zero exit status to 1,
and the statuses below are the point.

## Usage

```console
$ drupal_warmup [-v N] <command> [flags]
```

- `warm`: enumerate the site and fetch every page. The production path.
- `list`: enumerate the site and print the URLs, fetching none. The dry run.
- `debug`: fetch URLs one at a time with the Xdebug trigger set, never following redirects.
- `version`: print the version.

Each command takes `-h`.
`-v` goes before the command: `0` is silent, `1` warnings, `2` progress, `3` every request.
Standard output carries only the command's output; logs go to standard error.

The typical step after a Drupal cache rebuild:

```console
$ drupal_warmup warm --base=https://example.com --sitemap -c=4 --runs=2
```

The second run is the proof that the first one warmed:
every line of it should read `HIT`.

### The site

`--base` is the site: scheme, host and, for a site that is not rooted,
its path prefix, as in `https://example.com/blog`.
Everything relative joins onto that path, never onto the host root.

### Sources

- `--sitemap` discovers the site's XML sitemap as the sitemaps.org protocol says:
  the `Sitemap:` lines of the host's `robots.txt` that name a sitemap under the base,
  else `<base>/sitemap.xml`.
  A sitemap index is followed one level.
- `--list-file` reads URLs or paths, one per line, `-` for standard input.
  Blank lines and lines starting with `#` are skipped.

The two are exclusive, and one is required.

A sitemap may only list URLs under its own location,
the protocol's "Sitemap file location" rule:
same scheme, host and port, path under the sitemap's directory.
An entry outside that scope is reported and, without `--lax`, not fetched.
`--lax` fetches it anyway and only reports the violation.
An entry on another host, or outside the base path, is never fetched:
credentials go with every request, and the base is the host you named.

### Redirects

A listed URL that answers a redirect means the enumeration is stale,
so `warm` reports it with its `Location` and counts it as a failure.
`--follow` warms the target instead; the line still shows where it went.

### The report

`warm` writes one tab-separated line per fetch on standard output:

```
status  cache  seconds  URL  note
```

The cache column is `x-drupal-cache` for an anonymous warm
and `x-drupal-dynamic-cache` for a logged-in one,
since a session keeps a request out of the page cache.

The note is the `Location` of a redirect, the reason an entry was skipped,
the error of a failed request, or `-`.
Piping through `awk -F'\t' '$2 != "HIT"'` after the second run
lists what did not warm.

### Exit status

- `0`: every listed URL answered 2xx in the last run.
- `1`: in the last run, a URL failed or was redirected without `--follow`,
  or was out of scope without `--lax`; or nothing was enumerated.
- `2`: bad command line, or the sitemap did not answer 200.
- `3`: the Drupal login failed.

Only the last run decides:
the first run after a cache rebuild is the cold pass and may time out.

### Credentials

`--bauser` and `--bapass` add web server basic auth to every request.
`--druser` and `--drpass` log into Drupal first,
so that the pages warmed are the dynamic page cache of that account
rather than the page cache anonymous visitors hit.
Leave `--druser` empty for anonymous warming.

A logged-in warm then runs an anonymous stage over the same URLs,
`--runs` times as well:
the session kept those requests out of the page cache,
and the anonymous stage fills it from the fragments the first stage rendered.
`--no-anon` skips that stage.

Set `DRUPAL_WARMUP_BAPASS` and `DRUPAL_WARMUP_DRPASS` rather than passing
the passwords on the command line, where `ps` and shell history see them.
See [SECURITY.md](SECURITY.md).

[envrun](https://github.com/fgm/envrun) is one way to keep them out of the shell
altogether: put them in a `.env` file the shell never reads,

```
DRUPAL_WARMUP_DRPASS=pass
```

and run the warmer through it:

```console
$ envrun drupal_warmup warm --base=https://example.com --sitemap --druser=warmer --runs=2
```

Drupal's own `basic_auth` module bypasses the page cache for credentialed requests,
so `--bauser` against a site using it warms nothing for anonymous visitors.
Basic auth at the web server is unaffected.

## What is Drupal-specific

Not Drupal-specific: the enumeration, the serialized first fetch,
the fan-out, runs, redirect detection and the report.
Any site behind a sitemap can be warmed.

Drupal-specific: the login, which scrapes the `user/login` form
for `form_build_id` and `form_id` and looks for a `SESS` or `SSESS` cookie;
the cache column of the report;
and the reason the tool exists, which is that the first requests after
`drush cache:rebuild` pay the container rebuild, the config reload
and the plugin rediscovery.
The page cache keeps entries until a tag invalidates them,
so warming is needed after a rebuild, not on a schedule.

## Licence

GPL-3.0, see [LICENSE](LICENSE).
