package zlm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const fixtureStream = "bb6f7f61-e7dd-4eee-a03c-a53167d6d688"

var fixtureKey = StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: fixtureStream}

func TestZLMRejectsFalseSuccessAndCredentialEcho(t *testing.T) {
	for _, body := range []string{`{"code":-1,"msg":"rtsp://fixture:private@192.168.1.2/main"}`, `{"code":0,"result":false}`, `{"result":true}`, `invalid`, strings.Repeat("x", (1<<20)+1)} {
		t.Run(body[:min(len(body), 32)], func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.RawQuery != "" {
					t.Error("management request exposed query or not POST")
				}
				r.ParseForm()
				if r.PostForm.Get("secret") != "fixture-private-key" {
					t.Error("no form secret")
				}
				w.Write([]byte(body))
			}))
			defer s.Close()
			c, err := New(s.URL, "fixture-private-key", s.Client())
			if err != nil {
				t.Fatal(err)
			}
			err = c.StartRecord(context.Background(), fixtureKey, "/storage/pool/.work/zlm/"+fixtureStream, 60)
			if err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "rtsp:") {
				t.Fatal("false success or credential echo", err)
			}
		})
	}
	var redirected bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected = true
		w.Write([]byte(`{"code":0,"result":true}`))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	c, err := New(source.URL, "fixture", &http.Client{Timeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if c.Client.Timeout != 10*time.Second {
		t.Fatal("unbounded management timeout")
	}
	if err = c.StartRecord(context.Background(), fixtureKey, "/storage/pool/.work/zlm/"+fixtureStream, 60); err == nil || redirected {
		t.Fatal("followed management redirect", err)
	}
}
func TestZLMTypedOperationsAndSanitizedSnapshot(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.URL.Path {
		case "/index/api/addStreamProxy":
			for k, v := range map[string]string{"enable_mp4": "0", "enable_hls": "0", "retry_count": "0", "auto_close": "0", "rtp_type": "0"} {
				if r.Form.Get(k) != v {
					t.Error("unsafe proxy setting", k)
				}
			}
			w.Write([]byte(`{"code":0,"data":{"key":"__defaultVhost__/one_nvr/` + fixtureStream + `"}}`))
		case "/index/api/delStreamProxy":
			w.Write([]byte(`{"code":0,"data":{"flag":true}}`))
		case "/index/api/getMediaInfo":
			w.Write([]byte(`{"code":0,"schema":"rtsp","vhost":"__defaultVhost__","app":"one_nvr","stream":"` + fixtureStream + `","originUrl":"rtsp://private@192.168.1.2/main","bytesSpeed":1024,"isRecordingMP4":true,"tracks":[{"codec_type":0,"codec_id_name":"H264","ready":true,"width":320,"height":180,"fps":5,"frames":10}]}`))
		default:
			w.Write([]byte(`{"code":0,"result":true}`))
		}
	}))
	defer s.Close()
	c, err := New(s.URL, "fixture", s.Client())
	if err != nil {
		t.Fatal(err)
	}
	ref, err := c.AddProxy(context.Background(), ProxyInput{Key: fixtureKey, URL: "rtsp://fixture:private@192.168.1.2/main", Transport: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.RemoveProxy(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	snap, err := c.Inspect(context.Background(), fixtureKey)
	if err != nil || !snap.Recording || len(snap.Tracks) != 1 || snap.Tracks[0].Width != 320 {
		t.Fatal("snapshot missing typed evidence", err)
	}
	if err = c.StopRecord(context.Background(), fixtureKey); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = c.StopRecord(cancelled, fixtureKey); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
}
