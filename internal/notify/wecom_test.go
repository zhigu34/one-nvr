package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// wecomConfig builds a channel pointed at a test server, with the robot key in
// the URL exactly as the real API expects.
func wecomConfig(serverURL string) ChannelConfig {
	return ChannelConfig{
		ID:   "channel-wecom",
		Kind: "wecom",
		Name: "值班群",
		URL:  serverURL + "/cgi-bin/webhook/send?key=robot-secret-key",
		WeCom: &WeComConfig{
			MsgType: "markdown",
		},
	}
}

// A robot answers 200 for both success and failure, so the errcode is the only
// thing that says whether the alert landed. Trusting the status code would report
// every rejection as delivered.
func TestWeComReadsTheErrCodeNotTheStatus(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		// HTTP 200 with a failure code: exactly what the API does for a bad key.
		_, _ = w.Write([]byte(`{"errcode":93000,"errmsg":"invalid webhook url"}`))
	}))
	defer server.Close()
	adapter, err := newWeCom(wecomConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	sendErr := adapter.Send(context.Background(), emailSampleMessage(t))
	if sendErr == nil {
		t.Fatal("a rejected robot call was reported as delivered")
	}
	if !IsPermanent(sendErr) {
		t.Fatalf("an invalid robot key must not be retried: %v", sendErr)
	}
	if !strings.Contains(sendErr.Error(), "93000") {
		t.Fatalf("the failure does not name the code the operator can look up: %v", sendErr)
	}
	if got["msgtype"] != "markdown" {
		t.Fatalf("posted msgtype %v, want markdown", got["msgtype"])
	}
}

// A rate limit is worth another attempt; a rotated key is not.
func TestWeComClassifiesErrCodes(t *testing.T) {
	cases := map[int]bool{
		45009: false, // rate limited: retry
		93000: true,  // invalid key: permanent
		40001: true,  // invalid credential: permanent
		40008: true,  // malformed message: permanent
	}
	for code, wantPermanent := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"errcode":` + itoa(code) + `,"errmsg":"x"}`))
		}))
		adapter, err := newWeCom(wecomConfig(server.URL))
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		sendErr := adapter.Send(context.Background(), emailSampleMessage(t))
		server.Close()
		if sendErr == nil {
			t.Fatalf("errcode %d was reported as delivered", code)
		}
		if IsPermanent(sendErr) != wantPermanent {
			t.Fatalf("errcode %d: permanent=%v, want %v (%v)", code, IsPermanent(sendErr), wantPermanent, sendErr)
		}
	}
}

// The robot key is a credential: it must not appear in the error a delivery row
// stores and an operator reads.
func TestWeComErrorsNeverCarryTheRobotKey(t *testing.T) {
	// A closed server: the transport error normally quotes the whole URL.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL + "/cgi-bin/webhook/send?key=robot-secret-key"
	server.Close()

	adapter, err := newWeCom(ChannelConfig{ID: "c", Kind: "wecom", URL: url, WeCom: &WeComConfig{MsgType: "markdown"}})
	if err != nil {
		t.Fatal(err)
	}
	sendErr := adapter.Send(context.Background(), emailSampleMessage(t))
	if sendErr == nil {
		t.Fatal("a closed server was reported as delivered")
	}
	if strings.Contains(sendErr.Error(), "robot-secret-key") {
		t.Fatalf("the robot key leaked into the error: %v", sendErr)
	}
	if IsPermanent(sendErr) {
		t.Fatalf("an unreachable robot must be retried: %v", sendErr)
	}
}

// The markdown carries the facts and both links, and does not repeat the title.
func TestWeComMarkdownCarriesTheLinksOnce(t *testing.T) {
	var content string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Markdown struct {
				Content string `json:"content"`
			} `json:"markdown"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		content = payload.Markdown.Content
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer server.Close()
	adapter, err := newWeCom(wecomConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Send(context.Background(), emailSampleMessage(t)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "CH03 后门") {
		t.Fatalf("the channel is missing from the message:\n%s", content)
	}
	if strings.Count(content, "CH03 后门 人 高") != 1 {
		t.Fatalf("the title appears more than once:\n%s", content)
	}
	if !strings.Contains(content, "[回放](") || !strings.Contains(content, "[抓拍](") {
		t.Fatalf("the links are missing:\n%s", content)
	}
}

// Mentions are opt-in: a robot that pings the whole group on every motion event
// gets muted, and a muted channel is a silent one.
func TestWeComMentionsOnlyWhenConfigured(t *testing.T) {
	var content string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Markdown struct {
				Content string `json:"content"`
			} `json:"markdown"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		content = payload.Markdown.Content
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer server.Close()

	plain, err := newWeCom(wecomConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.Send(context.Background(), emailSampleMessage(t)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "<@") {
		t.Fatalf("a robot with no mentions still pinged someone:\n%s", content)
	}

	config := wecomConfig(server.URL)
	config.WeCom.Mentions = "13800000000, 13900000000"
	mentioning, err := newWeCom(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := mentioning.Send(context.Background(), emailSampleMessage(t)); err != nil {
		t.Fatal(err)
	}
	for _, phone := range []string{"<@13800000000>", "<@13900000000>"} {
		if !strings.Contains(content, phone) {
			t.Fatalf("the configured mention %s is missing:\n%s", phone, content)
		}
	}
}

// news mode posts one card with a link; a robot message carries a single card.
func TestWeComNewsModePostsOneCard(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer server.Close()
	config := wecomConfig(server.URL)
	config.WeCom.MsgType = "news"
	adapter, err := newWeCom(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Send(context.Background(), emailSampleMessage(t)); err != nil {
		t.Fatal(err)
	}
	if payload["msgtype"] != "news" {
		t.Fatalf("posted msgtype %v, want news", payload["msgtype"])
	}
	news, ok := payload["news"].(map[string]any)
	if !ok {
		t.Fatalf("no news block in the payload: %v", payload)
	}
	articles, _ := news["articles"].([]any)
	if len(articles) != 1 {
		t.Fatalf("%d articles; one event is one card", len(articles))
	}
}

// A robot without a URL cannot deliver; refusing at build time beats discovering
// it at the first alarm.
func TestWeComRequiresARobotURL(t *testing.T) {
	if _, err := newWeCom(ChannelConfig{ID: "c", Kind: "wecom", WeCom: &WeComConfig{MsgType: "markdown"}}); err == nil {
		t.Fatal("a robot with no URL was accepted")
	}
	if _, err := newWeCom(ChannelConfig{ID: "c", Kind: "wecom", URL: "ftp://x", WeCom: &WeComConfig{MsgType: "markdown"}}); err == nil {
		t.Fatal("a non-http robot URL was accepted")
	}
	if _, err := newWeCom(ChannelConfig{ID: "c", Kind: "wecom", URL: "https://x", WeCom: &WeComConfig{MsgType: "text"}}); err == nil {
		t.Fatal("an unsupported message type was accepted")
	}
}

func itoa(n int) string {
	digits := "0123456789"
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{digits[n%10]}, out...)
		n /= 10
	}
	return string(out)
}
