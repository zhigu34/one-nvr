package channel

import (
	"net/netip"
	"testing"
)

func TestRTSPAddressAndAllowedCIDRs(t *testing.T) {
	p, err := ParseNetworkPolicy("192.168.33.0/24,2001:db8:1234::/64", []netip.Addr{netip.MustParseAddr("192.168.33.10")})
	if err != nil {
		t.Fatal(err)
	}
	good := SourceConfig{IP: "192.168.33.20", RTSPPort: 554, MainPath: "/main?x=a%2Fb", Transport: "tcp"}
	if err := good.Validate(p); err != nil {
		t.Fatal(err)
	}
	got, err := BuildRTSPURL(good, "u@:/?#%", "p@:/?#%", good.MainPath)
	if err != nil || got != "rtsp://u%40%3A%2F%3F%23%25:p%40%3A%2F%3F%23%25@192.168.33.20:554/main?x=a%2Fb" {
		t.Fatalf("userinfo/path encoding: %s %v", got, err)
	}
	ipv6 := good
	ipv6.IP = "2001:db8:1234::20"
	ipv6.RTSPPort = 0
	got, err = BuildRTSPURL(ipv6, "", "", ipv6.MainPath)
	if err != nil || got != "rtsp://[2001:db8:1234::20]:554/main?x=a%2Fb" {
		t.Fatalf("IPv6/default port: %s %v", got, err)
	}
	for _, ip := range []string{"192.168.66.20", "192.168.33.10", "127.0.0.1", "169.254.1.2", "::ffff:192.168.33.20", "192.168.33.0", "192.168.33.255"} {
		in := good
		in.IP = ip
		if err := in.Validate(p); err == nil {
			t.Fatal("disallowed address accepted", ip)
		}
	}
	for _, path := range []string{"rtsp://192.168.33.20/main", "//host/main", "/main#fragment", "/bad%GG", "/main\r\n"} {
		in := good
		in.MainPath = path
		if err := in.Validate(p); err == nil {
			t.Fatal("bad path accepted")
		}
	}
	for _, path := range []string{"/main%2Fpart?x=one%20two", "/Streaming/Channels/101", "/m?token=%23", "/main"} {
		in := good
		in.MainPath = path
		if err := in.Validate(p); err != nil {
			t.Fatal("valid path rejected", err)
		}
	}
	for _, input := range []string{"invalid-cidr", "192.168.33.20", "0.0.0.0/0", "::/0"} {
		if _, err := ParseNetworkPolicy(input, nil); err == nil {
			t.Fatal("invalid/overbroad range accepted")
		}
	}
	if err := good.Validate(NetworkPolicy{}); err == nil {
		t.Fatal("empty allowed ranges accepted source")
	}
}
