package integration

import (
	"github.com/zhigu34/one-nvr/internal/httpapi"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTimezoneReferenceAvailableBeforeLogin(t *testing.T) {
	_, accounts, sites, _ := authFixture(t)
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: "http://nvr.example.com"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://nvr.example.com/api/v1/timezones", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Asia/Shanghai") {
		t.Fatalf("initialization cannot choose timezone: %d %s", w.Code, w.Body.String())
	}
	for _, s := range []string{"master_key", "setup_token", "csrf_key", "password"} {
		if strings.Contains(w.Body.String(), s) {
			t.Fatal("private value in public reference")
		}
	}
}
