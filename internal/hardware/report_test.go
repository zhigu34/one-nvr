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
