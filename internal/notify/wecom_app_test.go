package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The application API lives on a fixed host, so these tests replace the shared
// token cache and point the client at a test server by rewriting the URL. That
// keeps the production code free of a base-URL setting it would only need for
// tests.

// newTestWeComApp builds an adapter whose HTTP calls go to the given server.
func newTestWeComApp(t *testing.T, server *httptest.Server, tokens *wecomTokens) *wecomApp {
	t.Helper()
	adapter, err := newWeComApp(ChannelConfig{
		ID:     "channel-app",
		Kind:   "wecom_app",
		Name:   "值班应用",
		Secret: []byte("app-secret"),
		WeComApp: &WeComAppConfig{
			CorpID:  "ww-corp",
			AgentID: 1000002,
			To:      "user:zhangsan",
			MsgType: "text",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	app := adapter.(*wecomApp)
	app.client = server.Client()
	app.tokens = tokens
	// The endpoints are absolute in production; the test server stands in for
	// both of them through a transport hook.
	app.client.Transport = rewriteTransport{base: server.URL, underlying: app.client.Transport}
	return app
}

// rewriteTransport sends every request to the test server, keeping the path and
// query so the code under test still builds the URL it would in production.
type rewriteTransport struct {
	base       string
	underlying http.RoundTripper
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target := r.base + req.URL.Path
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}
	rewritten, err := http.NewRequestWithContext(req.Context(), req.Method, target, req.Body)
	if err != nil {
		return nil, err
	}
	rewritten.Header = req.Header
	underlying := r.underlying
	if underlying == nil {
		underlying = http.DefaultTransport
	}
	return underlying.RoundTrip(rewritten)
}

func testTokens(now time.Time) *wecomTokens {
	return &wecomTokens{entries: map[string]wecomToken{}, now: func() time.Time { return now }}
}

// A token costs a rate-limited gettoken, so one exchange must serve many sends.
func TestWeComAppCachesItsAccessToken(t *testing.T) {
	var tokenCalls, sendCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			atomic.AddInt32(&tokenCalls, 1)
			_, _ = w.Write([]byte(`{"errcode":0,"access_token":"token-1","expires_in":7200}`))
		case "/cgi-bin/message/send":
			atomic.AddInt32(&sendCalls, 1)
			if r.URL.Query().Get("access_token") != "token-1" {
				t.Errorf("send used token %q, want the one gettoken returned", r.URL.Query().Get("access_token"))
			}
			_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
		}
	}))
	defer server.Close()
	app := newTestWeComApp(t, server, testTokens(time.Now()))

	for i := 0; i < 3; i++ {
		if err := app.Send(context.Background(), emailSampleMessage(t)); err != nil {
			t.Fatal(err)
		}
	}
	if tokenCalls != 1 {
		t.Fatalf("%d gettoken calls for 3 sends; the token must be cached", tokenCalls)
	}
	if sendCalls != 3 {
		t.Fatalf("%d sends, want 3", sendCalls)
	}
}

// The token lives about two hours; the cache must refresh before it dies rather
// than send a doomed request first.
func TestWeComAppRefreshesAnExpiredToken(t *testing.T) {
	var tokenCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			n := atomic.AddInt32(&tokenCalls, 1)
			if n == 1 {
				_, _ = w.Write([]byte(`{"errcode":0,"access_token":"old","expires_in":7200}`))
				return
			}
			_, _ = w.Write([]byte(`{"errcode":0,"access_token":"fresh","expires_in":7200}`))
			return
		}
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer server.Close()

	tokens := testTokens(time.Now())
	app := newTestWeComApp(t, server, tokens)
	if err := app.Send(context.Background(), emailSampleMessage(t)); err != nil {
		t.Fatal(err)
	}
	// Just inside the refresh margin: the next send must exchange a new token.
	tokens.now = func() time.Time { return time.Now().Add(7200*time.Second - time.Minute) }
	if err := app.Send(context.Background(), emailSampleMessage(t)); err != nil {
		t.Fatal(err)
	}
	if tokenCalls != 2 {
		t.Fatalf("%d gettoken calls; a token near expiry must be refreshed before use", tokenCalls)
	}
}

