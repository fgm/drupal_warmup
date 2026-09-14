package sources

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// Sitemap errors.
var (
	// ErrNotSitemap reports a document that is neither a urlset nor a sitemapindex.
	ErrNotSitemap = errors.New("not a sitemap")
	// ErrStatus reports a sitemap that did not answer 200 after redirects.
	ErrStatus = errors.New("sitemap did not answer 200")
)

// Sitemap enumerates the XML sitemap of the base, discovered per the sitemaps.org protocol.
//
// Discovery takes every Sitemap: line of the host's robots.txt that names
// a sitemap under the base, else <base>/sitemap.xml,
// the only two mechanisms the protocol documents.
// An index is followed one level.
// A sitemap may only speak for URLs under its own location,
// so entries outside that scope are marked rather than dropped,
// and an index child outside it is only read when Lax is set.
// A child's entries are held to the index's scope, not the child's own:
// the index vouched for the child, as robots.txt vouches for a sitemap,
// and Hugo serves a default language's sitemap from /fr/ while its pages are not.
type Sitemap struct {
	// configuration
	Base *url.URL
	Lax  bool

	// services
	Client *http.Client
	Log    *slog.Logger
}

// located is a sitemap URL with the scope it speaks for,
// nil where the scope is its own directory, known once redirects are followed.
type located struct {
	scope *url.URL
	url   *url.URL
}

// sitemapDoc is the union of a urlset and a sitemapindex; XMLName tells which.
type sitemapDoc struct {
	XMLName  xml.Name
	Sitemaps []locElem `xml:"sitemap"`
	URLs     []locElem `xml:"url"`
}

type locElem struct {
	Loc string `xml:"loc"`
}

// Entries fetches the sitemap, its index children if any, and classifies every location.
//
// The first request is the sitemap itself, or robots.txt where the host has one,
// so a caller wanting one request alone before any fan-out gets it here.
func (s *Sitemap) Entries(ctx context.Context) ([]Entry, error) {
	base := baseScope(s.Base)
	maps, err := s.discover(ctx, base)
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, m := range maps {
		es, err := s.read(ctx, m, base)
		if err != nil {
			return nil, err
		}
		entries = append(entries, es...)
	}
	return entries, nil
}

// discover lists the sitemaps to read, from robots.txt or the default path.
func (s *Sitemap) discover(ctx context.Context, base *url.URL) ([]located, error) {
	robots := &url.URL{Scheme: s.Base.Scheme, Host: s.Base.Host, Path: "/robots.txt"}
	body, _, err := s.fetch(ctx, robots)
	if err != nil {
		if !errors.Is(err, ErrStatus) {
			return nil, err
		}
		s.Log.Debug("no robots.txt", "url", robots, "err", err)
	}
	var maps []located
	for line := range strings.Lines(string(body)) {
		key, value, found := strings.Cut(line, ":")
		if !found || !strings.EqualFold(strings.TrimSpace(key), "sitemap") {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(value))
		if err != nil || !underScope(u, base) {
			s.Log.Debug("ignoring robots.txt sitemap", "line", strings.TrimSpace(line))
			continue
		}
		maps = append(maps, located{scope: hostScope(u), url: u})
	}
	if len(maps) == 0 {
		maps = append(maps, located{url: joinPath(s.Base, "sitemap.xml")})
	}
	return maps, nil
}

// fetch returns the body and the final URL of a document that answered 200.
func (s *Sitemap) fetch(ctx context.Context, u *url.URL) ([]byte, *url.URL, error) {
	req := (&http.Request{Header: http.Header{}, Method: http.MethodGet, URL: u}).WithContext(ctx)
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching %s: %w", u, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("%s: %w: %s", resp.Request.URL, ErrStatus, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", resp.Request.URL, err)
	}
	return body, resp.Request.URL, nil
}

// parse decodes a sitemap document and reports whether it is an index.
func parse(body []byte, from *url.URL) (*sitemapDoc, bool, error) {
	var doc sitemapDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, false, fmt.Errorf("%s: %w: %w", from, ErrNotSitemap, err)
	}
	switch doc.XMLName.Local {
	case "sitemapindex":
		return &doc, true, nil
	case "urlset":
		return &doc, false, nil
	default:
		return nil, false, fmt.Errorf("%s: %w: root element %q", from, ErrNotSitemap, doc.XMLName.Local)
	}
}

// read fetches one sitemap and classifies its locations, one level of index included.
func (s *Sitemap) read(ctx context.Context, m located, base *url.URL) ([]Entry, error) {
	body, final, err := s.fetch(ctx, m.url)
	if err != nil {
		return nil, err
	}
	scope := m.scope
	if scope == nil {
		scope = dirScope(final)
	}
	doc, isIndex, err := parse(body, final)
	if err != nil {
		return nil, err
	}
	if !isIndex {
		return s.entries(doc, scope, base, final)
	}
	var entries []Entry
	for _, child := range doc.Sitemaps {
		cu, err := url.Parse(strings.TrimSpace(child.Loc))
		if err != nil {
			return nil, fmt.Errorf("%s: child location %q: %w", final, child.Loc, err)
		}
		e := check(cu, scope, base)
		if e.OutsideBase || (e.Violation != "" && !s.Lax) {
			entries = append(entries, e)
			continue
		}
		if e.Violation != "" {
			s.Log.Warn("reading out-of-scope sitemap", "url", cu, "violation", e.Violation)
		}
		cbody, cfinal, err := s.fetch(ctx, cu)
		if err != nil {
			return nil, err
		}
		cdoc, nested, err := parse(cbody, cfinal)
		if err != nil {
			return nil, err
		}
		if nested {
			return nil, fmt.Errorf("%s: %w: an index inside an index", cfinal, ErrNotSitemap)
		}
		es, err := s.entries(cdoc, scope, base, cfinal)
		if err != nil {
			return nil, err
		}
		entries = append(entries, es...)
	}
	return entries, nil
}

// entries classifies the locations of a urlset.
func (s *Sitemap) entries(doc *sitemapDoc, scope, base, from *url.URL) ([]Entry, error) {
	entries := make([]Entry, 0, len(doc.URLs))
	for _, loc := range doc.URLs {
		u, err := url.Parse(strings.TrimSpace(loc.Loc))
		if err != nil {
			return nil, fmt.Errorf("%s: location %q: %w", from, loc.Loc, err)
		}
		entries = append(entries, check(u, scope, base))
	}
	s.Log.Info("sitemap read", "url", from, "entries", len(entries))
	return entries, nil
}
