package zlm

import (
	"fmt"
	"github.com/zhigu34/one-nvr/internal/config"
	"github.com/zhigu34/one-nvr/internal/secrets"
)

func RenderConfig(c config.Config, secret secrets.State) (string, error) {
	key, e := secret.ComponentCredential("zlm")
	if e != nil {
		return "", e
	}
	hook, e := secret.ComponentCredential("recording-hook")
	if e != nil {
		return "", e
	}
	return fmt.Sprintf("[api]\napiDebug=0\nsecret=%s\n[general]\nmediaServerId=one-nvr-%s\n[http]\nport=80\nsslport=0\n[rtsp]\nport=554\nsslport=0\n[rtmp]\nport=0\nsslport=0\n[shell]\nport=0\n[rtc]\nexternIP=%s\nport=%d\ntcpPort=%d\n[protocol]\nenable_mp4=0\nenable_hls=0\nauto_close=0\ncontinue_push_ms=0\n[hook]\nenable=1\ntimeoutSec=5\nretry=3\nretry_delay=1\non_record_mp4=http://worker:8083/on_record_mp4?token=%s\non_play=http://worker:8083/on_play?token=%s\non_publish=http://worker:8083/on_publish?token=%s\non_http_access=http://worker:8083/on_http_access?token=%s\non_stream_none_reader=http://worker:8083/on_stream_none_reader?token=%s\n", key, secret.SiteID, c.MediaHost, c.RTCPort, c.RTCPort, hook, hook, hook, hook, hook), nil
}
