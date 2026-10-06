//go:build linux || darwin

package secrets

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFirstDeploymentWithRestrictiveCreationMask(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "secrets"), 0700); err != nil {
		t.Fatal(err)
	}
	// init-runtime already prepares directories. Container process umask may
	// still remove file permissions when CreateTemp creates the first key file.
	old := syscall.Umask(0277)
	defer syscall.Umask(old)
	first, err := Init(dir)
	if err != nil {
		t.Fatal("first deployment must generate usable keys automatically:", err)
	}
	loaded, err := Load(dir)
	if err != nil || loaded != first {
		t.Fatal("generated identity was not usable", err)
	}
}
