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
