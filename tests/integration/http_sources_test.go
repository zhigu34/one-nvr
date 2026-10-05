package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/httpapi"
	"github.com/zhigu34/one-nvr/internal/id"
)

func TestSourceDraftHTTPNoStoreAndVersion(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	ctx := context.Background()
	policy, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: "http://nvr.example.com", Sources: channel.NewSources(db, accounts, sites.Secrets, policy)})
	var c id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&c); err != nil {
		t.Fatal(err)
	}
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
	body := `{"config":{"ip":"192.168.33.20","main_path":"/main"},"credentials":{"username":"fixture-user","password":"` + sourcePassword + `","password_action":"replace"},"identity_intent":"replace"}`
	path := "/api/v1/channels/" + string(c) + "/source-revisions"
	if w := request("POST", path, body, "1"); w.Code != 422 {
		t.Fatal("unquoted/malformed If-Match accepted", w.Code)
	}
	w := request("POST", path, body, `"1"`)
	if w.Code != 201 {
		t.Fatalf("source create HTTP: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), sourcePassword) || strings.Contains(w.Body.String(), "fixture-user") {
		t.Fatal("draft response exposed credentials/cache")
	}
	var created struct {
		Data channel.SourceRevision `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	w = request("POST", "/api/v1/channels/"+string(c)+"/source/credentials/reveal", `{"revision_id":"`+string(created.Data.ID)+`"}`, "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), sourcePassword) {
		t.Fatal("explicit reveal failed", w.Code)
	}
	w = request("GET", path, "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), sourcePassword) {
		t.Fatal("history leaked/fails", w.Code)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=$2 WHERE id=$1", c, created.Data.ID); err != nil {
		t.Fatal(err)
	}
	w = request("POST", "/api/v1/channels/source-config-export", `{"channel_ids":["`+string(c)+`"]}`, "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), sourcePassword) {
		t.Fatal("explicit export failed", w.Code)
	}
	w = request("POST", path, body, `"1"`)
	if w.Code != 409 || strings.Contains(w.Body.String(), sourcePassword) {
		t.Fatal("stale draft not safely rejected", w.Code)
	}
	badSecret := sites.Secrets
	badSecret.MasterKey = strings.Repeat("ab", 32)
	h = httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: "http://nvr.example.com", Sources: channel.NewSources(db, accounts, badSecret, policy)})
	w = request("POST", "/api/v1/channels/"+string(c)+"/source/credentials/reveal", `{"revision_id":"`+string(created.Data.ID)+`"}`, "")
	if w.Code != 503 || !strings.Contains(w.Body.String(), `"code":"credential_unavailable"`) || strings.Contains(w.Body.String(), sourcePassword) {
		t.Fatal("decryption failure not distinguished safely", w.Code, w.Body.String())
	}
}

func TestSourceTestHTTPQueuesWithoutExposingCredentials(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	ctx := context.Background()
	policy, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	sources := channel.NewSources(db, accounts, sites.Secrets, policy)
	var ch id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&ch); err != nil {
		t.Fatal(err)
	}
	password := "isolated-test-only-password"
	revision, err := sources.CreateDraft(ctx, admin.Principal, ch, 1, channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.20", MainPath: "/main"}, IdentityIntent: "replace", Credentials: channel.CredentialInput{PasswordAction: "replace", Password: &password}})
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, Sources: sources, PublicURL: "http://nvr.example.com"})
	r := httptest.NewRequest("POST", "http://nvr.example.com/api/v1/channels/"+string(ch)+"/source-revisions/"+string(revision.ID)+"/test", bytes.NewBufferString(`{}`))
	r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: admin.RawSession})
	r.Header.Set("Origin", "http://nvr.example.com")
	r.Header.Set("X-CSRF-Token", accounts.CSRF(admin.RawSession))
	r.Header.Set("Idempotency-Key", "test-http")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 202 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), password) {
		t.Fatal("test request route invalid or unsafe", w.Code, w.Body.String())
	}
	missing := httptest.NewRequest("POST", r.URL.String(), bytes.NewBufferString(`{}`))
	missing.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: admin.RawSession})
	missing.Header.Set("Origin", "http://nvr.example.com")
	missing.Header.Set("X-CSRF-Token", accounts.CSRF(admin.RawSession))
	missingResponse := httptest.NewRecorder()
	h.ServeHTTP(missingResponse, missing)
	if missingResponse.Code != 422 {
		t.Fatal("missing request key not rejected as invalid", missingResponse.Code)
	}
	var change struct {
		Data channel.Change `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &change); err != nil {
		t.Fatal(err)
	}
	var testID id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM source_tests WHERE job_id=$1", change.Data.JobID).Scan(&testID); err != nil {
		t.Fatal(err)
	}
	if change.Data.TestID == nil || *change.Data.TestID != testID {
		t.Fatal("queued test result cannot be queried before job completion")
	}

	r = httptest.NewRequest("GET", "http://nvr.example.com/api/v1/channels/"+string(ch)+"/source-tests/"+string(testID), nil)
	r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: admin.RawSession})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), password) || !strings.Contains(w.Body.String(), `"state":"queued"`) {
		t.Fatal("test result route unavailable", w.Code, w.Body.String())
	}
}

