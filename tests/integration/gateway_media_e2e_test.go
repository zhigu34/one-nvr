//go:build gateway_runtime

package integration

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/config"
	"github.com/zhigu34/one-nvr/internal/media/egress"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/secrets"
)

// Provision real private media infrastructure only. Browser creates the 32-slot
// site, pool, drafts, tests and changes through the production HTTP/UI paths.
func TestGatewayMediaRuntimeE2EInit(t *testing.T) {
	TestGatewayTLSRuntimeE2EInit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", "camera")
	if err != nil || len(ips) != 1 {
		t.Fatal("isolated camera unavailable")
	}
	cidr := ips[0].String() + "/32"
	if cidr != os.Getenv("ONE_NVR_CAMERA_CIDRS") {
		t.Fatal("camera boundary mismatch")
	}
	secret, err := secrets.Load("/data")
	if err != nil {
		t.Fatal("private fixture secrets unavailable")
	}
	rendered, err := zlm.RenderConfig(config.Config{MediaHost: "127.0.0.1", RTCPort: 8000}, secret)
	if err != nil {
		t.Fatal("private media configuration unavailable")
	}
	if err = os.WriteFile("/media-config/zlm.ini", []byte(rendered), 0600); err != nil {
		t.Fatal(err)
	}
	boundary, _ := json.Marshal(egress.Config{CameraCIDRs: cidr, DeniedHosts: []string{"api", "worker", "postgres", "gateway", "browser", "runner", "zlm", "fixture"}, HookHost: "worker"})
	if err = os.WriteFile("/media-config/egress.json", boundary, 0600); err != nil {
		t.Fatal(err)
	}
	fixture, _ := json.Marshal(map[string]any{"camera_ip": ips[0].String(), "paths": []string{"/one_nvr/01887644-36d3-45d6-8d8b-5a4c5b9ebfea", "/one_nvr/c8f92b1a-22e5-4c8c-a3ca-7f66d8c2d101", "/one_nvr/c8f92b1a-22e5-4c8c-a3ca-7f66d8c2d102", "/one_nvr/c8f92b1a-22e5-4c8c-a3ca-7f66d8c2d103"}})
	if err = os.WriteFile("/results/media-fixture.json", fixture, 0600); err != nil {
		t.Fatal(err)
	}
}
