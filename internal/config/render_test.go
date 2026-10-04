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
func TestDeploymentRefusesSharedSystemRoots(t *testing.T){for _,p:=range []string{"/","/etc","/var","/srv","/usr","/home","/opt","/mnt","/tmp"}{if _,e:=BuildDeployment(map[string]string{"ONE_NVR_DATA_DIR":p});e==nil{t.Fatalf("shared system root accepted: %s",p)}}}
