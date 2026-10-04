package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type BindOptions struct {
	CreateHostPath bool `json:"create_host_path"`
}
type Mount struct {
	Type     string      `json:"type"`
	Source   string      `json:"source"`
	Target   string      `json:"target"`
	ReadOnly bool        `json:"read_only"`
	Bind     BindOptions `json:"bind"`
}
type Service struct {
	Image       string                       `json:"image"`
	User        string                       `json:"user,omitempty"`
	EntryPoint  []string                     `json:"entrypoint,omitempty"`
	Command     []string                     `json:"command,omitempty"`
	Environment map[string]string            `json:"environment,omitempty"`
	Ports       []string                     `json:"ports,omitempty"`
	Volumes     []Mount                      `json:"volumes,omitempty"`
	Restart     string                       `json:"restart"`
	DependsOn   map[string]map[string]string `json:"depends_on,omitempty"`
	HealthCheck map[string]any               `json:"healthcheck,omitempty"`
	Sysctls     map[string]string            `json:"sysctls,omitempty"`
	Devices     []string                     `json:"devices,omitempty"`
	ShmSize     string                       `json:"shm_size,omitempty"`
}
type Deployment struct {
	Services map[string]Service `json:"services"`
}

var imagePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:-]*@sha256:[a-f0-9]{64}$`)
var ImagePins = map[string]string{
	"postgres": "postgres:17.11-bookworm@sha256:639ab7ceb90e13123085b741fb31ef493fba25463002f6da665352e7b534b652",
	"zlm":      "zlmediakit/zlmediakit:master@sha256:25ecf6c4a55e72bc495be15c8480f0b0cef58c712e5c9f0596452777b41bf344",
	"frigate":  "ghcr.io/blakeblackshear/frigate:0.17.2@sha256:d4351369984d4a9e2a49ac59736f6490856a7ea11f7790040746d21496967010",
	"mqtt":     "eclipse-mosquitto:2.0.22@sha256:199ea8ef2e35ec2b1b37e59cfd1dbae538ed4dfa4a2251a121a52215a6248a21",
	"openlist": "openlistteam/openlist:v4.2.6@sha256:c555c6e1c8af2aead38ed12ec761ac077fdf046d19cf033414be8e056aec6b64",
}

func BuildDeployment(v map[string]string) (Deployment, error) {
	c, e := FromValues(v)
	if e != nil {
		return Deployment{}, e
	}
	if e = c.Validate(); e != nil {
		return Deployment{}, e
	}
	u, _ := url.Parse(c.PublicURL)
	if c.MediaHost == "" {
		c.MediaHost = u.Hostname()
	}
	image := func(name string) (string, error) {
		if x := v["ONE_NVR_"+strings.ToUpper(name)+"_IMAGE"]; x != "" {
			if !imagePattern.MatchString(x) {
				return "", fmt.Errorf("%s image must be digest pinned", name)
			}
			return x, nil
		}
		return ImagePins[name], nil
	}
	mount := func(source, target string, ro bool) Mount {
		return Mount{Type: "bind", Source: source, Target: target, ReadOnly: ro}
	}
	root := filepath.Clean(c.DataDir)
	switch root {
	case "/", "/etc", "/var", "/srv", "/usr", "/home", "/opt", "/mnt", "/tmp":
		return Deployment{}, fmt.Errorf("application data requires a dedicated directory")
	}
	runtime := filepath.Join(root, "runtime")
	poolRoot := v["ONE_NVR_STORAGE_ROOT"]
	if poolRoot == "" {
		poolRoot = "/srv/one-nvr-storage"
	}
	if !filepath.IsAbs(poolRoot) || strings.ContainsAny(poolRoot, "\x00\n\r") {
		return Deployment{}, fmt.Errorf("invalid storage root")
	}
	common := map[string]string{"ONE_NVR_PUBLIC_URL": c.PublicURL, "ONE_NVR_HTTP_PORT": fmt.Sprint(c.HTTPPort), "ONE_NVR_HTTPS_PORT": fmt.Sprint(c.HTTPSPort), "ONE_NVR_RTC_PORT": fmt.Sprint(c.RTCPort), "ONE_NVR_MEDIA_HOST": c.MediaHost, "ONE_NVR_DATA_DIR": "/data", "ONE_NVR_HARDWARE_PROFILE": c.HardwareProfile, "ONE_NVR_DATABASE_URL": "postgres://one_nvr:" + url.QueryEscape(v["ONE_NVR_POSTGRES_PASSWORD"]) + "@postgres:5432/one_nvr?sslmode=disable", "ONE_NVR_FRIGATE_ENABLE": boolValue(c.FrigateEnabled), "ONE_NVR_OPENLIST_ENABLE": boolValue(c.OpenListEnabled)}
	vols := []Mount{mount(root, "/data", false), mount(poolRoot, "/storage", false)}
	if c.TLSDir != "" {
		common["ONE_NVR_TLS_DIR"] = "/tls-input"
		vols = append(vols, mount(c.TLSDir, "/tls-input", true))
	}
	d := Deployment{Services: map[string]Service{}}
	appImage := v["ONE_NVR_APP_IMAGE"]
	if appImage == "" {
		appImage = "one-nvr/app:m1a"
	}
	gatewayImage := v["ONE_NVR_GATEWAY_IMAGE"]
	if gatewayImage == "" {
		gatewayImage = "one-nvr/gateway:m1a"
	}
	for name, x := range map[string]string{"app": appImage, "gateway": gatewayImage} {
		if x != "one-nvr/"+name+":m1a" && !imagePattern.MatchString(x) {
			return d, fmt.Errorf("custom %s image must be digest pinned", name)
		}
	}
	for _, name := range []string{"api", "worker"} {
		env := map[string]string{}
		for k, x := range common {
			env[k] = x
		}
		port := "8081"
		if name == "worker" {
			port = "8082"
		}
		env["ONE_NVR_LISTEN_ADDRESS"] = ":" + port
		d.Services[name] = Service{Image: appImage, User: "10001:10001", EntryPoint: []string{"/usr/local/bin/" + name}, Environment: env, Volumes: vols, Restart: "unless-stopped", DependsOn: map[string]map[string]string{"postgres": {"condition": "service_healthy"}}, HealthCheck: map[string]any{"test": []string{"CMD", "/usr/local/bin/admin", "ready-check", "http://127.0.0.1:" + port + "/health/ready"}, "interval": "10s", "timeout": "5s", "retries": 6}}
	}
	pg, e := image("postgres")
	if e != nil {
		return d, e
	}
	d.Services["postgres"] = Service{Image: pg, Environment: map[string]string{"POSTGRES_USER": "one_nvr", "POSTGRES_DB": "one_nvr", "POSTGRES_PASSWORD": v["ONE_NVR_POSTGRES_PASSWORD"]}, Volumes: []Mount{mount(filepath.Join(root, "postgres"), "/var/lib/postgresql/data", false)}, Restart: "unless-stopped", HealthCheck: map[string]any{"test": []string{"CMD-SHELL", "pg_isready -U one_nvr -d one_nvr"}, "interval": "2s", "timeout": "5s", "retries": 30}}
	webPort := fmt.Sprintf("%d:443/tcp", c.HTTPSPort)
	if u.Scheme == "http" {
		webPort = fmt.Sprintf("%d:80/tcp", c.HTTPPort)
	}
	d.Services["gateway"] = Service{Image: gatewayImage, User: "10001:10001", Environment: map[string]string{"ONE_NVR_PUBLIC_URL": c.PublicURL}, EntryPoint: []string{"sh", "-ec"}, Command: []string{`export ONE_NVR_GATEWAY_PROXY_TOKEN="$(cat /control/proxy-token)"; exec /usr/local/bin/gateway-entrypoint`}, Ports: []string{webPort}, Volumes: []Mount{mount(filepath.Join(root, "gateway"), "/control", false), mount(filepath.Join(root, "tls"), "/certs", true)}, Restart: "unless-stopped", Sysctls: map[string]string{"net.ipv4.ip_unprivileged_port_start": "0"}, HealthCheck: map[string]any{"test": []string{"CMD", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1/health"}, "interval": "10s", "timeout": "5s", "retries": 6}}
	zlm, e := image("zlm")
	if e != nil {
		return d, e
	}
	d.Services["zlm"] = Service{Image: zlm, Command: []string{"./MediaServer", "-c", "/opt/media/conf/config.ini", "-l", "0"}, Volumes: []Mount{mount(filepath.Join(runtime, "zlm.ini"), "/opt/media/conf/config.ini", true), mount(poolRoot, "/storage", false)}, Ports: []string{fmt.Sprintf("%d:%d/tcp", c.RTCPort, c.RTCPort), fmt.Sprintf("%d:%d/udp", c.RTCPort, c.RTCPort)}, Restart: "unless-stopped"}
	if c.FrigateEnabled {
		fg, e := image("frigate")
		if e != nil {
			return d, e
		}
		mq, e := image("mqtt")
		if e != nil {
			return d, e
		}
		d.Services["mqtt"] = Service{Image: mq, Volumes: []Mount{mount(filepath.Join(runtime, "mosquitto.conf"), "/mosquitto/config/mosquitto.conf", true), mount(filepath.Join(runtime, "mqtt.passwd"), "/mosquitto/config/passwords", true), mount(filepath.Join(root, "mqtt"), "/mosquitto/data", false)}, Restart: "unless-stopped"}
		d.Services["frigate"] = Service{Image: fg, Volumes: []Mount{mount(filepath.Join(root, "frigate"), "/config", false)}, ShmSize: "256mb", Restart: "unless-stopped"}
	}
	if c.OpenListEnabled {
		im, e := image("openlist")
		if e != nil {
			return d, e
		}
		d.Services["openlist"] = Service{Image: im, Volumes: []Mount{mount(filepath.Join(root, "openlist"), "/opt/openlist/data", false)}, Restart: "unless-stopped"}
	}
	return d, nil
}
func boolValue(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
func DisabledOptional(a, b Deployment) []string {
	out := []string{}
	for _, n := range []string{"frigate", "mqtt", "openlist"} {
		if _, ok := a.Services[n]; ok {
			if _, on := b.Services[n]; !on {
				out = append(out, n)
			}
		}
	}
	return out
}
func (d Deployment) Names() []string {
	out := []string{}
	for n := range d.Services {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Compose interpolates dollar signs even in JSON. Escape literal data exactly once.
func (d Deployment) JSON() ([]byte, error) {
	b, e := json.MarshalIndent(d, "", "  ")
	return []byte(strings.ReplaceAll(string(b), "$", "$$")), e
}
