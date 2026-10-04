package operations

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestHTTPProbeRequiresRealResponseAndRejectsRedirect(t *testing.T) {
	for _, body := range []string{`<html>login</html>`, ` `} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		err := ProbeHTTP(context.Background(), ProbeTarget{URL: s.URL, Kind: "version"}, NewProbeClient())
		s.Close()
		if err == nil {
			t.Fatal("non-version response reported healthy")
		}
	}
	for _, tc := range []struct {
		body   string
		status int
		good   bool
	}{{`{"code":0}`, 200, true}, {`{"code":-1}`, 200, false}, {`{"code":0}`, 503, false}, {`not-json`, 200, false}} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.body)
		}))
		err := ProbeHTTP(context.Background(), ProbeTarget{URL: s.URL, Kind: "zlm"}, NewProbeClient())
		s.Close()
		if (err == nil) != tc.good {
			t.Fatalf("status %d body %s accepted=%v", tc.status, tc.body, err == nil)
		}
	}
	var calls atomic.Int32
	end := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, `{"code":0}`) }))
	defer end.Close()
	start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, end.URL, 302) }))
	defer start.Close()
	if err := ProbeHTTP(context.Background(), ProbeTarget{URL: start.URL, Kind: "zlm"}, NewProbeClient()); err == nil || calls.Load() != 0 {
		t.Fatal("followed management redirect")
	}
}

func TestMQTTProbeAuthenticatesAndRequiresCONNACK(t *testing.T) {
	for _, code := range []byte{0, 5} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				result <- err
				return
			}
			defer conn.Close()
			var header [2]byte
			if _, err := io.ReadFull(conn, header[:]); err != nil {
				result <- err
				return
			}
			body := make([]byte, int(header[1]))
			if _, err := io.ReadFull(conn, body); err != nil {
				result <- err
				return
			}
			if header[0] != 0x10 || len(body) < 10 || body[7] != 0xc2 || !bytes.Contains(body, []byte("one_nvr")) || !bytes.Contains(body, []byte("fixture-secret")) {
				result <- errors.New("missing authenticated MQTT CONNECT")
				return
			}
			_, err = conn.Write([]byte{0x20, 0x02, 0x00, code})
			result <- err
		}()
		err = ProbeMQTT(context.Background(), listener.Addr().String(), "one_nvr", "fixture-secret")
		if (err == nil) != (code == 0) {
			t.Fatalf("CONNACK %d accepted=%v", code, err == nil)
		}
		if serverErr := <-result; serverErr != nil {
			t.Fatal(serverErr)
		}
		listener.Close()
	}
}
