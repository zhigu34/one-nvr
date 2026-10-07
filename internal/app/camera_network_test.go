package app

import (
	"context"
	"errors"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/config"
	"net/netip"
	"testing"
)

func TestOptionalDNSFailureDoesNotDisableCameraBoundary(t *testing.T) {
	lookup := func(_ context.Context, _ string, host string) ([]netip.Addr, error) {
		switch host {
		case "frigate", "mqtt", "openlist":
			return nil, errors.New("offline")
		}
		return []netip.Addr{netip.MustParseAddr("172.20.0.2")}, nil
	}
	c := config.Config{CameraCIDRs: "192.168.33.0/24,172.20.0.0/16", PublicURL: "https://192.168.33.200:8443", FrigateEnabled: true, OpenListEnabled: true}
	policy, err := freshCameraNetworkResolved(context.Background(), c, lookup, []netip.Prefix{netip.MustParsePrefix("172.20.0.0/16")})
	if err != nil {
		t.Fatal("optional outage disabled core", err)
	}
	if err := (channel.SourceConfig{IP: "192.168.33.20", MainPath: "/main"}).Validate(policy); err != nil {
		t.Fatal("ordinary camera rejected", err)
	}
	for _, ip := range []string{"172.20.0.2", "172.20.0.40", "192.168.33.200"} {
		if err := (channel.SourceConfig{IP: ip, MainPath: "/main"}).Validate(policy); err == nil {
			t.Fatal("management address became a camera", ip)
		}
	}
	lookup = func(context.Context, string, string) ([]netip.Addr, error) { return nil, errors.New("core offline") }
	if _, err := freshCameraNetworkResolved(context.Background(), c, lookup, nil); err == nil {
		t.Fatal("unknown core boundary accepted")
	}
}
