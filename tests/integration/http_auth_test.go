package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/httpapi"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPAndHTTPSCookiesAndCSRF(t *testing.T) {
	db, accounts, sites, _ := authFixture(t)
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			origin := scheme + "://nvr.example.com"
			handler := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: origin})
			start := httptest.NewRecorder()
			handler.ServeHTTP(start, httptest.NewRequest("GET", origin+"/api/v1/setup/status", nil))
			if start.Code != 200 {
				t.Fatal(start.Body.String())
			}
			var handshake struct {
				Data struct {
					CSRF string `json:"csrf_token"`
				}
			}
			if err := json.Unmarshal(start.Body.Bytes(), &handshake); err != nil {
				t.Fatal(err)
			}
			post := func(path, body, from, csrf string, cookies []*http.Cookie) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", origin+path, bytes.NewBufferString(body))
				r.Header.Set("Origin", from)
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-CSRF-Token", csrf)
				if scheme == "https" {
					r.Header.Set("X-Forwarded-Proto", "http")
				} else {
					r.Header.Set("X-Forwarded-Proto", "https")
				}
				for _, c := range cookies {
					r.AddCookie(c)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			body := `{"username":"admin","password":"` + testPassword + `"}`
			if w := post("/api/v1/auth/login", body, origin, "", start.Result().Cookies()); w.Code != 403 {
				t.Fatalf("login missing csrf %d", w.Code)
			}
			if w := post("/api/v1/auth/login", body, "https://attacker.example", handshake.Data.CSRF, start.Result().Cookies()); w.Code != 403 {
				t.Fatal("foreign origin allowed")
			}
			login := post("/api/v1/auth/login", body, origin, handshake.Data.CSRF, start.Result().Cookies())
			if login.Code != 200 {
				t.Fatalf("login %d %s", login.Code, login.Body.String())
			}
			cookies := login.Result().Cookies()
			var session *http.Cookie
			for _, c := range cookies {
				if c.Name == "one_nvr_session" {
					session = c
				}
			}
			if session == nil || session.Secure != (scheme == "https") || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode || session.Path != "/" {
				t.Fatal("invalid session cookie or trusted forged forwarded proto")
			}
			if login.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("auth response cacheable")
			}
			for _, secret := range []string{session.Value, "argon2id", "password_hash", "setup_token", "master_key", "digest"} {
				if strings.Contains(login.Body.String(), secret) {
					t.Fatalf("secret in login DTO: %s", secret)
				}
			}
			var dto struct {
				Data struct {
					CSRF string `json:"csrf_token"`
				}
			}
			if err := json.Unmarshal(login.Body.Bytes(), &dto); err != nil {
				t.Fatal(err)
			}
			if w := post("/api/v1/auth/logout", `{}`, origin, "wrong", cookies); w.Code != 403 {
				t.Fatal("bad session csrf accepted")
			}
			if w := post("/api/v1/auth/logout", `{}`, "", dto.Data.CSRF, cookies); w.Code != 403 {
				t.Fatal("missing origin accepted")
			}
			if w := post("/api/v1/auth/logout", `{}`, origin, dto.Data.CSRF, cookies); w.Code != 200 {
				t.Fatalf("logout %d %s", w.Code, w.Body.String())
			}
			me := httptest.NewRecorder()
			req := httptest.NewRequest("GET", origin+"/api/v1/auth/me", nil)
			req.AddCookie(session)
			handler.ServeHTTP(me, req)
			if me.Code != 401 {
				t.Fatal("revoked cookie accepted")
			}
		})
	}
	_ = db
}

