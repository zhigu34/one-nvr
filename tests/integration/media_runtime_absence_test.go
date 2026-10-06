package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/media/zlm"
)

func runtimeUpstreamAbsent(ctx context.Context, endpoint string, key zlm.StreamKey) bool {
	if key.Validate() != nil {
		return false
	}
	form := url.Values{"schema": {"rtsp"}, "vhost": {key.VHost}, "app": {key.App}, "stream": {key.Stream}, "secret": {"isolated-media-fixture-only"}}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/index/api/isMediaOnline", strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1025))
	var evidence struct {
		Code   *int  `json:"code"`
		Online *bool `json:"online"`
	}
	return err == nil && len(raw) <= 1024 && json.Unmarshal(raw, &evidence) == nil && evidence.Code != nil && *evidence.Code == 0 && evidence.Online != nil && !*evidence.Online
}

func TestRuntimeUpstreamAbsenceRequiresSuccessfulOnlineResponse(t *testing.T) {
	key := zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: "01887644-36d3-45d6-8d8b-5a4c5b9ebfea"}
	for _, tc := range []struct {
		name, body string
		status     int
		absent     bool
	}{
		{"offline", `{"code":0,"online":false}`, 200, true},
		{"online", `{"code":0,"online":true}`, 200, false},
		{"missing online", `{"code":0}`, 200, false},
		{"missing code", `{"online":false}`, 200, false},
		{"rejected", `{"code":-1,"online":false}`, 200, false},
		{"unavailable", `{"code":0,"online":false}`, 503, false},
		{"invalid", `not-json`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/index/api/getMediaInfo" {
					// A missing-stream lookup may use most of the observation
					// window. Absence needs only the explicit online response.
					time.Sleep(120 * time.Millisecond)
					fmt.Fprint(w, `{"code":-1}`)
					return
				}
				if r.URL.Path != "/index/api/isMediaOnline" || r.FormValue("stream") != key.Stream {
					w.WriteHeader(400)
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()
			if got := runtimeUpstreamAbsent(ctx, server.URL, key); got != tc.absent {
				t.Fatalf("absence=%t, want %t; unknown must not pass and offline must not depend on media-info latency", got, tc.absent)
			}
		})
	}
}
