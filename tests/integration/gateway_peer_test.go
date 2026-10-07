//go:build gateway_runtime

package integration

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

// The wrapper reserves the old API address, recreates API at a different IP,
// and keeps the actual gateway running. Static startup resolution cannot pass.
func TestGatewayAPIPeerRelocation(t *testing.T) {
	if os.Getenv("ONE_NVR_GATEWAY_RUNTIME") != "isolated" {
		t.Fatal("requires isolated gateway fixture")
	}
	public, err := url.Parse(os.Getenv("ONE_NVR_PUBLIC_URL"))
	if err != nil || public.Host == "" {
		t.Fatal("public fixture URL unavailable")
	}
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		request, _ := http.NewRequest("GET", public.Scheme+"://gateway/api/v1/setup/status?gateway_peer_check=1", nil)
		request.Host = public.Host
		response, err := client.Do(request)
		if err != nil {
			continue
		}
		var out struct {
			Data struct {
				Initialized bool `json:"initialized"`
			} `json:"data"`
			RequestID string `json:"request_id"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 16384)).Decode(&out)
		response.Body.Close()
		if response.StatusCode == 200 && decodeErr == nil && out.Data.Initialized && out.RequestID != "" {
			return
		}
	}
	t.Fatal("gateway did not reach relocated API without restart")
}