// WeCom can retire a token before its stated expiry. A send that reports an
// invalid token must drop the cache so the retry exchanges a fresh one — otherwise
// every retry replays the same dead token until the budget is gone.
func TestWeComAppDropsTheTokenWhenTheServerRejectsIt(t *testing.T) {
	var tokenCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			n := atomic.AddInt32(&tokenCalls, 1)
			token := "stale"
			if n > 1 {
				token = "good"
			}
			_, _ = w.Write([]byte(`{"errcode":0,"access_token":"` + token + `","expires_in":7200}`))
			return
		}
		if r.URL.Query().Get("access_token") == "stale" {
			_, _ = w.Write([]byte(`{"errcode":42001,"errmsg":"access_token expired"}`))
			return
		}
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer server.Close()

	tokens := testTokens(time.Now())
	app := newTestWeComApp(t, server, tokens)
	sendErr := app.Send(context.Background(), emailSampleMessage(t))
	if sendErr == nil {
		t.Fatal("a rejected token was reported as delivered")
	}
	if IsPermanent(sendErr) {
		t.Fatalf("an expired token must be retried with a fresh one: %v", sendErr)
	}
	// The retry (as the job layer would do) now succeeds.
	if err := app.Send(context.Background(), emailSampleMessage(t)); err != nil {
		t.Fatalf("the retry with a fresh token failed: %v", err)
	}
	if tokenCalls != 2 {
		t.Fatalf("%d gettoken calls, want 2 (initial + refresh after rejection)", tokenCalls)
	}
}

// A wrong corpID or secret is permanent: retrying sends the same rejected
// credentials, and WeCom counts the attempts.
func TestWeComAppBadCredentialsArePermanent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":40001,"errmsg":"invalid credential"}`))
	}))
	defer server.Close()
	app := newTestWeComApp(t, server, testTokens(time.Now()))
	sendErr := app.Send(context.Background(), emailSampleMessage(t))
	if sendErr == nil {
		t.Fatal("bad credentials were reported as delivered")
	}
	if !IsPermanent(sendErr) {
		t.Fatalf("bad credentials must not be retried: %v", sendErr)
	}
}

// A 200 with errcode 0 can still have refused every recipient. Reporting that as
// success would tell the operator an alert was delivered that nobody received.
func TestWeComAppRefusedRecipientsFailTheDelivery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			_, _ = w.Write([]byte(`{"errcode":0,"access_token":"t","expires_in":7200}`))
			return
		}
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok","invaliduser":"zhangsan"}`))
	}))
	defer server.Close()
	app := newTestWeComApp(t, server, testTokens(time.Now()))
	sendErr := app.Send(context.Background(), emailSampleMessage(t))
	if sendErr == nil {
		t.Fatal("a message nobody could receive was reported as delivered")
	}
	if !strings.Contains(sendErr.Error(), "zhangsan") {
		t.Fatalf("the failure does not name the unreachable recipient: %v", sendErr)
	}
}

