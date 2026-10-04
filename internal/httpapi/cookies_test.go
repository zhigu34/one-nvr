package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestProtocolCookiesCannotOverlayEachOther(t *testing.T) {
	for _, secure := range []bool{false, true} {
		r := &router{secure: secure}
		w := httptest.NewRecorder()
		r.setCookie(w, sessionCookie, "fixture-session", 3600)
		r.setCookie(w, preAuthCookie, "fixture-preauth", 600)
		cookies := w.Result().Cookies()
		for i, base := range []string{sessionCookie, preAuthCookie} {
			want := base
			if secure {
				want = "__Host-" + base
			}
			c := cookies[i]
			if c.Name != want || c.Secure != secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" {
				t.Fatalf("secure=%v cookie name=%q, want %q with host-only protections", secure, c.Name, want)
			}
		}
	}
}
