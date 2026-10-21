package detection

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderGolden(t *testing.T) {
	base := map[string]any{
		"mqtt":      map[string]any{"host": "mqtt", "user": "one_nvr", "password": "secret", "topic_prefix": "frigate"},
		"detectors": map[string]any{"cpu": map[string]any{"type": "cpu", "num_threads": 2}},
		"model":     map[string]any{"path": "/cpu_model.tflite"},
		"version":   "0.17-0",
	}
	cameras := map[string]CameraConfig{
		"ch01": {Enabled: true, InputURL: "rtsp://zlm:554/one_nvr/session-1", FPS: 5, Width: 640, Height: 360, Labels: []string{"person", "car"}},
		"ch02": {Enabled: false, InputURL: "rtsp://zlm:554/one_nvr/session-2", FPS: 5, Width: 640, Height: 360, Labels: []string{"person"}},
	}
	rendered, err := Render(base, cameras)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(rendered, &config); err != nil {
		t.Fatal("rendered config must be valid JSON", err)
	}
	cameraList, ok := config["cameras"].(map[string]any)
	if !ok || len(cameraList) != 1 {
		t.Fatal("only enabled cameras are rendered", config["cameras"])
	}
	camera, ok := cameraList["ch01"].(map[string]any)
	if !ok {
		t.Fatal("the camera key is its stable name")
	}
	// Detection must never pull from the camera directly (PRD §10.2): the
	// input is the media server's internal path.
	inputs := camera["ffmpeg"].(map[string]any)["inputs"].([]any)
	path := inputs[0].(map[string]any)["path"].(string)
	if !strings.HasPrefix(path, "rtsp://zlm:554/one_nvr/") {
		t.Fatal("detection input must be the ZLM internal path", path)
	}
	// Detection never owns recording; ZLM keeps the recording pipeline.
	if record := camera["record"].(map[string]any)["enabled"]; record != false {
		t.Fatal("record must stay disabled in Frigate", record)
	}
	track := camera["objects"].(map[string]any)["track"].([]any)
	if len(track) != 2 {
		t.Fatal("labels ride into objects.track", track)
	}
	// Deterministic bytes: the same rows must hash identically for the
	// revision ledger to mean anything.
	again, err := Render(base, cameras)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(rendered) {
		t.Fatal("rendering is not deterministic")
	}
}

