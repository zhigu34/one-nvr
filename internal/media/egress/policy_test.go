package egress

import (
	"net/netip"
	"testing"
)

func TestMediaEgressRejectsRedirectAndOverlappingNetworks(t *testing.T) {
	p, err := Build(Config{CameraCIDRs: "192.168.33.0/24", DeniedIPs: []string{"192.168.33.200", "172.19.0.2"}}, []netip.Prefix{netip.MustParsePrefix("172.19.0.0/16")}, []netip.Addr{netip.MustParseAddr("172.19.0.3")})
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"192.168.33.200", "172.19.0.2", "172.19.0.3", "169.254.169.254", "127.0.0.1", "127.0.0.11", "224.0.0.1", "192.168.44.20"} {
		if p.Allows(netip.MustParseAddr(ip), 554, false) {
			t.Fatal("redirect outside boundary allowed", ip)
		}
	}
	if !p.Allows(netip.MustParseAddr("127.0.0.11"), 53, false) {
		t.Fatal("embedded DNS blocked")
	}
	if !p.Allows(netip.MustParseAddr("192.168.33.20"), 554, false) || !p.Allows(netip.MustParseAddr("172.19.0.3"), 8083, false) {
		t.Fatal("camera or hook blocked")
	}
	if !p.Allows(netip.MustParseAddr("100.64.0.1"), 8000, true) {
		t.Fatal("established viewer response blocked")
	}
	if _, err := Build(Config{CameraCIDRs: "172.19.0.0/24"}, []netip.Prefix{netip.MustParsePrefix("172.19.0.0/16")}, nil); err == nil {
		t.Fatal("broad camera network overlaps Docker internal subnet")
	}
	isolated, err := Build(Config{CameraCIDRs: "172.19.0.4/32", DeniedIPs: []string{"172.19.0.2"}}, []netip.Prefix{netip.MustParsePrefix("172.19.0.0/16")}, nil)
	if err != nil || !isolated.Allows(netip.MustParseAddr("172.19.0.4"), 554, false) || isolated.Allows(netip.MustParseAddr("172.19.0.2"), 8555, false) {
		t.Fatal("exact isolated fixture peer escaped", err)
	}
	p6, err := Build(Config{CameraCIDRs: "fd00:42::/64", DeniedIPs: []string{"fd00:42::10"}}, nil, nil)
	if err != nil || !p6.Allows(netip.MustParseAddr("fd00:42::20"), 554, false) || p6.Allows(netip.MustParseAddr("fd00:42::10"), 554, false) || p6.Allows(netip.MustParseAddr("::ffff:192.168.33.20"), 554, false) {
		t.Fatal("IPv6 boundary invalid", err)
	}
}
