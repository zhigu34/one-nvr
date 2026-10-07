package zlm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWHEPPrivateNegotiationAndDeletion(t *testing.T) {
	var deleted bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			if r.URL.Path != "/index/api/delete_webrtc" || r.URL.Query().Get("id") != "peer-1" || r.URL.Query().Get("token") != "private-delete" {
				t.Error("invalid deletion")
			}
			deleted = true
			w.WriteHeader(200)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.Path != "/index/api/whep" || r.Header.Get("Content-Type") != "application/sdp" || string(raw) != "v=0\r\n" || r.URL.Query().Get("live_token") != strings.Repeat("a", 64) || r.URL.Query().Get("secret") != "" {
			t.Error("invalid negotiation")
		}
		w.Header().Set("Location", server.URL+"/index/api/delete_webrtc?id=peer-1&token=private-delete")
		w.WriteHeader(201)
		w.Write([]byte("v=0\r\nanswer"))
	}))
	defer server.Close()
	c, _ := New(server.URL, "management-key", nil)
	answer, peer, err := c.Negotiate(context.Background(), StreamKey{"__defaultVhost__", "one_nvr", "e7022543-2512-4fbf-867a-0bbcc686d28d"}, "v=0\r\n", strings.Repeat("a", 64))
	if err != nil || answer != "v=0\r\nanswer" {
		t.Fatalf("negotiate: %v", err)
	}
	if err := c.CloseRTC(context.Background(), peer); err != nil || !deleted {
		t.Fatalf("delete: %v", err)
	}
}

func TestWHEPRejectsUntrustedLocation(t *testing.T) {
	for _, location := range []string{"http://other-host/index/api/delete_webrtc?id=x&token=y", "/index/api/getServerConfig?id=x&token=y", "/index/api/delete_webrtc?id=x&id=z&token=y", "/index/api/delete_webrtc?id=x&token=y&secret=z"} {
		t.Run(location, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", location)
				w.WriteHeader(201)
				w.Write([]byte("v=0\r\n"))
			}))
			defer server.Close()
			c, _ := New(server.URL, "management-key", nil)
			_, _, err := c.Negotiate(context.Background(), StreamKey{"__defaultVhost__", "one_nvr", "e7022543-2512-4fbf-867a-0bbcc686d28d"}, "v=0\r\n", strings.Repeat("a", 64))
			if err == nil {
				t.Fatal("accepted untrusted deletion location")
			}
		})
	}
}