// Frigate defaults tls.enabled to true, which adds a self-signed HTTPS
// listener plus a per-minute TLS handshake against 127.0.0.1:8971 (the
// certsync service). A deployment that never exposes Frigate directly must
// say so explicitly, or that loop runs forever.
func TestRenderDisablesFrigateTLS(t *testing.T) {
	base := map[string]any{
		"mqtt":    map[string]any{"host": "mqtt"},
		"version": "0.17-0",
		"tls":     map[string]any{"enabled": false},
	}
	rendered, err := Render(base, map[string]CameraConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(rendered, &config); err != nil {
		t.Fatal(err)
	}
	tlsSection, ok := config["tls"].(map[string]any)
	if !ok {
		t.Fatal("the rendered config must state tls explicitly")
	}
	if enabled, _ := tlsSection["enabled"].(bool); enabled {
		t.Fatal("Frigate TLS must be off: this deployment never exposes it")
	}
}

func TestCameraNameStable(t *testing.T) {
	if CameraName(1) != "ch01" || CameraName(16) != "ch16" {
		t.Fatal("camera names must be the fixed channel key", CameraName(1), CameraName(16))
	}
}

// An incomplete wiring used to surface only when an operator clicked publish,
// as an unhelpful "publish failed". The constructor must refuse it up front.
func TestNewCompleteRefusesIncompleteWiring(t *testing.T) {
	cases := []struct {
		name string
		in   ServiceConfig
	}{
		{"no config path", ServiceConfig{FrigateURL: "http://frigate:5000", MQTTKey: "k", ProbeToken: "p"}},
		{"no mqtt key", ServiceConfig{FrigateURL: "http://frigate:5000", ConfigPath: "/tmp/config.yml", ProbeToken: "p"}},
		{"no probe token", ServiceConfig{FrigateURL: "http://frigate:5000", ConfigPath: "/tmp/config.yml", MQTTKey: "k"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewComplete(nil, nil, testCase.in); err == nil {
				t.Fatal("an incomplete detection wiring must be refused at construction")
			}
		})
	}
	service, err := NewComplete(nil, nil, ServiceConfig{
		FrigateURL: "http://frigate:5000", ConfigPath: "/tmp/config.yml", MQTTKey: "k", ProbeToken: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.MQTTKey != "k" || service.ProbeToken != "p" || service.Reload == nil {
		t.Fatal("a complete wiring must carry the key, the probe token and the reload hook")
	}
}

// The detector must follow the deployment's validated hardware: an accelerated
// profile switches Frigate to the OpenVINO model on the GPU, and anything else
// stays on CPU. Publishing a CPU detector on a GPU host was the reported bug.
func TestRenderFollowsInferenceProfile(t *testing.T) {
	cpu := &Service{MQTTKey: "k"}
	config, err := cpu.baseConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := config["detectors"].(map[string]any)["cpu"]; !ok {
		t.Fatal("without a validated accelerator the detector stays on CPU")
	}
	// An explicit "cpu" profile must also stay on the CPU path.
	explicitCPU := &Service{MQTTKey: "k", Inference: "cpu"}
	config, err = explicitCPU.baseConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := config["detectors"].(map[string]any)["cpu"]; !ok {
		t.Fatal("an explicit cpu profile must not select OpenVINO")
	}
	if model, _ := config["model"].(map[string]any)["path"].(string); model != "/cpu_model.tflite" {
		t.Fatalf("cpu profile must use the bundled CPU model, got %q", model)
	}

	// A validated Intel proposal carries its PCI address, not the word
	// "intel": matching on the vendor name would silently fall back to CPU on
	// every real deployment (the reported bug).
	accelerated := &Service{MQTTKey: "k", Inference: "0000:00:02.0"}
	config, err = accelerated.baseConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ov, ok := config["detectors"].(map[string]any)["intel"].(map[string]any)
	if !ok {
		t.Fatal("a validated intel profile must select the OpenVINO detector")
	}
	if ov["type"] != "openvino" || ov["device"] != "GPU" {
		t.Fatalf("OpenVINO detector must target the GPU, got %v", ov)
	}
	model := config["model"].(map[string]any)
	if model["path"] != "/openvino-model/ssdlite_mobilenet_v2.xml" {
		t.Fatalf("accelerated profile must use the image's bundled OpenVINO model, got %v", model["path"])
	}
	if model["labelmap_path"] == "" || model["input_tensor"] != "nhwc" {
		t.Fatalf("the OpenVINO model must match the validated profile, got %v", model)
	}
}

// The sensitivity profile must reach Frigate as a filters block. Without it
// every camera runs on Frigate's defaults (min_score 0.5), which is what made a
// driveway with a parked scooter both miss slow movement and fire on nothing.
func TestRenderCarriesTheSensitivityProfile(t *testing.T) {
	base := map[string]any{
		"mqtt":      map[string]any{"host": "mqtt"},
		"detectors": map[string]any{"cpu": map[string]any{"type": "cpu"}},
		"model":     map[string]any{"path": "/cpu_model.tflite"},
	}
	cases := []struct {
		sensitivity Sensitivity
		minScore    float64
		threshold   float64
		minArea     float64
	}{
		{SensitivitySensitive, 0.35, 0.45, 800},
		{SensitivityBalanced, 0.50, 0.55, 1500},
		{SensitivityStrict, 0.65, 0.70, 3000},
	}
	for _, c := range cases {
		t.Run(string(c.sensitivity), func(t *testing.T) {
			cameras := map[string]CameraConfig{
				"ch01": {Enabled: true, InputURL: "rtsp://zlm:554/one_nvr/s", FPS: 5, Width: 640, Height: 360,
					Labels: []string{"person", "car"}, Sensitivity: c.sensitivity},
			}
			rendered, err := Render(base, cameras)
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]any
			if err := json.Unmarshal(rendered, &config); err != nil {
				t.Fatal(err)
			}
			objects := config["cameras"].(map[string]any)["ch01"].(map[string]any)["objects"].(map[string]any)
			filters, ok := objects["filters"].(map[string]any)
			if !ok {
				t.Fatal("objects.filters must be rendered", objects)
			}
			// Every tracked label carries the same profile: sensitivity describes
			// the scene, not the object class.
			for _, label := range []string{"person", "car"} {
				block, ok := filters[label].(map[string]any)
				if !ok {
					t.Fatalf("%s must have a filter block", label)
				}
				if block["min_score"] != c.minScore || block["threshold"] != c.threshold || block["min_area"] != c.minArea {
					t.Fatalf("%s: want min_score=%v threshold=%v min_area=%v, got %v", label, c.minScore, c.threshold, c.minArea, block)
				}
			}
		})
	}
}

// An unset sensitivity renders the balanced profile, never a zero-score one: a
// zero min_score would detect everything, and an upgrade must not make a camera
// noisier than the operator configured.
func TestRenderDefaultsToBalanced(t *testing.T) {
	base := map[string]any{
		"mqtt":      map[string]any{"host": "mqtt"},
		"detectors": map[string]any{"cpu": map[string]any{"type": "cpu"}},
		"model":     map[string]any{"path": "/cpu_model.tflite"},
	}
	cameras := map[string]CameraConfig{
		"ch01": {Enabled: true, InputURL: "rtsp://zlm:554/one_nvr/s", FPS: 5, Width: 640, Height: 360, Labels: []string{"person"}},
	}
	rendered, err := Render(base, cameras)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(rendered, &config); err != nil {
		t.Fatal(err)
	}
	filters := config["cameras"].(map[string]any)["ch01"].(map[string]any)["objects"].(map[string]any)["filters"].(map[string]any)
	person := filters["person"].(map[string]any)
	if person["min_score"] != 0.50 {
		t.Fatalf("an unset sensitivity must render balanced, got %v", person)
	}
}
