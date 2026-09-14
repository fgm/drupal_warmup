package cmd

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"time"
)

// maxRedirects is what net/http enforces when left to itself,
// and what a CheckRedirect of our own has to enforce again.
const maxRedirects = 10

// basicAuth adds an Authorization header to every request.
type basicAuth struct {
	next http.RoundTripper
	pass string
	user string
}

// RoundTrip clones the request, as the RoundTripper contract asks, and authenticates it.
func (b *basicAuth) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.SetBasicAuth(b.user, b.pass)
	return b.next.RoundTrip(r)
}

// newJar builds an empty cookie jar.
//
// cookiejar.New returns a nil error unconditionally, read in net/http/cookiejar,
// so a non-nil one would mean something language has broken.
func newJar() http.CookieJar {
	jar, _ := cookiejar.New(nil)
	return jar
}

// newClient builds a client with one timeout and one redirect policy.
//
// Timeout and CheckRedirect are client fields,
// which is why each command builds its own clients on the shared transport.
func newClient(transport http.RoundTripper, jar http.CookieJar, timeout time.Duration, follow bool, log *slog.Logger) *http.Client {
	c := &http.Client{Jar: jar, Timeout: timeout, Transport: transport}
	if !follow {
		c.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		return c
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		log.Debug("redirect", "from", via[len(via)-1].URL, "to", req.URL)
		if len(via) >= maxRedirects {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return c
}
