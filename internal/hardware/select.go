package hardware

import (
	"fmt"
	"sort"
)

type Device struct {
	ID          string `json:"id"`
	Vendor      string `json:"vendor"`
	Node        string `json:"node"`
	Accelerator bool   `json:"accelerator"`
}
type Inventory struct {
	Known   bool     `json:"known"`
	Devices []Device `json:"devices"`
}
type Result struct {
	Decode       bool   `json:"decode"`
	Inference    bool   `json:"inference"`
	Reason       string `json:"reason,omitempty"`
	SampleSHA256 string `json:"sample_sha256,omitempty"`
	ModelSHA256  string `json:"model_sha256,omitempty"`
}
type Validation map[string]Result
type Proposal struct {
	ID          string `json:"id"`
	DecodeID    string `json:"decode_id"`
	InferenceID string `json:"inference_id"`
	Node        string `json:"node,omitempty"`
}

func Select(inv Inventory, v Validation, override string, prior *Proposal) (Proposal, error) {
	if !inv.Known && override == "cpu" && v["cpu"].Decode && v["cpu"].Inference {
		return Proposal{ID: "cpu", DecodeID: "cpu", InferenceID: "cpu"}, nil
	}
	if !inv.Known {
		if prior != nil {
			return *prior, fmt.Errorf("hardware inventory unknown")
		}
		return Proposal{}, fmt.Errorf("hardware inventory unknown")
	}
	devices := append([]Device(nil), inv.Devices...)
	sort.Slice(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })
	p := Proposal{ID: "cpu", DecodeID: "cpu", InferenceID: "cpu"}
	found := false
	for _, d := range devices {
		if d.Vendor != "8086" || !d.Accelerator || d.Node == "" {
			continue
		}
		if override == "auto" && prior != nil && prior.ID != "cpu" && d.ID != prior.ID {
			continue
		}
		if override == "cpu" {
			continue
		}
		r := v[d.ID]
		if r.Decode || r.Inference {
			p.ID = d.ID
			if r.Decode {
				p.DecodeID = d.ID
			}
			p.Node = d.Node
			found = true
			if r.Inference {
				p.InferenceID = d.ID
			}
			break
		}
	}
	if override == "auto" && prior != nil && prior.ID != "cpu" && (!found || (prior.DecodeID != "cpu" && p.DecodeID != prior.DecodeID) || (prior.InferenceID != "cpu" && p.InferenceID != prior.InferenceID)) {
		return *prior, fmt.Errorf("previous accelerator failed; no silent CPU fallback")
	}
	if (override == "intel-igpu" && !found) || override == "nvidia" {
		return p, fmt.Errorf("requested accelerator not validated")
	}
	if p.DecodeID == "cpu" && !v["cpu"].Decode {
		return p, fmt.Errorf("CPU decoding not validated")
	}
	if p.InferenceID == "cpu" && !v["cpu"].Inference {
		return p, fmt.Errorf("CPU inference not validated")
	}
	return p, nil
}
