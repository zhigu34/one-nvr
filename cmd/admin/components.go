package main

import (
	"encoding/json"
	"fmt"
	"github.com/zhigu34/one-nvr/internal/config"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"net/url"
	"os"
	"path/filepath"
)

func writeComponentConfigs(v map[string]string, secret secrets.State) error {
	c, e := config.FromValues(v)
	if e != nil {
		return e
	}
	u, _ := url.Parse(c.PublicURL)
	host := c.MediaHost
	if host == "" {
		host = u.Hostname()
	}
	key, e := secret.ComponentCredential("zlm")
	if e != nil {
		return e
	}
	zlm := fmt.Sprintf("[api]\nsecret=%s\n[http]\nport=80\nsslport=0\n[rtsp]\nport=554\nsslport=0\n[rtmp]\nport=1935\n[rtc]\nexternIP=%s\nport=%d\ntcpPort=%d\n[protocol]\nenable_mp4=0\nenable_hls=0\n", key, host, c.RTCPort, c.RTCPort)
	if e = replaceFile("/data/runtime/zlm.ini", []byte(zlm), 0644); e != nil {
		return e
	}
	if c.FrigateEnabled {
		mqtt, e := secret.ComponentCredential("mqtt")
		if e != nil {
			return e
		}
		conf := "listener 1883\nallow_anonymous false\npassword_file /mosquitto/config/passwords\npersistence true\npersistence_location /mosquitto/data/\nlog_dest stdout\n"
		if e = replaceFile("/data/runtime/mosquitto.conf", []byte(conf), 0644); e != nil {
			return e
		}
		// Converted once by the pinned mosquitto_passwd tool using a file, no password argument.
		path := "/data/runtime/mqtt.passwd"
		if _, e = os.Lstat(path); os.IsNotExist(e) {
			if e = immutableFile(path, []byte("one_nvr:"+mqtt+"\n"), 0600); e != nil {
				return e
			}
			if e = os.Chown(path, 1883, 1883); e != nil {
				return e
			}
		}
		if e = os.Chown("/data/mqtt", 1883, 1883); e != nil {
			return e
		}
		cfg := map[string]any{"mqtt": map[string]any{"host": "mqtt", "user": "one_nvr", "password": mqtt}, "record": map[string]any{"enabled": false}, "snapshots": map[string]any{"enabled": true}, "cameras": map[string]any{}, "detectors": map[string]any{"cpu": map[string]any{"type": "cpu", "num_threads": 2}}, "model": map[string]any{"path": "/cpu_model.tflite"}, "version": "0.17-0"}
		b, _ := json.MarshalIndent(cfg, "", "  ")
		cfgPath := filepath.Join("/data/frigate", "config.yml")
		if _, statErr := os.Lstat(cfgPath); os.IsNotExist(statErr) {
			e = immutableFile(cfgPath, b, 0600)
		}
		if e != nil {
			return e
		}
	}
	return nil
}
