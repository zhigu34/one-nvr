package config

import (
	"reflect"
	"testing"
)

func TestDeploymentModuleMatrixAndSelectedPorts(t *testing.T) {
	for _, v := range []struct {
		f, o string
		n    int
	}{{"no", "no", 5}, {"yes", "no", 7}, {"no", "yes", 6}, {"yes", "yes", 8}} {
		d, e := BuildDeployment(map[string]string{"ONE_NVR_PUBLIC_URL": "https://192.168.1.10:8443", "ONE_NVR_HTTPS_PORT": "8443", "ONE_NVR_DATA_DIR": "/srv/one-nvr", "ONE_NVR_FRIGATE_ENABLE": v.f, "ONE_NVR_OPENLIST_ENABLE": v.o})
		if e != nil {
			t.Fatal(e)
		}
		if len(d.Services) != v.n {
			t.Fatalf("services %d", len(d.Services))
		}
		if !reflect.DeepEqual(d.Services["gateway"].Ports, []string{"8443:443/tcp"}) {
			t.Fatal("web port")
		}
		if !reflect.DeepEqual(d.Services["zlm"].Ports, []string{"8000:8000/tcp", "8000:8000/udp"}) {
			t.Fatal("rtc port")
		}
	}
}
func TestDisableStopsOnlyOptionalServices(t *testing.T) {
	a, _ := BuildDeployment(map[string]string{"ONE_NVR_FRIGATE_ENABLE": "yes", "ONE_NVR_OPENLIST_ENABLE": "yes"})
	b, _ := BuildDeployment(nil)
	if got := DisabledOptional(a, b); !reflect.DeepEqual(got, []string{"frigate", "mqtt", "openlist"}) {
		t.Fatalf("%v", got)
	}
}
func TestDeploymentLiteralAndReadOnlyDirectory(t *testing.T) {
	d, e := BuildDeployment(map[string]string{"ONE_NVR_DATA_DIR": "/srv/$literal data", "ONE_NVR_TLS_DIR": "/srv/cert"})
	if e != nil {
		t.Fatal(e)
	}
	mounts := d.Services["api"].Volumes
	found := false
	for _, m := range mounts {
		if m.Source == "/srv/cert" {
			found = m.ReadOnly && !m.Bind.CreateHostPath
		}
	}
	if !found {
		t.Fatal("TLS input must bind existing directory readonly")
	}
	for _, bad := range []string{"mutable:latest", "$(touch /tmp/owned)", "x\nsecret"} {
		if _, e = BuildDeployment(map[string]string{"ONE_NVR_ZLM_IMAGE": bad}); e == nil {
			t.Fatal("unsafe image override")
		}
	}
}
func TestDeploymentRefusesSharedSystemRoots(t *testing.T) {
	for _, p := range []string{"/", "/etc", "/var", "/srv", "/usr", "/home", "/opt", "/mnt", "/tmp"} {
		if _, e := BuildDeployment(map[string]string{"ONE_NVR_DATA_DIR": p}); e == nil {
			t.Fatalf("shared system root accepted: %s", p)
		}
	}
}

// The detector's own UI is published only when a port is asked for: an
// operator who never reads Frigate's interface should not have it reachable
// on the host, and internal-only must stay the default.
func TestFrigateUIPortIsOptIn(t *testing.T) {
	base := map[string]string{
		"ONE_NVR_PUBLIC_URL":     "https://192.168.1.10:8443",
		"ONE_NVR_HTTPS_PORT":     "8443",
		"ONE_NVR_DATA_DIR":       "/srv/one-nvr",
		"ONE_NVR_FRIGATE_ENABLE": "yes",
	}
	deployment, err := BuildDeployment(base)
	if err != nil {
		t.Fatal(err)
	}
	if ports := deployment.Services["frigate"].Ports; len(ports) != 0 {
		t.Fatalf("frigate must not publish a host port by default, got %v", ports)
	}

	withUI := map[string]string{}
	for k, v := range base {
		withUI[k] = v
	}
	withUI["ONE_NVR_FRIGATE_UI_PORT"] = "5001"
	deployment, err = BuildDeployment(withUI)
	if err != nil {
		t.Fatal(err)
	}
	ports := deployment.Services["frigate"].Ports
	if !reflect.DeepEqual(ports, []string{"5001:5000/tcp"}) {
		t.Fatalf("a configured UI port must be published to Frigate's 5000, got %v", ports)
	}
}

func TestFrigateUIPortValidation(t *testing.T) {
	base := map[string]string{
		"ONE_NVR_PUBLIC_URL": "https://192.168.1.10:8443",
		"ONE_NVR_HTTPS_PORT": "8443",
		"ONE_NVR_DATA_DIR":   "/srv/one-nvr",
	}
	bad := map[string]string{}
	for k, v := range base {
		bad[k] = v
	}
	bad["ONE_NVR_FRIGATE_UI_PORT"] = "70000"
	if _, err := BuildDeployment(bad); err == nil {
		t.Fatal("an out-of-range detector UI port must be refused")
	}
	ok := map[string]string{}
	for k, v := range base {
		ok[k] = v
	}
	ok["ONE_NVR_FRIGATE_UI_PORT"] = "0"
	if _, err := BuildDeployment(ok); err != nil {
		t.Fatalf("zero keeps the UI unpublished and must be accepted: %v", err)
	}
}

