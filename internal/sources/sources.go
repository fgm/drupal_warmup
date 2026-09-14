// Package sources enumerates the URLs to warm.
//
// A plain text list and an XML sitemap are the two sources,
// and neither is Drupal-specific: any site behind a sitemap can be enumerated.
// Every entry comes back as an absolute URL,
// marked where it falls outside the scope its source may speak for,
// so that the caller decides whether that is fatal.
package sources

import (
	"context"
	"net/url"
	"path"
	"strings"
)

// Entry is one URL to warm, with the outcome of its scope check.
type Entry struct {
	// OutsideBase marks an entry that is not under the base URL.
	// Nothing lifts it: credentials go with every request,
	// and the base is the host the user named.
	OutsideBase bool
	URL         *url.URL
	// Violation says why the entry is outside the scope its source may speak for,
	// and is empty when it is not.
	Violation string
}

// Source enumerates entries.
type Source interface {
	Entries(ctx context.Context) ([]Entry, error)
}

// baseScope is the scope a base URL speaks for: its path as a directory.
//
// https://example.com/blog speaks for https://example.com/blog/,
// so that a site under a prefix never claims its host root.
func baseScope(base *url.URL) *url.URL {
	scope := *base
	scope.Path = strings.TrimSuffix(base.Path, "/") + "/"
	scope.RawPath = ""
	scope.RawQuery = ""
	scope.Fragment = ""
	return &scope
}

// check classifies u against the scope of its source and against the base.
//
// The base check ignores the scheme: it exists so that credentials only ever
// reach the host the user named, and a scheme mismatch is the sitemap's
// scope violation, which Lax may lift and the redirect policy then sees.
func check(u, scope, base *url.URL) Entry {
	e := Entry{URL: u}
	switch {
	case !underHost(u, base):
		e.OutsideBase = true
		e.Violation = "outside base " + base.String()
	case !underScope(u, scope):
		e.Violation = "outside sitemap scope " + scope.String()
	}
	return e
}

// dirScope is the scope a sitemap speaks for: the directory it is served from.
//
// This is the sitemaps.org "Sitemap file location" rule:
// http://example.com/catalog/sitemap.xml may list http://example.com/catalog/...
// and nothing else, https://example.com/catalog/... included.
func dirScope(sitemap *url.URL) *url.URL {
	scope := *sitemap
	scope.Path = sitemap.Path[:strings.LastIndex(sitemap.Path, "/")+1]
	scope.RawPath = ""
	scope.RawQuery = ""
	scope.Fragment = ""
	return &scope
}

// hostScope is the scope a host's robots.txt vouches for: the whole host.
//
// The protocol's cross-submission rule: a sitemap named in robots.txt
// may list any URL of the host that named it.
func hostScope(u *url.URL) *url.URL {
	return &url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/"}
}

// joinBase joins a relative URL onto the base path, keeping its query.
//
// The join is onto the base path, never onto the host root:
// /fr/ under https://example.com/blog is https://example.com/blog/fr/.
func joinBase(base, rel *url.URL) *url.URL {
	u := *base
	u.Path = path.Join(base.Path, rel.Path)
	if strings.HasSuffix(rel.Path, "/") && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	u.RawPath = ""
	u.RawQuery = rel.RawQuery
	u.Fragment = ""
	return &u
}

// underHost reports whether u is on the base's host, under its path, whatever the scheme.
func underHost(u, base *url.URL) bool {
	if u.Host != base.Host {
		return false
	}
	p := u.Path
	if p == "" {
		p = "/"
	}
	return strings.HasPrefix(p, base.Path)
}

// underScope reports whether u is within a directory scope:
// same scheme, host and port, and a path under the scope's directory.
func underScope(u, scope *url.URL) bool {
	if u.Scheme != scope.Scheme || u.Host != scope.Host {
		return false
	}
	p := u.Path
	if p == "" {
		p = "/"
	}
	return strings.HasPrefix(p, scope.Path)
}
