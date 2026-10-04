package integration

import (
	"bytes"
	"fmt"
	"github.com/zhigu34/one-nvr/internal/httpapi"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGatewayClientIPRequiresPrivateProxyToken(t *testing.T) {
	_, accounts, sites, _ := authFixture(t)
	proxyToken := strings.Repeat("c", 64)
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: "http://nvr.example.com", TrustedProxyToken: proxyToken})
	cookie, csrf, err := accounts.PreAuthToken()
	if err != nil {
		t.Fatal(err)
	}
	request := func(token, client string) int {
		q := httptest.NewRequest("POST", "http://nvr.example.com/api/v1/auth/login", bytes.NewBufferString(`{"username":"invalid?","password":"unused"}`))
		q.RemoteAddr = "192.0.2.1:4000"
		q.Header.Set("Origin", "http://nvr.example.com")
		q.Header.Set("X-CSRF-Token", csrf)
		q.Header.Set("X-One-NVR-Proxy-Token", token)
		q.Header.Set("X-One-NVR-Client-IP", client)
		q.AddCookie(&http.Cookie{Name: "one_nvr_preauth", Value: cookie})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, q)
		return w.Code
	}
	for n := range 61 {
		got := request("forged", fmt.Sprintf("198.51.100.%d", n+1))
		want := 422
		if n == 60 {
			want = 429
		}
		if got != want {
			t.Fatalf("forged proxy header bypassed limiter: %d want%d", got, want)
		}
	}
	if got := request(proxyToken, "198.51.100.200"); got != 422 {
		t.Fatal("real gateway clients shared peer limiter", got)
	}
	if got := request(proxyToken, "not-an-ip"); got != 429 {
		t.Fatal("invalid proxied client address trusted", got)
	}
}
