package config

import "testing"

func TestCameraNetworksReachAPIAndWorker(t *testing.T) {
	value := "192.168.33.0/24,192.168.66.0/24"
	d, err := BuildDeployment(map[string]string{"ONE_NVR_CAMERA_CIDRS": value})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api", "worker"} {
		if d.Services[name].Environment["ONE_NVR_CAMERA_CIDRS"] != value {
			t.Fatal("allowed camera ranges missing from service", name)
		}
	}
	for _, value := range []string{"invalid", "0.0.0.0/0", "::/0", "192.168.1.1"} {
		if _, err := BuildDeployment(map[string]string{"ONE_NVR_CAMERA_CIDRS": value}); err == nil {
			t.Fatal("invalid network accepted")
		}
	}
	if _, err := BuildDeployment(nil); err != nil {
		t.Fatal("empty camera ranges prevented basic deployment", err)
	}
}

func TestZLMAlwaysUsesContainerNetworkBoundary(t *testing.T) {
	d, err := BuildDeployment(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := d.Services["zlm"]
	if len(s.EntryPoint) != 1 || s.EntryPoint[0] != "/usr/local/bin/media-launcher" {
		t.Fatal("ZLM can start without installing its boundary")
	}
	if len(s.CapAdd) != 1 || s.CapAdd[0] != "NET_ADMIN" {
		t.Fatal("initializer cannot install boundary")
	}
	binary, policy := false, false
	for _, mount := range s.Volumes {
		if mount.Target == "/usr/local/bin/media-launcher" {
			binary = mount.ReadOnly && !mount.Bind.CreateHostPath
		}
		if mount.Target == "/opt/media/conf/egress.json" {
			policy = mount.ReadOnly && !mount.Bind.CreateHostPath
		}
	}
	if !binary || !policy {
		t.Fatal("network wrapper or policy missing/read-write")
	}
	if len(d.Services) != 5 {
		t.Fatal("network boundary added a permanent container")
	}
}
