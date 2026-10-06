package main

import (
	"encoding/json"
	"fmt"
	"github.com/zhigu34/one-nvr/internal/config"
	"github.com/zhigu34/one-nvr/internal/media/egress"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"net/netip"
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
	c.MediaHost = host
	zlm, e := zlm.RenderConfig(c, secret)
	if e != nil {
		return e
	}
	if e = replaceFile("/data/runtime/zlm.ini", []byte(zlm), 0644); e != nil {
		return e
	}
	launcher, e := os.ReadFile("/usr/local/bin/media-launcher")
	if e != nil {
		return fmt.Errorf("media network launcher unavailable")
	}
	if e = replaceFile("/data/runtime/zlm-launcher", launcher, 0755); e != nil {
		return e
	}
	boundary := egress.Config{CameraCIDRs: c.CameraCIDRs, HookHost: "worker-hook", DeniedHosts: []string{"api", "worker", "worker-hook", "gateway", "postgres", "zlm"}}
	for _, address := range []string{host, u.Hostname()} {
		if ip, e := netip.ParseAddr(address); e == nil {
			boundary.DeniedIPs = append(boundary.DeniedIPs, ip.String())
		} else if address != "" {
			boundary.DeniedHosts = append(boundary.DeniedHosts, address)
		}
	}
	if c.FrigateEnabled {
		boundary.DeniedHosts = append(boundary.DeniedHosts, "frigate", "mqtt")
	}
	if c.OpenListEnabled {
		boundary.DeniedHosts = append(boundary.DeniedHosts, "openlist")
	}
	policy, e := json.Marshal(boundary)
	if e != nil {
		return e
	}
	if e = replaceFile("/data/runtime/zlm-egress.json", policy, 0644); e != nil {
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
