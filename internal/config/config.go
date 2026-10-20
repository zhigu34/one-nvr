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
	"time"
)

type Config struct {
	PublicURL                                   string
	HTTPPort, HTTPSPort, RTCPort, FrigateUIPort int
	// OpenListUIPort publishes the bundled OpenList console on the host. Zero
	// keeps it unpublished, exactly like FrigateUIPort.
	//
	// It has to be a published port rather than a proxied path under our origin:
	// OpenList's console — and the first-run administrator creation that happens
	// in it — is another product's application, not part of one-nvr's API.
	// Reverse-proxying it would put its session cookies and upgrades under our
	// origin to no benefit.
	OpenListUIPort                                                          int
	MediaHost, DataDir, DatabaseURL, TLSDir, HardwareProfile, ListenAddress string
	CameraCIDRs, HookSubnet, Timezone                                       string
	FrigateEnabled, OpenListEnabled                                         bool
	// EventCacheMB is the size of the in-memory ring buffer used for event
	// pre-recording (REC-09). Zero means the feature is off: no tmpfs is
	// mounted, so nothing can write there and no memory is reserved. It is a
	// deployment-time value because a mount cannot be resized under a running
	// container; the business decision (which channels, how long) stays in the
	// settings.
	EventCacheMB int
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
	if c.Timezone == "" {
		return fmt.Errorf("ONE_NVR_TZ must be an IANA time zone name")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("ONE_NVR_TZ must be an IANA time zone name")
	}
	for k, p := range map[string]int{"HTTP": c.HTTPPort, "HTTPS": c.HTTPSPort, "RTC": c.RTCPort} {
		if p < 1 || p > 65535 {
			return fmt.Errorf("%s port must be 1–65535", k)
		}
	}
	// The detector UI port is optional: 0 keeps it unpublished.
	if c.FrigateUIPort != 0 && (c.FrigateUIPort < 1 || c.FrigateUIPort > 65535) {
		return fmt.Errorf("FRIGATE_UI port must be 0 or 1–65535")
	}
	if c.OpenListUIPort != 0 && (c.OpenListUIPort < 1 || c.OpenListUIPort > 65535) {
		return fmt.Errorf("OPENLIST_UI port must be 0 or 1–65535")
	}
	// Publishing a port for a container that does not ship is a configuration
	// mistake worth naming: the operator would wait for an interface that can
	// never come up. Frigate has no such check because its UI port predates this
	// one; adding it there would reject configurations that already work.
	if c.OpenListUIPort != 0 && !c.OpenListEnabled {
		return fmt.Errorf("OPENLIST_UI port requires ONE_NVR_OPENLIST_ENABLE=yes")
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
	case "auto", "cpu", "intel-igpu", "nvidia":
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
