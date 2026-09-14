package cmd

import (
	"context"
	"net/http"
	"net/url"

	"github.com/fgm/drupal_warmup/internal/login"
)

// loginIfAsked logs in when --druser is set, and says whether the command must stop.
func (s *siteFlags) loginIfAsked(ctx context.Context, rt *Runtime, base *url.URL, transport http.RoundTripper, jar http.CookieJar) (int, bool) {
	if s.drUser == "" {
		return ExitOK, false
	}
	if base == nil {
		return usageErrorf(rt, "--druser needs --base"), true
	}
	d := login.Drupal{
		Base:     base,
		Client:   newClient(transport, jar, s.loginTimeout, false, rt.Log),
		Log:      rt.Log,
		Password: s.drPass,
		User:     s.drUser,
	}
	if err := d.Login(ctx); err != nil {
		rt.Log.Error("login failed", "user", s.drUser, "err", err)
		return ExitLogin, true
	}
	return ExitOK, false
}
