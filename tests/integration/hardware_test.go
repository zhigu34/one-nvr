package integration

import (
	"context"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/httpapi"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHardwareReportScopeAndDisabledStatus(t *testing.T) {
	_, accounts, sites, admin := authFixture(t)
	ctx := context.Background()
	_, e := accounts.CreateUser(ctx, admin.Principal, auth.CreateUserInput{Username: "hardwareviewer", Password: testPassword, Role: "viewer"})
	if e != nil {
		t.Fatal(e)
	}
	viewer, e := accounts.Login(ctx, "hardwareviewer", testPassword)
	if e != nil {
		t.Fatal(e)
	}
	for _, on := range []bool{false, true} {
		h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: "http://nvr.example.com", FrigateEnabled: on})
		for _, a := range []struct {
			raw    string
			status int
		}{{viewer.RawSession, 403}, {admin.RawSession, map[bool]int{false: 409, true: 503}[on]}} {
			r := httptest.NewRequest("GET", "http://nvr.example.com/api/v1/settings/hardware", nil)
			r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: a.raw})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != a.status {
				t.Fatalf("hardware report status %d expected %d", w.Code, a.status)
			}
		}
	}
}
