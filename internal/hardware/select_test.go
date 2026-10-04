package hardware

import "testing"

func TestAutoHardwareDisabledAndStableIdentity(t *testing.T) {
	inv := Inventory{Known: true, Devices: []Device{{ID: "0000:00:02.0", Vendor: "8086", Node: "/dev/dri/renderD129", Accelerator: true}, {ID: "0000:01:00.0", Vendor: "1a03", Node: "/dev/dri/renderD128"}}}
	p, e := Select(inv, Validation{"0000:00:02.0": {Decode: true, Inference: true}}, "auto", nil)
	if e != nil || p.ID != "0000:00:02.0" || p.Node != "/dev/dri/renderD129" {
		t.Fatalf("%+v %v", p, e)
	}
	p, e = Select(Inventory{Known: true}, Validation{"cpu": {Decode: true, Inference: true}}, "auto", nil)
	if e != nil || p.ID != "cpu" {
		t.Fatal("validated CPU selection")
	}
	if _, e = Select(Inventory{Known: true}, nil, "auto", nil); e == nil {
		t.Fatal("CPU needs validation")
	}
}
func TestAutoHardwareSeparatesDecodeAndInference(t *testing.T) {
	inv := Inventory{Known: true, Devices: []Device{{ID: "intel", Vendor: "8086", Accelerator: true, Node: "/dev/dri/renderD128"}}}
	p, e := Select(inv, Validation{"intel": {Decode: true}, "cpu": {Decode: true, Inference: true}}, "auto", nil)
	if e != nil || p.DecodeID != "intel" || p.InferenceID != "cpu" {
		t.Fatalf("%+v %v", p, e)
	}
}
func TestPriorGPUFailureDoesNotFallback(t *testing.T) {
	old := Proposal{ID: "intel", DecodeID: "intel", InferenceID: "cpu", Node: "/dev/dri/renderD128"}
	p, e := Select(Inventory{Known: true}, Validation{"cpu": {Decode: true, Inference: true}}, "auto", &old)
	if e == nil || p.ID != old.ID {
		t.Fatal("lost GPU must preserve expected profile and fail")
	}
}
func TestGPUInferenceFailureDoesNotSilentlyFallback(t *testing.T) {
	inv := Inventory{Known: true, Devices: []Device{{ID: "intel", Vendor: "8086", Accelerator: true, Node: "/dev/dri/renderD128"}}}
	prior := Proposal{ID: "intel", DecodeID: "intel", InferenceID: "intel", Node: "/dev/dri/renderD128"}
	if _, e := Select(inv, Validation{"intel": {Decode: true}, "cpu": {Decode: true, Inference: true}}, "auto", &prior); e == nil {
		t.Fatal("prior inference GPU failure silently falls back")
	}
}
func TestGPUInferenceIndependentOfDecoding(t *testing.T) {
	inv := Inventory{Known: true, Devices: []Device{{ID: "intel", Vendor: "8086", Accelerator: true, Node: "/dev/dri/renderD128"}}}
	p, e := Select(inv, Validation{"intel": {Inference: true}, "cpu": {Decode: true, Inference: true}}, "auto", nil)
	if e != nil || p.DecodeID != "cpu" || p.InferenceID != "intel" {
		t.Fatal("independent inference result ignored")
	}
}
func TestExplicitCPUWithUnknownGPUInventory(t *testing.T){p,e:=Select(Inventory{},Validation{"cpu":{Decode:true,Inference:true}},"epyc-cpu",nil);if e!=nil||p.ID!="cpu"{t.Fatal("explicit validated CPU requires no GPU inventory")}}
