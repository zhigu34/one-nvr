// Package egress defines the media container's fixed outbound boundary.
package egress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
)

var ErrBoundary = errors.New("media_network_boundary_unavailable")

type Config struct {
	CameraCIDRs string   `json:"camera_cidrs"`
	DeniedIPs   []string `json:"denied_ips"`
	DeniedHosts []string `json:"denied_hosts"`
	HookHost    string   `json:"hook_host"`
}
type Policy struct {
	Allowed []netip.Prefix
	Denied  []netip.Addr
	HookIPs []netip.Addr
}

var special = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("::/128"), netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8"), netip.MustParsePrefix("::ffff:0:0/96")}

func Build(c Config, connected []netip.Prefix, hooks []netip.Addr) (Policy, error) {
	p := Policy{HookIPs: append([]netip.Addr(nil), hooks...)}
	if len(c.DeniedIPs) > 128 || len(hooks) > 16 || len(connected) > 128 {
		return Policy{}, ErrBoundary
	}
	for _, raw := range c.DeniedIPs {
		ip, err := netip.ParseAddr(raw)
		if err != nil || ip.Is4In6() || ip.Zone() != "" {
			return Policy{}, ErrBoundary
		}
		p.Denied = append(p.Denied, ip)
	}
	if strings.TrimSpace(c.CameraCIDRs) == "" {
		return p, nil
	}
	parts := strings.Split(c.CameraCIDRs, ",")
	if len(parts) > 32 {
		return Policy{}, ErrBoundary
	}
	for _, raw := range parts {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
			return Policy{}, ErrBoundary
		}
		prefix = prefix.Masked()
		for _, local := range connected {
			if prefix.Overlaps(local) && prefix.Bits() != prefix.Addr().BitLen() {
				return Policy{}, ErrBoundary
			}
		}
		p.Allowed = append(p.Allowed, prefix)
		if prefix.Addr().Is4() && prefix.Bits() <= 30 {
			p.Denied = append(p.Denied, prefix.Addr())
			last := prefix.Addr().As4()
			for bit := prefix.Bits(); bit < 32; bit++ {
				last[bit/8] |= 1 << uint(7-bit%8)
			}
			p.Denied = append(p.Denied, netip.AddrFrom4(last))
		}
	}
	return p, nil
}
func (p Policy) Allows(ip netip.Addr, port uint16, established bool) bool {
	if established {
		return true
	}
	if ip.Is4In6() || ip.Zone() != "" {
		return false
	}
	if ip == netip.MustParseAddr("127.0.0.11") && port == 53 {
		return true
	}
	for _, hook := range p.HookIPs {
		if ip == hook && port == 8083 {
			return true
		}
	}
	for _, s := range special {
		if s.Contains(ip) {
			return false
		}
	}
	for _, deny := range p.Denied {
		if ip == deny {
			return false
		}
	}
	for _, allow := range p.Allowed {
		if allow.Contains(ip) {
			return true
		}
	}
	return false
}
func Resolve(ctx context.Context, c Config) (Policy, error) {
	if len(c.DeniedHosts) > 32 {
		return Policy{}, ErrBoundary
	}
	var hooks []netip.Addr
	if c.HookHost != "" {
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", c.HookHost)
		if err != nil {
			return Policy{}, ErrBoundary
		}
		for _, ip := range ips {
			hooks = append(hooks, ip.Unmap())
		}
	}
	for _, host := range c.DeniedHosts {
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err == nil {
			for _, ip := range ips {
				c.DeniedIPs = append(c.DeniedIPs, ip.Unmap().String())
			}
		}
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return Policy{}, ErrBoundary
	}
	var connected []netip.Prefix
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err != nil || prefix.Addr().IsLoopback() {
			continue
		}
		connected = append(connected, prefix.Masked())
		c.DeniedIPs = append(c.DeniedIPs, prefix.Addr().String())
	}
	return Build(c, connected, hooks)
}
