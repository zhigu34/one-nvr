package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/httpapi"
	"github.com/zhigu34/one-nvr/internal/storage"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPoolHTTPPermissionsImmutablePathAndVersions(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	base := t.TempDir()
	path := filepath.Join(base, "disk")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: "http://nvr.example.com", Storage: storage.New(db, accounts, []string{base})})
	request := func(method, url, body, version, raw string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://nvr.example.com"+url, bytes.NewBufferString(body))
		r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: raw})
		r.Header.Set("Origin", "http://nvr.example.com")
		r.Header.Set("X-CSRF-Token", accounts.CSRF(raw))
		r.Header.Set("If-Match", version)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	body, _ := json.Marshal(storage.RegisterInput{Name: "录像池", Path: path})
	w := request("POST", "/api/v1/storage-pools", string(body), "", admin.RawSession)
	if w.Code != 201 {
		t.Fatalf("registration %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Data storage.Pool `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	url := "/api/v1/storage-pools/" + string(created.Data.ID)
	for _, tc := range []struct {
		body, version string
		want          int
	}{{`{"path":"/other"}`, `"1"`, 422}, {`{"name":"renamed"}`, "", 422}, {`{"name":"renamed"}`, `"1"`, 200}, {`{"enabled":false}`, `"1"`, 409}} {
		w := request("PATCH", url, tc.body, tc.version, admin.RawSession)
		if w.Code != tc.want {
			t.Fatalf("patch %s %d %s", tc.body, w.Code, w.Body.String())
		}
	}
	if w := request("GET", "/api/v1/storage-pools?limit=1", "", "", admin.RawSession); w.Code != 200 {
		t.Fatal("list failed", w.Body.String())
	}
	if _, err := accounts.CreateUser(context.Background(), admin.Principal, auth.CreateUserInput{Username: "poolviewer", Password: testPassword, Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	viewer, err := accounts.Login(context.Background(), "poolviewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, url, body, version string }{{"GET", "/api/v1/storage-pools", "", ""}, {"POST", "/api/v1/storage-pools", string(body), ""}, {"PATCH", url, `{"name":"denied"}`, `"2"`}, {"DELETE", url, "", `"2"`}, {"POST", url + "/test", "", ""}} {
		if w := request(tc.method, tc.url, tc.body, tc.version, viewer.RawSession); w.Code != 403 {
			t.Fatalf("viewer %s %d", tc.method, w.Code)
		}
	}
}
