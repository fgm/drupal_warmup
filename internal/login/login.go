// Package login signs into a Drupal site through its user login form.
//
// This is the Drupal-specific part of the warmer.
// It reads form_build_id, form_id and op from /user/login under the base,
// posts the credentials with them,
// and takes a SESS or SSESS cookie in the jar as success.
package login

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// Login errors.
var (
	// ErrFailed reports credentials the site did not accept.
	ErrFailed = errors.New("login failed")
	// ErrForm reports a login page without the inputs Drupal's form carries.
	ErrForm = errors.New("login form not recognized")
	// ErrNoJar reports a client that cannot hold the session cookie.
	ErrNoJar = errors.New("client has no cookie jar")
)

// sessionCookie matches Drupal's session cookie: SESS over HTTP, SSESS over HTTPS.
var sessionCookie = regexp.MustCompile(`^S?SESS[0-9a-fA-F]{32}$`)

// Drupal logs one account into one site.
type Drupal struct {
	// configuration
	Base     *url.URL
	Password string
	User     string

	// services
	// Client carries the jar the session lands in.
	// It need not follow redirects: cookies are stored before redirect handling,
	// so the 303 a successful login answers is enough.
	Client *http.Client
	Log    *slog.Logger
}

// Login fetches the form, posts the credentials and checks the jar for a session.
func (d *Drupal) Login(ctx context.Context) error {
	if d.Client.Jar == nil {
		return ErrNoJar
	}
	// JoinPath keeps the path relative when the base path is empty,
	// and a relative request-target is rejected on the wire, so it is rooted.
	formURL := d.Base.JoinPath("user/login")
	if !strings.HasPrefix(formURL.Path, "/") {
		formURL.Path = "/" + formURL.Path
	}
	form, err := d.readForm(ctx, formURL)
	if err != nil {
		return err
	}
	if err := d.post(ctx, formURL, form); err != nil {
		return err
	}
	for _, c := range d.Client.Jar.Cookies(d.Base) {
		if sessionCookie.MatchString(c.Name) {
			d.Log.Info("logged in", "user", d.User, "cookie", c.Name)
			return nil
		}
	}
	return fmt.Errorf("%w: no session cookie for %s", ErrFailed, d.User)
}

// post submits the form and drains the answer, whose status is logged, not judged:
// the jar decides.
func (d *Drupal) post(ctx context.Context, formURL *url.URL, form url.Values) error {
	form.Set("name", d.User)
	form.Set("pass", d.Password)
	encoded := form.Encode()
	req := (&http.Request{
		Body:          io.NopCloser(strings.NewReader(encoded)),
		ContentLength: int64(len(encoded)),
		Header:        http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
		Method:        http.MethodPost,
		URL:           formURL,
	}).WithContext(ctx)
	resp, err := d.Client.Do(req)
	if err != nil {
		return fmt.Errorf("posting login: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return fmt.Errorf("reading login answer: %w", err)
	}
	// Drupal answers 303 to the account page on success
	// and 200 with the form again on failure.
	d.Log.Info("login posted", "status", resp.StatusCode, "location", resp.Header.Get("Location"))
	return nil
}

// readForm fetches the login page and returns the hidden fields to post back.
func (d *Drupal) readForm(ctx context.Context, formURL *url.URL) (url.Values, error) {
	req := (&http.Request{Header: http.Header{}, Method: http.MethodGet, URL: formURL}).WithContext(ctx)
	resp, err := d.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching login form: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s answered %s", ErrForm, formURL, resp.Status)
	}
	form, err := scrape(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", formURL, err)
	}
	return form, nil
}

// scrape collects the inputs of Drupal's login form.
//
// name and pass must exist and are posted with the credentials;
// form_build_id, form_id and op are posted back as found.
func scrape(page io.Reader) (url.Values, error) {
	wanted := map[string]bool{"form_build_id": true, "form_id": true, "name": true, "op": true, "pass": true}
	form := url.Values{}
	z := html.NewTokenizer(page)
	for {
		switch z.Next() {
		case html.ErrorToken:
			if !errors.Is(z.Err(), io.EOF) {
				return nil, fmt.Errorf("%w: %w", ErrForm, z.Err())
			}
			for field := range wanted {
				if !form.Has(field) {
					return nil, fmt.Errorf("%w: no %s input", ErrForm, field)
				}
			}
			return form, nil
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			if !strings.EqualFold(tok.Data, "input") {
				continue
			}
			name, value := attrs(tok)
			if wanted[name] {
				form.Set(name, value)
			}
		}
	}
}

// attrs returns the name and value attributes of a token.
//
// Attribute namespaces are not looked at: the tokenizer never sets them,
// only the parser does.
func attrs(tok html.Token) (name, value string) {
	for _, a := range tok.Attr {
		switch a.Key {
		case "name":
			name = a.Val
		case "value":
			value = a.Val
		}
	}
	return name, value
}
