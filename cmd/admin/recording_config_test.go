package main

import (
	"github.com/zhigu34/one-nvr/internal/config"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"strings"
	"testing"
)

func TestZLMRecordingHookConfig(t *testing.T) {
	s, e := secrets.Init(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	text, e := zlm.RenderConfig(config.Config{MediaHost: "192.168.33.200", RTCPort: 8000}, s)
	if e != nil {
		t.Fatal(e)
	}
	hook, _ := s.ComponentCredential("recording-hook")
	for _, need := range []string{"apiDebug=0", "mediaServerId=one-nvr-" + string(s.SiteID), "enable_mp4=0", "enable_hls=0", "auto_close=0", "continue_push_ms=0", "on_record_mp4=http://worker:8083/on_record_mp4?token=" + hook, "on_play=http://worker:8083/on_play?token=" + hook, "on_http_access=http://worker:8083/on_http_access?token=" + hook, "on_stream_none_reader=http://worker:8083/on_stream_none_reader?token=" + hook} {
		if !strings.Contains(text, need+"\n") {
			t.Fatal("missing private media setting", strings.Split(need, "=")[0])
		}
	}
	if strings.Contains(text, "on_server_started=http") {
		t.Fatal("full config must not be posted in server-start hook")
	}
}
