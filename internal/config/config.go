package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	PublicURL                                                               string
	HTTPPort, HTTPSPort, RTCPort                                            int
	MediaHost, DataDir, DatabaseURL, TLSDir, HardwareProfile, ListenAddress string
	CameraCIDRs, HookSubnet                                                 string
	FrigateEnabled, OpenListEnabled                                         bool
}

func Load() (Config, error) {
	v := make(map[string]string)
	for _, s := range os.Environ() {
		k, value, ok := strings.Cut(s, "=")
		if ok {
			v[k] = value
		}
	}
	c, err := FromValues(v)
	if err != nil {
		return c, err
	}
	return c, c.Validate()
}
func (c *Config) Validate() error {
	for k, p := range map[string]int{"HTTP": c.HTTPPort, "HTTPS": c.HTTPSPort, "RTC": c.RTCPort} {
		if p < 1 || p > 65535 {
			return fmt.Errorf("%s port must be 1–65535", k)
		}
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Host == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("ONE_NVR_PUBLIC_URL must be an absolute HTTP(S) origin")
	}
	var expected, effective int
	switch u.Scheme {
	case "http":
		expected = c.HTTPPort
		effective = 80
	case "https":
		expected = c.HTTPSPort
		effective = 443
	default:
		return fmt.Errorf("ONE_NVR_PUBLIC_URL requires http or https")
	}
	if u.Port() != "" {
		effective, err = strconv.Atoi(u.Port())
		if err != nil {
			return fmt.Errorf("invalid public port")
		}
	}
	if effective != expected {
		return fmt.Errorf("public URL port must match active %s port", u.Scheme)
	}
	if c.RTCPort == expected {
		return fmt.Errorf("RTC TCP port conflicts with active web port")
	}
	if c.MediaHost == "" {
		c.MediaHost = u.Hostname()
	}
	ip, err := netip.ParseAddr(c.MediaHost)
	if err != nil || ip.IsUnspecified() || ip.IsMulticast() || ip.Zone() != "" {
		return fmt.Errorf("ONE_NVR_MEDIA_HOST must be a reachable IP (required for domain URLs)")
	}
	if !filepath.IsAbs(c.DataDir) || strings.ContainsAny(c.DataDir, "\x00\r\n") {
		return fmt.Errorf("ONE_NVR_DATA_DIR must be an absolute path")
	}
	if c.TLSDir != "" && !filepath.IsAbs(c.TLSDir) {
		return fmt.Errorf("ONE_NVR_TLS_DIR must be an absolute path")
	}
	switch c.HardwareProfile {
	case "auto", "epyc-cpu", "intel-igpu", "nvidia":
	default:
		return fmt.Errorf("invalid ONE_NVR_HARDWARE_PROFILE")
	}
	if _, p, err := net.SplitHostPort(c.ListenAddress); err != nil || p == "" {
		return fmt.Errorf("invalid API listen address")
	}
	if strings.TrimSpace(c.CameraCIDRs) != "" {
		parts := strings.Split(c.CameraCIDRs, ",")
		if len(parts) > 32 {
			return fmt.Errorf("ONE_NVR_CAMERA_CIDRS supports at most 32 ranges")
		}
		for _, raw := range parts {
			p, err := netip.ParsePrefix(strings.TrimSpace(raw))
			if err != nil || p.Bits() == 0 || p.Addr().Is4In6() {
				return fmt.Errorf("ONE_NVR_CAMERA_CIDRS requires explicit valid IP ranges")
			}
		}
	}
	return nil
}
