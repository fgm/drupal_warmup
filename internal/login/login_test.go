package login_test

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/fgm/drupal_warmup/internal/login"
)

const form = `<!DOCTYPE html><html><body>
<form class="user-login-form" data-drupal-selector="user-login-form" action="/user/login" method="post" id="user-login-form" accept-charset="UTF-8">
<div class="js-form-item form-item">
<label for="edit-name" class="js-form-required form-required">Username</label>
<input autocomplete="username" type="text" id="edit-name" name="name" value="" size="60" maxlength="60" class="form-text required" required="required">
</div>
<input type="password" id="edit-pass" name="pass" size="60" maxlength="128" class="form-text required" required="required" />
<input autocomplete="off" data-drupal-selector="form-abc" type="hidden" name="form_build_id" value="form-abc" />
<input data-drupal-selector="edit-user-login-form" type="hidden" name="form_id" value="user_login_form" />
<input data-drupal-selector="edit-submit" type="submit" id="edit-submit" name="op" value="Log in" class="button js-form-submit form-submit" />
</form></body></html>`

const session = "SESS0123456789abcdef0123456789abcdef"

// server answers Drupal's login form under any base path.
//
// A POST carrying the hidden fields as served and the right password
// answers 303 with a session cookie; anything else answers the form again.
// truncate names the method whose answer is cut short of its Content-Length.
func server(t *testing.T, page string, status int, truncate string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == truncate {
			w.Header().Set("Content-Length", "1000")
			_, _ = w.Write([]byte(page[:10]))
			return
		}
		if r.Method == http.MethodGet {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(page))
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		ok := r.PostForm.Get("form_build_id") == "form-abc" &&
			r.PostForm.Get("form_id") == "user_login_form" &&
			r.PostForm.Get("op") == "Log in" &&
			r.PostForm.Get("name") == "warmer" &&
			r.PostForm.Get("pass") == "pass"
		if !ok {
			_, _ = w.Write([]byte(page))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: session, Value: "s", Path: "/"})
		http.Redirect(w, r, "/user/1", http.StatusSeeOther)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func client(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func TestLogin(t *testing.T) {
	cases := []struct {
		name     string
		basePath string
		page     string
		status   int
		password string
		noJar    bool
		truncate string
		wantErr  error
		anyErr   bool
	}{
		{name: "form cut short", page: form, status: http.StatusOK, truncate: http.MethodGet, wantErr: login.ErrForm},
		{name: "answer cut short", page: form, status: http.StatusOK, password: "pass", truncate: http.MethodPost, anyErr: true},
		{name: "success", page: form, status: http.StatusOK, password: "pass"},
		{name: "success under a base path", basePath: "/blog", page: form, status: http.StatusOK, password: "pass"},
		{name: "wrong password", page: form, status: http.StatusOK, password: "wrong", wantErr: login.ErrFailed},
		{name: "form without form_id", page: `<input name="name"><input name="pass"><input name="form_build_id" value="x"><input name="op" value="Log in">`, status: http.StatusOK, wantErr: login.ErrForm},
		{name: "form page not found", page: form, status: http.StatusNotFound, wantErr: login.ErrForm},
		{name: "client without a jar", page: form, status: http.StatusOK, noJar: true, wantErr: login.ErrNoJar},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := server(t, c.page, c.status, c.truncate)
			base, err := url.Parse(srv.URL + c.basePath)
			if err != nil {
				t.Fatal(err)
			}
			cl := client(t)
			if c.noJar {
				cl.Jar = nil
			}
			d := login.Drupal{Base: base, Client: cl, Log: slog.New(slog.DiscardHandler), Password: c.password, User: "warmer"}
			err = d.Login(t.Context())
			if c.anyErr {
				if err == nil {
					t.Fatal("no error")
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if c.wantErr != nil {
				return
			}
			for _, ck := range cl.Jar.Cookies(base) {
				if ck.Name == session {
					return
				}
			}
			t.Fatal("no session cookie in the jar after a successful login")
		})
	}
}

func TestLoginPostError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(form))
			return
		}
		// Break the POST at the transport: hijack and close without a response.
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	d := login.Drupal{Base: base, Client: client(t), Log: slog.New(slog.DiscardHandler), Password: "pass", User: "warmer"}
	if err := d.Login(t.Context()); err == nil {
		t.Fatal("no error when the login POST fails")
	}
}

func TestLoginServerDown(t *testing.T) {
	srv := server(t, form, http.StatusOK, "")
	base, _ := url.Parse(srv.URL)
	srv.Close()
	d := login.Drupal{Base: base, Client: client(t), Log: slog.New(slog.DiscardHandler), Password: "pass", User: "warmer"}
	if err := d.Login(t.Context()); err == nil {
		t.Fatal("no error from a closed server")
	}
}