func TestSourceApplyAndClearHTTPAreDurableAndProtected(t *testing.T) {
	f, _, test, revision := testedSource(t)
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: f.Auth, Site: f.Site, Sources: f.Service.Sources, PublicURL: "http://nvr.example.com"})
	request := func(path, body, key, version string, csrf bool) *httptest.ResponseRecorder {
		q := httptest.NewRequest("POST", "http://nvr.example.com/api/v1/channels/"+string(f.Channel)+path, bytes.NewBufferString(body))
		q.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: f.Session})
		q.Header.Set("Origin", "http://nvr.example.com")
		q.Header.Set("Idempotency-Key", key)
		q.Header.Set("If-Match", version)
		if csrf {
			q.Header.Set("X-CSRF-Token", f.Auth.CSRF(f.Session))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, q)
		return w
	}
	body := `{"revision_id":"` + string(revision) + `","test_id":"` + string(*test.TestID) + `"}`
	if w := request("/source/apply", body, "apply-http", `"3"`, false); w.Code != 403 {
		t.Fatal("missing CSRF accepted", w.Code)
	}
	if w := request("/source/apply", body, "apply-http", `3`, true); w.Code != 422 {
		t.Fatal("unquoted version admitted", w.Code)
	}
	if w := request("/source/apply", body, "apply-http", `"3"`, true); w.Code != 202 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("apply route unavailable", w.Code, w.Body.String())
	}
	executeChange(t, f, "source.apply")
	if w := request("/source/clear", `{}`, "clear-http", `"5"`, true); w.Code != 202 {
		t.Fatal("clear route unavailable", w.Code, w.Body.String())
	}
}

func TestRecordingPolicyAndSourceStatusHTTP(t *testing.T) {
	f, _, _, _ := testedSource(t)
	ctx := context.Background()
	_ = ctx
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: f.Auth, Site: f.Site, Sources: f.Service.Sources, PublicURL: "http://nvr.example.com"})
	request := func(method, path, body, key, version string, csrf bool) *httptest.ResponseRecorder {
		q := httptest.NewRequest(method, "http://nvr.example.com/api/v1/channels/"+string(f.Channel)+path, strings.NewReader(body))
		q.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: f.Session})
		q.Header.Set("Content-Type", "application/json")
		q.Header.Set("Origin", "http://nvr.example.com")
		q.Header.Set("Idempotency-Key", key)
		q.Header.Set("If-Match", version)
		if csrf {
			q.Header.Set("X-CSRF-Token", f.Auth.CSRF(f.Session))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, q)
		return w
	}
	for _, path := range []string{"/source/status", "/recording-policy"} {
		w := request("GET", path, "", "", "", false)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("state route unavailable", path, w.Code, w.Body.String())
		}
	}
	if w := request("PUT", "/recording-policy", `{"mode":"none"}`, "policy-http", `"3"`, false); w.Code != 403 {
		t.Fatal("policy lacks CSRF", w.Code)
	}
	if w := request("PUT", "/recording-policy", `{"mode":"none"}`, "policy-http", `"3"`, true); w.Code != 202 {
		t.Fatal("policy not queued", w.Code, w.Body.String())
	}
	if w := request("PUT", "/storage-pool", `{"pool_id":"`+string(f.Pool.ID)+`"}`, "pool-http", `"3"`, true); w.Code != 409 {
		t.Fatal("stale pool version accepted", w.Code, w.Body.String())
	}
}

func TestImportHTTPMultipartLimitNoStoreAndCSRF(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	policy, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: "http://nvr.example.com", Sources: channel.NewSources(db, accounts, sites.Secrets, policy)})
	request := func(content string, csrf bool) *httptest.ResponseRecorder {
		var body bytes.Buffer
		m := multipart.NewWriter(&body)
		if err := m.WriteField("format", "csv"); err != nil {
			t.Fatal(err)
		}
		f, err := m.CreateFormFile("file", "camera.csv")
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(content))
		m.Close()
		r := httptest.NewRequest("POST", "http://nvr.example.com/api/v1/source-imports/preview", &body)
		r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: admin.RawSession})
		r.Header.Set("Content-Type", m.FormDataContentType())
		r.Header.Set("Origin", "http://nvr.example.com")
		if csrf {
			r.Header.Set("X-CSRF-Token", accounts.CSRF(admin.RawSession))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	csv := "channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n1,Front,192.168.33.20,554,admin,private-import-fixture,/main,\n"
	if w := request(csv, false); w.Code != 403 {
		t.Fatal("import missing CSRF accepted", w.Code)
	}
	if w := request(csv, true); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "private-import-fixture") {
		t.Fatal("import preview unavailable/unsafe", w.Code, w.Body.String())
	}
	for _, bad := range []string{strings.Repeat("x", channel.MaxImportBytes), "channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n1,\"unclosed,private-import-fixture"} {
		if w := request(bad, true); w.Code < 400 || strings.Contains(w.Body.String(), "private-import-fixture") {
			t.Fatal("import size/parse error exposed input", w.Code)
		}
	}
}

func TestImportCancelHTTPMatchesContract(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	policy, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	sources := channel.NewSources(db, accounts, sites.Secrets, policy)
	preview, err := sources.PreviewImport(context.Background(), admin.Principal, "csv", strings.NewReader("channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n1,Front,192.168.33.20,554,,,/main,\n"))
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: "http://nvr.example.com", Sources: sources})
	r := httptest.NewRequest("POST", "http://nvr.example.com/api/v1/source-imports/"+string(preview.BatchID)+"/cancel", strings.NewReader(`{}`))
	r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: admin.RawSession})
	r.Header.Set("Origin", "http://nvr.example.com")
	r.Header.Set("X-CSRF-Token", accounts.CSRF(admin.RawSession))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var response struct {
		Data struct {
			CancelRequested bool `json:"cancel_requested"`
		} `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || !response.Data.CancelRequested {
		t.Fatal("cancel response differs from contract", w.Code, err)
	}
	progress, err := sources.GetImport(context.Background(), admin.Principal, preview.BatchID)
	if err != nil || progress.State != "cancelled" {
		t.Fatal("idle preview remained active after cancel", progress, err)
	}
}
