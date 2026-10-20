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
	if without.Volumes != nil {
		t.Fatal("a deployment without the cache must not declare the volume")
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
		// A named volume, not a per-container tmpfs: the media server writes the
		// fragments and the worker reads and evicts them, and Docker gives each
		// container its own tmpfs. Measured on the live site: ZLM held 143
		// fragments while the worker saw zero.
		if found.Type != "volume" || found.Source != "event-cache" {
			t.Fatalf("%s cache mount is %q/%q, must be the shared event-cache volume", name, found.Type, found.Source)
		}
		if found.Bind != nil || found.Tmpfs != nil || found.Volume != nil {
			t.Fatalf("%s cache mount repeats options that belong on the volume: %+v", name, found)
		}
	}
	// The volume must be declared once, so every service references the same one
	// rather than each getting its own. tmpfs is what keeps the fragments off a
	// disk; 1777 is what lets the non-root worker create directories beside the
	// media server's.
	volume := with.Volumes["event-cache"]
	if volume == nil {
		t.Fatal("the event-cache volume is not declared")
	}
	opts, _ := volume["driver_opts"].(map[string]string)
	if opts["type"] != "tmpfs" || opts["device"] != "tmpfs" {
		t.Fatalf("event-cache volume is not backed by tmpfs: %+v", opts)
	}
	if opts["o"] != "size=512m,mode=1777" {
		t.Fatalf("event-cache volume options are %q, want size and mode", opts["o"])
	}
	// The API must know where the mount is, or the overview cannot report its
	// usage.
	if with.Services["api"].Environment["ONE_NVR_EVENT_CACHE_DIR"] != "/cache" {
		t.Fatal("api is not told where the cache is mounted")
	}
	// The size must travel as well as the path: the process decides whether the
	// feature is on from the size, so a container that can see the mount but not
	// the size reports "not configured" while an empty tmpfs sits beside it.
	// Measured on the live site exactly that way.
	for _, name := range []string{"api", "worker"} {
		if got := with.Services[name].Environment["ONE_NVR_EVENT_CACHE_MB"]; got != "512" {
			t.Fatalf("%s is not told the cache size (got %q); the buffer would report not configured", name, got)
		}
	}
}

// The bundled console is where OpenList's first administrator account is
// created, so turning the module on without publishing its port leaves the
// operator with a container they cannot configure. Publishing stays opt-in for
// the same reason Frigate's does: it widens the attack surface.
func TestOpenListUIPortIsOptIn(t *testing.T) {
	base := map[string]string{
		"ONE_NVR_PUBLIC_URL":      "https://192.168.1.10:8443",
		"ONE_NVR_HTTPS_PORT":      "8443",
		"ONE_NVR_DATA_DIR":        "/srv/one-nvr",
		"ONE_NVR_OPENLIST_ENABLE": "yes",
	}
	deployment, err := BuildDeployment(base)
	if err != nil {
		t.Fatal(err)
	}
	if ports := deployment.Services["openlist"].Ports; len(ports) != 0 {
		t.Fatalf("openlist must not publish a host port by default, got %v", ports)
	}

	withUI := map[string]string{}
	for k, v := range base {
		withUI[k] = v
	}
	withUI["ONE_NVR_OPENLIST_UI_PORT"] = "5245"
	deployment, err = BuildDeployment(withUI)
	if err != nil {
		t.Fatal(err)
	}
	ports := deployment.Services["openlist"].Ports
	if !reflect.DeepEqual(ports, []string{"5245:5244/tcp"}) {
		t.Fatalf("a configured console port must be published to OpenList's 5244, got %v", ports)
	}
}

// A published port for a container that does not ship waits for an interface
// that can never come up, so it is refused rather than silently ignored.
func TestOpenListUIPortRequiresTheModule(t *testing.T) {
	_, err := BuildDeployment(map[string]string{
		"ONE_NVR_PUBLIC_URL":       "https://192.168.1.10:8443",
		"ONE_NVR_HTTPS_PORT":       "8443",
		"ONE_NVR_DATA_DIR":         "/srv/one-nvr",
		"ONE_NVR_OPENLIST_ENABLE":  "no",
		"ONE_NVR_OPENLIST_UI_PORT": "5245",
	})
	if err == nil {
		t.Fatal("a console port without the module must be refused")
	}
	// Out of range is refused too, same rule as every other port.
	_, err = BuildDeployment(map[string]string{
		"ONE_NVR_PUBLIC_URL":       "https://192.168.1.10:8443",
		"ONE_NVR_HTTPS_PORT":       "8443",
		"ONE_NVR_DATA_DIR":         "/srv/one-nvr",
		"ONE_NVR_OPENLIST_ENABLE":  "yes",
		"ONE_NVR_OPENLIST_UI_PORT": "70000",
	})
	if err == nil {
		t.Fatal("an out-of-range console port must be refused")
	}
}

// The console port is used twice: to publish the host port, and by the API to
// tell the page where the console is. Rendering the mapping without passing the
// value satisfies the first and silently breaks the second — the container read
// 0 and the archive page reported "not published" while the port was published.
func TestOpenListUIPortReachesTheContainers(t *testing.T) {
	deployment, err := BuildDeployment(map[string]string{
		"ONE_NVR_PUBLIC_URL":       "https://192.168.1.10:8443",
		"ONE_NVR_HTTPS_PORT":       "8443",
		"ONE_NVR_DATA_DIR":         "/srv/one-nvr",
		"ONE_NVR_OPENLIST_ENABLE":  "yes",
		"ONE_NVR_OPENLIST_UI_PORT": "5245",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Both facts, in the same test: the mapping alone is not the feature.
	if ports := deployment.Services["openlist"].Ports; !reflect.DeepEqual(ports, []string{"5245:5244/tcp"}) {
		t.Fatalf("the console port must be published, got %v", ports)
	}
	for _, name := range []string{"api", "worker"} {
		if got := deployment.Services[name].Environment["ONE_NVR_OPENLIST_UI_PORT"]; got != "5245" {
			t.Fatalf("%s must receive the console port so the page can link to it, got %q", name, got)
		}
	}
	// Unset means zero, not a missing key: the API reads it on every start.
	off, err := BuildDeployment(map[string]string{
		"ONE_NVR_PUBLIC_URL":      "https://192.168.1.10:8443",
		"ONE_NVR_HTTPS_PORT":      "8443",
		"ONE_NVR_DATA_DIR":        "/srv/one-nvr",
		"ONE_NVR_OPENLIST_ENABLE": "yes",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := off.Services["api"].Environment["ONE_NVR_OPENLIST_UI_PORT"]; got != "0" {
		t.Fatalf("an unpublished console must read as 0, got %q", got)
	}
}
