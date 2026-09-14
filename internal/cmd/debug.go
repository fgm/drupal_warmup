package cmd

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// xdebugCookie is the cookie Xdebug reads its trigger from.
const xdebugCookie = "XDEBUG_SESSION"

// Debug fetches the given URLs one at a time with the Xdebug trigger set.
//
// A production warm never carries a debug trigger,
// which is why this is a command of its own rather than a flag on warm.
// Redirects are never followed, and stdout shows the status line,
// the response headers and the drained body length of each answer.
func Debug(ctx context.Context, rt *Runtime, args []string) int {
	fs := newFlagSet("debug", "[-v N] debug [--base URL] [flags] URL...")
	var site siteFlags
	site.register(fs)
	trigger := fs.String("trigger", "PHPSTORM", "The Xdebug trigger value, sent as the "+xdebugCookie+" cookie")
	if status, done := parse(fs, args, true, rt); done {
		return status
	}
	if fs.NArg() == 0 {
		return usageError(fs, rt, fmt.Errorf("no URL to fetch"))
	}
	base, err := site.baseURL(false)
	if err != nil {
		return usageError(fs, rt, err)
	}
	targets, err := resolveAll(fs.Args(), base)
	if err != nil {
		return usageError(fs, rt, err)
	}
	site.passwords(rt.Env)
	transport := site.transport(rt)
	jar := newJar()
	for _, t := range targets {
		jar.SetCookies(&url.URL{Scheme: t.Scheme, Host: t.Host, Path: "/"},
			[]*http.Cookie{{Name: xdebugCookie, Value: *trigger, Path: "/"}})
	}
	if status, done := site.loginIfAsked(ctx, rt, base, transport, jar); done {
		return status
	}
	client := newClient(transport, jar, site.timeout, false, rt.Log)
	status := ExitOK
	for _, t := range targets {
		if err := show(ctx, rt, client, t); err != nil {
			rt.Log.Error("fetching", "url", t, "err", err)
			status = ExitFailed
		}
	}
	return status
}

// resolveAll parses the operands, joining a relative one onto the base path.
func resolveAll(operands []string, base *url.URL) ([]*url.URL, error) {
	targets := make([]*url.URL, 0, len(operands))
	for _, op := range operands {
		u, err := url.Parse(op)
		if err != nil {
			return nil, fmt.Errorf("parsing %q: %w", op, err)
		}
		if !u.IsAbs() {
			if base == nil {
				return nil, fmt.Errorf("%q is relative and there is no --base", op)
			}
			rel := u
			u = base.JoinPath(rel.Path)
			if !strings.HasPrefix(u.Path, "/") {
				u.Path = "/" + u.Path
			}
			u.RawQuery = rel.RawQuery
		}
		targets = append(targets, u)
	}
	return targets, nil
}

// cookieNames lists the cookies' names for a debug log.
//
// Never the values: a session cookie's value is a credential,
// and logging it is what CodeQL's clear-text-logging query rejects.
func cookieNames(cs []*http.Cookie) []string {
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = c.Name
	}
	return names
}

// show fetches one URL and prints what came back.
func show(ctx context.Context, rt *Runtime, client *http.Client, u *url.URL) error {
	req := (&http.Request{Header: http.Header{}, Method: http.MethodGet, URL: u}).WithContext(ctx)
	rt.Log.Debug("request", "url", u, "cookies", cookieNames(client.Jar.Cookies(u)))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "GET %s\n%s %s\n", u, resp.Proto, resp.Status)
	for _, name := range slices.Sorted(maps.Keys(resp.Header)) {
		for _, v := range resp.Header[name] {
			fmt.Fprintf(&b, "%s: %s\n", name, v)
		}
	}
	fmt.Fprintf(&b, "\nbody: %d bytes\n\n", n)
	_, err = io.WriteString(rt.Stdout, b.String())
	return err
}
