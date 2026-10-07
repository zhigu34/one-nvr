package config

import (
	"encoding/json"
	"net/netip"
	"testing"
)

// The generated callback hostname must have one fixed, private destination:
// restarting/recreating Worker may change its ordinary Docker address, while
// the unprivileged media process cannot rewrite its startup firewall.
func TestDeploymentProvidesStablePrivateHookPeer(t *testing.T) {
	d, err := BuildDeployment(map[string]string{"ONE_NVR_HOOK_SUBNET": "172.30.254.0/29"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := d.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Services map[string]struct {
			Networks map[string]struct {
				Address string   `json:"ipv4_address"`
				Aliases []string `json:"aliases"`
			} `json:"networks"`
			Ports []string `json:"ports"`
		} `json:"services"`
		Networks map[string]struct {
			Internal bool `json:"internal"`
		} `json:"networks"`
	}
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	var fixed netip.Addr
	shared := ""
	for network, peer := range document.Services["worker"].Networks {
		for _, alias := range peer.Aliases {
			if alias != "worker-hook" {
				continue
			}
			fixed, err = netip.ParseAddr(peer.Address)
			if err != nil || !netip.MustParsePrefix("172.30.254.0/29").Contains(fixed) {
				t.Fatal("callback alias lacks the configured fixed destination")
			}
			shared = network
		}
	}
	if !fixed.IsValid() {
		t.Fatal("Worker callback destination changes with dynamic Docker allocation")
	}
	if !document.Networks[shared].Internal {
		t.Fatal("callback network is externally routed")
	}
	if _, ok := document.Services["zlm"].Networks[shared]; !ok {
		t.Fatal("media cannot reach private callback peer")
	}
	for name, service := range document.Services {
		if name != "worker" && name != "zlm" {
			if _, ok := service.Networks[shared]; ok {
				t.Fatal("unrelated service joined callback network", name)
			}
		}
	}
	if len(document.Services["worker"].Ports) != 0 {
		t.Fatal("private hook published to host")
	}
}

func TestDeploymentRejectsInvalidOrCameraOverlappingHookSubnet(t *testing.T) {
	for _, subnet := range []string{"0.0.0.0/0", "172.30.254.1/29", "172.30.254.0/32", "8.8.8.0/29", "::1/128", "172.30.254.0/8", "192.168.33.0/29"} {
		if _, err := BuildDeployment(map[string]string{"ONE_NVR_HOOK_SUBNET": subnet, "ONE_NVR_CAMERA_CIDRS": "192.168.33.0/24"}); err == nil {
			t.Fatalf("unsafe or overlapping callback network accepted: %s", subnet)
		}
	}
}