// The addressing field is one box with a prefix; each prefix must reach the right
// API field, and a bare value is read as user ids because that is what operators
// paste.
func TestWeComAppAddressing(t *testing.T) {
	cases := map[string]wecomTo{
		"@all":          {users: "@all"},
		"user:a|b":      {users: "a|b"},
		"party:1|2":     {parties: "1|2"},
		"tag:值班":        {tags: "值班"},
		"zhangsan|lisi": {users: "zhangsan|lisi"},
		"user: a | b ":  {users: "a|b"},
		"party:1|":      {parties: "1"},
	}
	for raw, want := range cases {
		got, err := parseWeComTo(raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if got != want {
			t.Fatalf("%q parsed to %+v, want %+v", raw, got, want)
		}
	}
	if _, err := parseWeComTo("   "); err == nil {
		t.Fatal("an empty recipient list was accepted")
	}
	if _, err := parseWeComTo("user:|"); err == nil {
		t.Fatal("a list of empty segments was accepted")
	}
}

// The application's text message is one block with the facts and both links.
func TestWeComAppTextBodyCarriesTheFacts(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			_, _ = w.Write([]byte(`{"errcode":0,"access_token":"t","expires_in":7200}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer server.Close()
	app := newTestWeComApp(t, server, testTokens(time.Now()))
	if err := app.Send(context.Background(), emailSampleMessage(t)); err != nil {
		t.Fatal(err)
	}
	if payload["msgtype"] != "text" {
		t.Fatalf("msgtype is %v, want text", payload["msgtype"])
	}
	text, ok := payload["text"].(map[string]any)
	if !ok {
		t.Fatalf("no text block: %v", payload)
	}
	content, _ := text["content"].(string)
	for _, want := range []string{"CH03 后门", "抓拍：", "回放："} {
		if !strings.Contains(content, want) {
			t.Fatalf("body is missing %q:\n%s", want, content)
		}
	}
	// The agent id travels with every message: an application message is
	// addressed to one app.
	if payload["agentid"] != float64(1000002) {
		t.Fatalf("agentid is %v, want 1000002", payload["agentid"])
	}
	if payload["touser"] != "zhangsan" {
		t.Fatalf("touser is %v, want zhangsan", payload["touser"])
	}
}

// An incomplete application cannot deliver; refusing at build time beats learning
// it at the first alarm.
func TestWeComAppRequiresItsParameters(t *testing.T) {
	base := func() ChannelConfig {
		return ChannelConfig{
			ID: "c", Kind: "wecom_app", Secret: []byte("s"),
			WeComApp: &WeComAppConfig{CorpID: "corp", AgentID: 1, To: "@all", MsgType: "text"},
		}
	}
	cases := map[string]func(*ChannelConfig){
		"no settings": func(c *ChannelConfig) { c.WeComApp = nil },
		"no corp":     func(c *ChannelConfig) { c.WeComApp.CorpID = "" },
		"no agent":    func(c *ChannelConfig) { c.WeComApp.AgentID = 0 },
		"no to":       func(c *ChannelConfig) { c.WeComApp.To = "" },
		"no secret":   func(c *ChannelConfig) { c.Secret = nil },
		"bad type":    func(c *ChannelConfig) { c.WeComApp.MsgType = "voice" },
	}
	for name, breakIt := range cases {
		config := base()
		copyApp := *config.WeComApp
		config.WeComApp = &copyApp
		breakIt(&config)
		if _, err := newWeComApp(config); err == nil {
			t.Fatalf("%s: an incomplete application was accepted", name)
		}
	}
}

// The secret is sent to gettoken as a query parameter. A transport failure there
// must not put it in the error a delivery row stores.
func TestWeComAppSecretNeverReachesTheError(t *testing.T) {
	tokens := testTokens(time.Now())
	client := &http.Client{Transport: rewriteTransport{base: "http://127.0.0.1:1"}}
	adapter, err := newWeComApp(ChannelConfig{
		ID: "c", Kind: "wecom_app", Secret: []byte("super-secret-app-key"),
		WeComApp: &WeComAppConfig{CorpID: "corp", AgentID: 1, To: "@all", MsgType: "text"},
	})
	if err != nil {
		t.Fatal(err)
	}
	app := adapter.(*wecomApp)
	app.client = client
	app.tokens = tokens
	sendErr := app.Send(context.Background(), emailSampleMessage(t))
	if sendErr == nil {
		t.Fatal("an unreachable server was reported as delivered")
	}
	if strings.Contains(sendErr.Error(), "super-secret-app-key") {
		t.Fatalf("the application secret leaked into the error: %v", sendErr)
	}
}
