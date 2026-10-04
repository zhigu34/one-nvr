package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/httpapi"
)

func TestFoundationHTTPRoutesAndImmutableChannelNumber(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: "http://nvr.example.com"})
	request := func(method, path, body, version string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://nvr.example.com"+path, bytes.NewBufferString(body))
		r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: admin.RawSession})
		r.Header.Set("Origin", "http://nvr.example.com")
		r.Header.Set("X-CSRF-Token", accounts.CSRF(admin.RawSession))
		r.Header.Set("If-Match", version)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/v1/site", "/api/v1/timezones", "/api/v1/channels", "/api/v1/capabilities", "/api/v1/operations/components", "/api/v1/audit-logs"} {
		if w := request("GET", path, "", ""); w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	var channelID string
	if err := db.Pool.QueryRow(context.Background(), "SELECT id FROM channels WHERE channel_no=1").Scan(&channelID); err != nil {
		t.Fatal(err)
	}
	if w := request("PATCH", "/api/v1/channels/"+channelID, `{"channel_name":"renamed","channel_no":2}`, `"1"`); w.Code != 422 {
		t.Fatal("channel number edit accepted", w.Code)
	}
	if w := request("PATCH", "/api/v1/channels/"+channelID, `{"channel_name":"门口"}`, `"1"`); w.Code != 200 {
		t.Fatal("rename rejected", w.Code, w.Body.String())
	}
	if w := request("PATCH", "/api/v1/site", `{"timezone":"Asia/Tokyo","channel_count":32}`, `"1"`); w.Code != 422 {
		t.Fatal("uncontrolled expansion accepted", w.Code)
	}
	if w := request("POST", "/api/v1/site/expand", "", `"1"`); w.Code != 200 {
		t.Fatal("expansion rejected", w.Code, w.Body.String())
	}
	if w := request("POST", "/api/v1/site/expand", "", `"1"`); w.Code != 409 {
		t.Fatal("stale expansion accepted", w.Code)
	}
}

func TestFutureModuleDirectHTTPGates(t *testing.T) {
	_, accounts, sites, admin := authFixture(t)
	for _, enabled := range []bool{false, true} {
		h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: "http://nvr.example.com", FrigateEnabled: enabled, OpenListEnabled: enabled})
		for _, path := range []string{"/api/v1/archive-tasks", "/api/v1/detection-rules"} {
			r := httptest.NewRequest("POST", "http://nvr.example.com"+path, bytes.NewBufferString(`{}`))
			r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: admin.RawSession})
			r.Header.Set("Origin", "http://nvr.example.com")
			r.Header.Set("X-CSRF-Token", accounts.CSRF(admin.RawSession))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := 409
			if enabled {
				want = 501
			}
			if w.Code != want {
				t.Fatalf("%s enabled=%v: %d %s", path, enabled, w.Code, w.Body.String())
			}
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || envelope.Error.Code == "" {
				t.Fatal("module error missing code", err)
			}
		}
	}
	if _, err := accounts.CreateUser(context.Background(), admin.Principal, auth.CreateUserInput{Username: "viewdiag", Password: testPassword, Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	viewer, err := accounts.Login(context.Background(), "viewdiag", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: "http://nvr.example.com"})
	for _, path := range []string{"/api/v1/operations/components", "/api/v1/audit-logs"} {
		r := httptest.NewRequest("GET", "http://nvr.example.com"+path, nil)
		r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: viewer.RawSession})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("viewer diagnostics allowed", path, w.Code)
		}
	}
}