// Every container must follow the site zone: an operator comparing a database
// log line, a recorder job and a detector event otherwise has to convert three
// different clocks in their head.
func TestTimezoneReachesEveryService(t *testing.T) {
	deployment, err := BuildDeployment(map[string]string{
		"ONE_NVR_PUBLIC_URL":      "https://192.168.1.10:8443",
		"ONE_NVR_HTTPS_PORT":      "8443",
		"ONE_NVR_DATA_DIR":        "/srv/one-nvr",
		"ONE_NVR_TZ":              "Europe/Berlin",
		"ONE_NVR_FRIGATE_ENABLE":  "yes",
		"ONE_NVR_OPENLIST_ENABLE": "yes",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api", "worker", "postgres", "gateway", "zlm", "mqtt", "frigate", "openlist"} {
		service, ok := deployment.Services[name]
		if !ok {
			t.Fatalf("service %s missing from the deployment", name)
		}
		if service.Environment["TZ"] != "Europe/Berlin" {
			t.Fatalf("service %s has TZ=%q, want Europe/Berlin", name, service.Environment["TZ"])
		}
	}
}

func TestTimezoneDefaultsAndValidation(t *testing.T) {
	base := map[string]string{
		"ONE_NVR_PUBLIC_URL": "https://192.168.1.10:8443",
		"ONE_NVR_HTTPS_PORT": "8443",
		"ONE_NVR_DATA_DIR":   "/srv/one-nvr",
	}
	c, err := FromValues(base)
	if err != nil {
		t.Fatal(err)
	}
	if c.Timezone != "Asia/Shanghai" {
		t.Fatalf("the default zone must match the default site, got %q", c.Timezone)
	}
	bad := map[string]string{}
	for k, v := range base {
		bad[k] = v
	}
	bad["ONE_NVR_TZ"] = "Mars/Olympus"
	// BuildDeployment is the path a real deployment takes; it validates.
	if _, err := BuildDeployment(bad); err == nil {
		t.Fatal("a non-IANA zone must be refused before it reaches the containers")
	}
}

// REC-09: the event ring buffer only exists when the deployment reserved memory
// for it. A site that did not must get no mount at all — a zero-sized tmpfs or
// a mount into a directory nothing writes would be worse than absent, because
// the overview page would report a cache that cannot hold anything.
func TestEventCacheMountIsOptIn(t *testing.T) {
	base := map[string]string{
		"ONE_NVR_PUBLIC_URL": "https://192.168.1.10:8443",
		"ONE_NVR_HTTPS_PORT": "8443",
		"ONE_NVR_DATA_DIR":   "/srv/one-nvr",
	}
	without, err := BuildDeployment(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api", "worker", "zlm"} {
		for _, mount := range without.Services[name].Volumes {
			if mount.Target == "/cache" {
				t.Fatalf("%s mounts /cache without a configured size", name)
			}
		}
	}

	withSize := map[string]string{"ONE_NVR_EVENT_CACHE_MB": "512"}
	for k, v := range base {
		withSize[k] = v
	}
	with, err := BuildDeployment(withSize)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api", "worker", "zlm"} {
		var found *Mount
		for i := range with.Services[name].Volumes {
			if with.Services[name].Volumes[i].Target == "/cache" {
				found = &with.Services[name].Volumes[i]
			}
		}
		if found == nil {
			t.Fatalf("%s does not mount the event cache", name)
		}
		if found.Type != "tmpfs" {
			t.Fatalf("%s cache mount is %q, must be tmpfs so the contents never reach a disk", name, found.Type)
		}
		if found.Tmpfs == nil || found.Tmpfs.Size != 512<<20 {
			t.Fatalf("%s cache size wrong: %+v", name, found.Tmpfs)
		}
		// Docker's default tmpfs mode is 0755 root-owned, which locks out the
		// non-root worker. Measured on the live site: ZLM (root) wrote fine and
		// uid 10001 got EACCES. The sticky world-writable mode is what makes
		// both containers able to create directories there.
		if found.Tmpfs.Mode != 0o1777 {
			t.Fatalf("%s cache mode is %o, must be 1777 so the non-root worker can write: %+v", name, found.Tmpfs.Mode, found.Tmpfs)
		}
		// A tmpfs mount carrying bind options is rejected by compose.
		if found.Bind != nil {
			t.Fatalf("%s cache mount carries bind options", name)
		}
	}
	// The API must know where the mount is, or the overview cannot report its
	// usage.
	if with.Services["api"].Environment["ONE_NVR_EVENT_CACHE_DIR"] != "/cache" {
		t.Fatal("api is not told where the cache is mounted")
	}
}
