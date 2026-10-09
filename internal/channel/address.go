package channel

import (
	"encoding/binary"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/zhigu34/one-nvr/internal/fault"
)

type NetworkPolicy struct {
	Allowed        []netip.Prefix
	Denied         []netip.Addr
	DeniedNetworks []netip.Prefix
}

func ParseNetworkPolicy(value string, denied []netip.Addr) (NetworkPolicy, error) {
	out := NetworkPolicy{Denied: append([]netip.Addr(nil), denied...)}
	if strings.TrimSpace(value) == "" {
		return out, nil
	}
	parts := strings.Split(value, ",")
	if len(parts) > 32 {
		return out, invalidSource("camera_network_invalid", "摄像头允许网段最多32个")
	}
	seen := map[netip.Prefix]bool{}
	for _, raw := range parts {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() || prefix.Addr().Zone() != "" {
			return NetworkPolicy{}, invalidSource("camera_network_invalid", "请填写有效且明确的摄像头允许网段")
		}
		prefix = prefix.Masked()
		if !seen[prefix] {
			out.Allowed = append(out.Allowed, prefix)
			seen[prefix] = true
		}
	}
	return out, nil
}
func invalidSource(code, message string) error { return fault.New(422, code, message) }
func (p NetworkPolicy) allows(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" || addr.Is4In6() || !addr.IsGlobalUnicast() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	for _, deny := range p.Denied {
		if deny.Unmap() == addr.Unmap() {
			return false
		}
	}
	for _, network := range p.DeniedNetworks {
		if !network.Contains(addr) {
			continue
		}
		exact := false
		for _, allow := range p.Allowed {
			if allow.Bits() == addr.BitLen() && allow.Contains(addr) {
				exact = true
				break
			}
		}
		if !exact {
			return false
		}
	}
	for _, prefix := range p.Allowed {
		if !prefix.Contains(addr) {
			continue
		}
		if addr.Is4() && prefix.Bits() <= 30 {
			ip := addr.As4()
			first := prefix.Masked().Addr().As4()
			v := binary.BigEndian.Uint32(ip[:])
			start := binary.BigEndian.Uint32(first[:])
			last := start | uint32((uint64(1)<<(32-prefix.Bits()))-1)
			if v == start || v == last {
				continue
			}
		}
		return true
	}
	return false
}
func validSourcePath(path string, optional bool) bool {
	if path == "" {
		return optional
	}
	if len(path) > 4096 || !utf8.ValidString(path) || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return false
	}
	u, err := url.Parse(path)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Fragment != "" || strings.Contains(path, "#") {
		return false
	}
	decoded, err := url.PathUnescape(path)
	return err == nil && strings.IndexFunc(decoded, unicode.IsControl) < 0
}

// validateTarget is the single address-boundary check: source configuration and
// outbound camera traffic (including ONVIF probing) must agree on which
// addresses this deployment may talk to.
func (p NetworkPolicy) validateTarget(ip string) (netip.Addr, error) {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil || a.Zone() != "" || a.Is4In6() {
		return netip.Addr{}, invalidSource("source_config_invalid", "摄像头IP、端口或码流路径无效")
	}
	if len(p.Allowed) == 0 {
		return netip.Addr{}, invalidSource("camera_network_not_configured", "请先在部署配置中填写 ONE_NVR_CAMERA_CIDRS 摄像头允许网段")
	}
	if !p.allows(a) {
		return netip.Addr{}, invalidSource("camera_address_denied", "摄像头地址不在允许范围内或属于受限管理服务")
	}
	return a, nil
}
func (c SourceConfig) shapeValid() bool {
	a, err := netip.ParseAddr(c.IP)
	if err != nil || a.Zone() != "" || a.Is4In6() || c.RTSPPort < 0 || c.RTSPPort > 65535 || (c.Transport != "" && c.Transport != "tcp" && c.Transport != "udp") {
		return false
	}
	if c.ONVIFPort != nil && (*c.ONVIFPort < 1 || *c.ONVIFPort > 65535) {
		return false
	}
	return validSourcePath(c.MainPath, false) && validSourcePath(c.SubPath, true)
}
func (c SourceConfig) Validate(policy NetworkPolicy) error {
	if !c.shapeValid() {
		return invalidSource("source_config_invalid", "摄像头IP、端口或码流路径无效")
	}
	_, err := policy.validateTarget(c.IP)
	return err
}
func BuildRTSPURL(c SourceConfig, username, password, path string) (string, error) {
	if !c.shapeValid() || !validSourcePath(path, false) || (path != c.MainPath && path != c.SubPath) || !validCredential(username) || !validCredential(password) {
		return "", invalidSource("source_config_invalid", "摄像头连接配置无效")
	}
	u, _ := url.Parse(path)
	u.Scheme = "rtsp"
	port := c.RTSPPort
	if port == 0 {
		port = 554
	}
	u.Host = net.JoinHostPort(c.IP, strconv.Itoa(port))
	if username != "" || password != "" {
		u.User = url.UserPassword(username, password)
	}
	return u.String(), nil
}
func validCredential(value string) bool {
	return len(value) <= 1024 && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}
func normalizedConfig(c SourceConfig) SourceConfig {
	a, err := netip.ParseAddr(c.IP)
	if err == nil {
		c.IP = a.String()
	}
	if c.RTSPPort == 0 {
		c.RTSPPort = 554
	}
	if c.Transport == "" {
		c.Transport = "tcp"
	}
	return c
}