func TestOwnGrantChangeRespondsSuccessAndRevokesSession(t *testing.T) {
	_, accounts, sites, admin := authFixture(t)
	origin := "http://nvr.example.com"
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: origin})
	req := httptest.NewRequest("PUT", origin+"/api/v1/users/"+string(admin.User.ID)+"/channel-grants", bytes.NewBufferString(`{"grants":[]}`))
	req.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: admin.RawSession})
	req.Header.Set("Origin", origin)
	req.Header.Set("X-CSRF-Token", accounts.CSRF(admin.RawSession))
	req.Header.Set("If-Match", `"1"`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("committed own-grant change reported %d %s", w.Code, w.Body.String())
	}
	me := httptest.NewRecorder()
	req = httptest.NewRequest("GET", origin+"/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: admin.RawSession})
	h.ServeHTTP(me, req)
	if me.Code != 401 {
		t.Fatal("old own-grant session still valid")
	}
}
func TestViewerDirectHTTPConfigurationRejected(t *testing.T) {
	_, accounts, sites, admin := authFixture(t)
	ctx := context.Background()
	if _, err := accounts.CreateUser(ctx, admin.Principal, auth.CreateUserInput{Username: "viewer", Password: testPassword, Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	viewer, err := accounts.Login(ctx, "viewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://nvr.example.com"
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: origin})
	req := httptest.NewRequest("POST", origin+"/api/v1/users", bytes.NewBufferString(`{"username":"hacker","password":"valid-password-12345","role":"admin"}`))
	req.Header.Set("Origin", origin)
	req.Header.Set("X-CSRF-Token", accounts.CSRF(viewer.RawSession))
	req.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: viewer.RawSession})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("viewer direct creation status %d", w.Code)
	}
}

func TestInvalidVersionAndUnknownFieldsCannotMutateUser(t *testing.T) {
	_, accounts, sites, admin := authFixture(t)
	origin := "http://nvr.example.com"
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: origin})
	for _, test := range []struct{ version, body string }{{`"+1"`, `{"role":"admin"}`}, {`"01"`, `{"role":"admin"}`}, {`W/"1"`, `{"role":"admin"}`}, {`"1"`, `{"role":"admin","password_hash":"fake"}`}} {
		req := httptest.NewRequest("PATCH", origin+"/api/v1/users/"+string(admin.User.ID), bytes.NewBufferString(test.body))
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", accounts.CSRF(admin.RawSession))
		req.Header.Set("If-Match", test.version)
		req.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: admin.RawSession})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 422 {
			t.Fatalf("invalid version/body accepted: %s status %d", test.version, w.Code)
		}
	}
}

func TestMalformedJSONDistinguishedFromInvalidFields(t *testing.T) {
	_, accounts, sites, _ := authFixture(t)
	origin := "http://nvr.example.com"
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: origin})
	start := httptest.NewRecorder()
	h.ServeHTTP(start, httptest.NewRequest("GET", origin+"/api/v1/setup/status", nil))
	var handshake struct {
		Data struct {
			CSRF string `json:"csrf_token"`
		}
	}
	if err := json.Unmarshal(start.Body.Bytes(), &handshake); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body string
		want int
	}{{`{"username":`, 400}, {``, 400}, {`{} {}`, 400}, {`{"unknown":true}`, 422}, {`{"username":123}`, 422}, {`{"username":"` + strings.Repeat("x", 70<<10) + `"}`, 413}} {
		req := httptest.NewRequest("POST", origin+"/api/v1/auth/login", bytes.NewBufferString(tc.body))
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", handshake.Data.CSRF)
		for _, cookie := range start.Result().Cookies() {
			req.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("malformed JSON status=%d want=%d", w.Code, tc.want)
		}
	}
}

func TestMalformedAccountFloodDoesNotBlockAnotherClient(t *testing.T) {
	_, accounts, sites, _ := authFixture(t)
	origin := "http://nvr.example.com"
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: origin})
	cookie, token, err := accounts.PreAuthToken()
	if err != nil {
		t.Fatal(err)
	}
	for n := range 1025 {
		name := fmt.Sprintf("invalid?%d", n)
		remote := "192.0.2.1:1234"
		if n == 1024 {
			name = "admin"
			remote = "192.0.2.99:1234"
		}
		body, _ := json.Marshal(map[string]string{"username": name, "password": testPassword})
		req := httptest.NewRequest("POST", origin+"/api/v1/auth/login", bytes.NewReader(body))
		req.RemoteAddr = remote
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", token)
		req.AddCookie(&http.Cookie{Name: "one_nvr_preauth", Value: cookie})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if n == 1024 && w.Code != 200 {
			t.Fatalf("unrelated valid login blocked after account flood: %d", w.Code)
		}
	}
}
func TestProtocolSwitchRevokesOldSessionsPermanently(t *testing.T) {
	_, accounts, sites, login := authFixture(t)
	for index, origin := range []string{"http://nvr.example.com", "https://nvr.example.com", "http://nvr.example.com"} {
		h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: origin})
		req := httptest.NewRequest("GET", origin+"/api/v1/auth/me", nil)
		req.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: login.RawSession})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		want := 401
		if index == 0 {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("protocol switch %d status %d want %d", index, w.Code, want)
		}
	}
}
