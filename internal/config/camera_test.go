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
