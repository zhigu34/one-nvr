package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/httpapi"
	"github.com/zhigu34/one-nvr/internal/tlsmanager"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTLSHTTPDirectoryPendingAndWatchPermissions(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	ctx := context.Background()
	input := t.TempDir()
	svc := tlsmanager.New(db, accounts, t.TempDir(), "http://nvr.example.com", input)
	if err := svc.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: "http://nvr.example.com", TLS: svc})
	request := func(method, path, body, version, raw string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://nvr.example.com"+path, bytes.NewBufferString(body))
		r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: raw})
		r.Header.Set("Origin", "http://nvr.example.com")
		r.Header.Set("X-CSRF-Token", accounts.CSRF(raw))
		r.Header.Set("If-Match", version)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	base := "/api/v1/settings/tls"
	w := request("GET", base, "", "", admin.RawSession)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("TLS state missing/no-store", w.Code)
	}
	var state struct {
		Data tlsmanager.State `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Data.Source != "directory" || !state.Data.AutoApply || state.Data.ActiveID != nil || state.Data.CheckState != "pending" {
		t.Fatal("first invalid input pretends ready")
	}
	for _, body := range []string{`{}`, `{"auto_apply":null}`} {
		if w := request("PATCH", base+"/watch", body, `"1"`, admin.RawSession); w.Code != 422 {
			t.Fatal("missing switch changed setting", w.Code)
		}
	}
	if w := request("PATCH", base+"/watch", `{"auto_apply":false}`, `"1"`, admin.RawSession); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := request("PATCH", base+"/watch", `{"auto_apply":true}`, `"1"`, admin.RawSession); w.Code != 409 {
		t.Fatal("stale watch update", w.Code)
	}
	if w := request("POST", base+"/check", "", "", admin.RawSession); w.Code != 202 {
		t.Fatal("manual check not queued", w.Body.String())
	}
	chain, key := tlsPair(t, 1)
	body, _ := json.Marshal(map[string]string{"fullchain_pem": string(chain), "private_key_pem": string(key)})
	if w := request("POST", base+"/import", string(body), "", admin.RawSession); w.Code != 409 {
		t.Fatal("directory mode accepted manual upload", w.Code)
	}
	if _, err := accounts.CreateUser(ctx, admin.Principal, auth.CreateUserInput{Username: "tlsviewer", Password: testPassword, Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	viewer, err := accounts.Login(ctx, "tlsviewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path, body, version string }{{"GET", base, "", ""}, {"POST", base + "/import", string(body), ""}, {"POST", base + "/check", "", ""}, {"PATCH", base + "/watch", `{"auto_apply":true}`, `"2"`}, {"POST", base + "/rollback", "", `"2"`}} {
		if w := request(tc.method, tc.path, tc.body, tc.version, viewer.RawSession); w.Code != 403 {
			t.Fatalf("viewer TLS %s %d", tc.method, w.Code)
		}
	}
}
