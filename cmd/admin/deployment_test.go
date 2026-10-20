package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImmutableDeploymentRefusesOverwrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state")
	if e := immutableFile(p, []byte("one"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := immutableFile(p, []byte("two"), 0600); e == nil {
		t.Fatal("immutable overwrite")
	}
	b, _ := os.ReadFile(p)
	if string(b) != "one" {
		t.Fatal("changed")
	}
}
func TestLostSecretsWithExistingStateAreNotRegenerated(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "postgres"), 0700)
	os.WriteFile(filepath.Join(root, "postgres/PG_VERSION"), []byte("17"), 0600)
	if e := validateRuntimeIdentity(root); e == nil {
		t.Fatal("existing DB allowed missing secrets regeneration")
	}
}

func TestManagedRuntimeEntriesAllowPrivateRecordingSpool(t *testing.T) {
	root := t.TempDir()
	spool := filepath.Join(root, "recording-spool")
	if err := os.Mkdir(spool, 0700); err != nil {
		t.Fatal(err)
	}
	acknowledged := filepath.Join(spool, "acknowledged.json")
	if err := os.WriteFile(acknowledged, []byte("preserved callback"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeEntries(root); err != nil {
		t.Fatal("repeat deployment rejected managed spool", err)
	}
	if raw, err := os.ReadFile(acknowledged); err != nil || string(raw) != "preserved callback" {
		t.Fatal("spool entry altered", err)
	}
	if err := os.Mkdir(filepath.Join(root, "unrelated"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeEntries(root); err == nil {
		t.Fatal("unrelated content accepted")
	}
}
func TestManagedRuntimeEntriesRejectSpoolSymlink(t *testing.T) {
	for _, kind := range []string{"file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "recording-spool")
			var err error
			if kind == "file" {
				err = os.WriteFile(path, []byte("unrelated"), 0600)
			} else {
				err = os.Symlink(t.TempDir(), path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := validateRuntimeEntries(root); err == nil {
				t.Fatal("unsafe recording-spool accepted")
			}
		})
	}
}

// Every managed directory must have a declared owner, and OpenList's must be the
// uid its image actually runs as.
//
// This shipped broken: the loop excluded openlist from chown along with the
// root-running services, leaving Docker to create it as root:root 700. The
// container then exited on its first boot with "Current user does not have write
// and/or execute permissions for the ./data directory". The failure is invisible
// in every unit test and every compose config check — only the container knows.
func TestRuntimeOwnersCoverEveryManagedDirectory(t *testing.T) {
	managed := []string{"", "secrets", "gateway", "tls", "tls/versions", "runtime", "hardware", "postgres", "mqtt", "frigate", "openlist"}
	for _, name := range managed {
		if _, ok := runtimeOwners[name]; !ok {
			t.Fatalf("data directory %q has no declared owner; it would stay root owned", name)
		}
	}
}

func TestOpenListDataDirectoryBelongsToItsImageUser(t *testing.T) {
	// The official image runs as uid 1001 and checks this at startup.
	if uid := runtimeOwners["openlist"]; uid != 1001 {
		t.Fatalf("openlist data must belong to uid 1001, got %d", uid)
	}
	// The one-nvr service user must not be used for it: that is the mistake this
	// table exists to prevent.
	if uid := runtimeOwners["openlist"]; uid == 10001 {
		t.Fatal("openlist does not run as the one-nvr user")
	}
}
