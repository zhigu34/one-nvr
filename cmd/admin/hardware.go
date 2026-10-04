package main

import (
	"encoding/json"
	"fmt"
	"github.com/zhigu34/one-nvr/internal/config"
	"github.com/zhigu34/one-nvr/internal/hardware"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

func hardwareCommand(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "hardware-failed":
		if e := hardware.RecordFailure("/data"); e != nil {
			return true, e
		}
		return true, os.Chown("/data/hardware/latest.json", 10001, 10001)
	case "hardware-nodes":
		var inv hardware.Inventory
		b, e := os.ReadFile("/data/hardware/inventory.json")
		if e != nil {
			return true, e
		}
		if e = json.Unmarshal(b, &inv); e != nil {
			return true, e
		}
		safe := regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)
		node := regexp.MustCompile(`^/dev/dri/renderD[0-9]+$`)
		for _, d := range inv.Devices {
			if d.Accelerator && d.Vendor == "8086" && safe.MatchString(d.ID) && node.MatchString(d.Node) {
				fmt.Printf("%s\t%s\n", d.ID, d.Node)
			}
		}
		return true, nil
	case "select-hardware":
		c, e := config.Load()
		if e != nil {
			return true, e
		}
		var inv hardware.Inventory
		b, e := os.ReadFile("/data/hardware/inventory.json")
		if e != nil {
			return true, e
		}
		if e = json.Unmarshal(b, &inv); e != nil {
			return true, e
		}
		validations := hardware.Validation{}
		ids := []string{"cpu"}
		for _, d := range inv.Devices {
			if d.Accelerator {
				ids = append(ids, d.ID)
			}
		}
		for _, id := range ids {
			if !regexp.MustCompile(`^[a-z0-9:._-]+$`).MatchString(id) {
				continue
			}
			b, e = os.ReadFile(filepath.Join("/data/hardware", "result-"+id+".json"))
			if e == nil {
				var r hardware.Result
				if json.Unmarshal(b, &r) == nil {
					validations[id] = r
				}
			}
		}
		var prior *hardware.Proposal
		var old struct {
			Proposal hardware.Proposal `json:"proposal"`
		}
		if b, e = os.ReadFile("/data/hardware/latest.json"); e == nil && json.Unmarshal(b, &old) == nil && old.Proposal.ID != "" {
			prior = &old.Proposal
		}
		p, selectionErr := hardware.Select(inv, validations, c.HardwareProfile, prior)
		report := map[string]any{"inventory": inv, "validation": validations, "proposal": p, "validated": selectionErr == nil, "checked_at": time.Now().UTC(), "sample_source": "ffmpeg lavfi testsrc2 320x240 5fps six libx264 frames", "image": config.ImagePins["frigate"]}
		if imageBytes, imageErr := os.ReadFile("/data/runtime/image-frigate"); imageErr == nil {
			report["image"] = string(imageBytes)
		}
		if selectionErr != nil {
			report["error_code"] = "hardware_validation_failed"
		}
		b, _ = json.MarshalIndent(report, "", "  ")
		if e = replaceFile("/data/hardware/latest.json", b, 0600); e != nil {
			return true, e
		}
		if e = os.Chown("/data/hardware/latest.json", 10001, 10001); e != nil {
			return true, e
		}
		if selectionErr != nil {
			return true, selectionErr
		}
		path := "/data/frigate/config.yml"
		b, e = os.ReadFile(path)
		if e != nil {
			return true, e
		}
		cfg := map[string]any{}
		if e = json.Unmarshal(b, &cfg); e != nil {
			return true, e
		}
		if p.DecodeID != "cpu" {
			cfg["ffmpeg"] = map[string]any{"hwaccel_args": []string{"-hwaccel", "vaapi", "-hwaccel_device", p.Node, "-hwaccel_output_format", "vaapi"}}
		} else {
			delete(cfg, "ffmpeg")
		}
		if p.InferenceID != "cpu" {
			cfg["detectors"] = map[string]any{"intel": map[string]any{"type": "openvino", "device": "GPU"}}
			cfg["model"] = map[string]any{"path": "/openvino-model/ssdlite_mobilenet_v2.xml", "width": 300, "height": 300, "input_tensor": "nhwc", "input_pixel_format": "bgr", "labelmap_path": "/openvino-model/coco_91cl_bkgr.txt"}
		} else {
			cfg["detectors"] = map[string]any{"cpu": map[string]any{"type": "cpu", "num_threads": 2}}
			cfg["model"] = map[string]any{"path": "/cpu_model.tflite"}
		}
		b, _ = json.MarshalIndent(cfg, "", "  ")
		if e = replaceFile(path, b, 0600); e != nil {
			return true, e
		}
		override := map[string]any{"services": map[string]any{"frigate": map[string]any{"devices": []string{}}}}
		if p.Node != "" {
			override["services"].(map[string]any)["frigate"] = map[string]any{"devices": []string{p.Node + ":" + p.Node}}
		}
		b, _ = json.MarshalIndent(override, "", "  ")
		return true, replaceFile("/data/runtime/hardware.compose.json", b, 0600)
	}
	return false, nil
}
