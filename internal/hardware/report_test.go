package hardware

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHardwareReportRejectsSecretsAndInvalidInput(t *testing.T) {
	root := t.TempDir()
	if e := os.Mkdir(filepath.Join(root, "hardware"), 0700); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(root, "hardware/latest.json")
	os.WriteFile(path, []byte(`{"inventory":{"known":true,"devices":[]},"validation":{},"proposal":{"id":"cpu","decode_id":"cpu","inference_id":"cpu"},"validated":true,"password":"secret"}`), 0600)
	if _, e := ReadReport(root); e == nil {
		t.Fatal("unrecognized report fields exposed")
	}
}
func TestProbeFailureRetainsExpectedHardware(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "hardware"), 0700)
	path := filepath.Join(root, "hardware/latest.json")
	os.WriteFile(path, []byte(`{"inventory":{"known":true,"devices":[]},"validation":{},"proposal":{"id":"intel","decode_id":"intel","inference_id":"intel"},"validated":true,"checked_at":"2026-10-04T00:00:00Z","sample_source":"fixture","image":"fixed"}`), 0600)
	if e := RecordFailure(root); e != nil {
		t.Fatal(e)
	}
	r, e := ReadReport(root)
	if e != nil || r.Validated || r.Proposal.ID != "intel" || r.ErrorCode != "hardware_probe_failed" {
		t.Fatal("failed probe replaced expected GPU or retained success")
	}
}
