package tlsmanager

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writePair(t *testing.T, dir string, chain, key []byte) {
	t.Helper()
	for name, data := range map[string][]byte{"fullchain.pem": chain, "privkey.pem": key} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func TestWatcherPartialReplacementAndSameMtime(t *testing.T) {
	dir := t.TempDir()
	first, key1 := certificateFixture(t, "ec-pkcs8", 1, false)
	second, key2 := certificateFixture(t, "ec-pkcs8", 2, false)
	writePair(t, dir, first, key1)
	w := &Watcher{Directory: dir, Host: "nvr.example.com"}
	at := time.Now()
	one, err := w.Sample(at)
	if err != nil || one.State != "stabilizing" {
		t.Fatalf("first sample: %+v %v", one, err)
	}
	early, err := w.Sample(at.Add(time.Second))
	if err != nil || early.State != "stabilizing" {
		t.Fatal("stable too early", err)
	}
	stable, err := w.Sample(at.Add(2 * time.Second))
	if err != nil || stable.State != "stable" {
		t.Fatal("stable pair missing", err)
	}
	oldDigest := stable.digest
	writePair(t, dir, second, key1)
	if sample, err := w.Sample(at.Add(3 * time.Second)); err == nil || sample.State != "unavailable" {
		t.Fatal("half replacement accepted", err)
	}
	writePair(t, dir, second, key2)
	for _, name := range []string{"fullchain.pem", "privkey.pem"} {
		if err := os.Chtimes(filepath.Join(dir, name), at, at); err != nil {
			t.Fatal(err)
		}
	}
	if sample, err := w.Sample(at.Add(4 * time.Second)); err != nil || sample.State != "stabilizing" {
		t.Fatal("new contents not recognized", err)
	}
	changed, err := w.Sample(at.Add(6 * time.Second))
	if err != nil || changed.State != "stable" || changed.digest == oldDigest {
		t.Fatal("same-mtime change lost", err)
	}
}
func TestWatcherDuplicateAndChainOnlyChange(t *testing.T) {
	dir := t.TempDir()
	leaf, root, key := signedChainFixture(t)
	writePair(t, dir, leaf, key)
	w := &Watcher{Directory: dir, Host: "nvr.example.com"}
	at := time.Now()
	if _, err := w.Sample(at); err != nil {
		t.Fatal(err)
	}
	first, err := w.Sample(at.Add(2 * time.Second))
	if err != nil || first.State != "stable" {
		t.Fatal(err)
	}
	again, err := w.Sample(at.Add(3 * time.Second))
	if err != nil || again.digest != first.digest {
		t.Fatal("duplicate identity changed", err)
	}
	writePair(t, dir, append(append([]byte(nil), leaf...), root...), key)
	if _, err := w.Sample(at.Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	updated, err := w.Sample(at.Add(6 * time.Second))
	if err != nil || updated.State != "stable" || updated.digest == first.digest || updated.Metadata.LeafSHA256 != first.Metadata.LeafSHA256 {
		t.Fatal("chain-only renewal lost", err)
	}
}
func TestWatcherRootBoundSymlinksAndLimit(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	chain, key := certificateFixture(t, "ec-pkcs8", 1, false)
	writePair(t, outside, chain, key)
	if err := os.Symlink(filepath.Join(outside, "fullchain.pem"), filepath.Join(dir, "fullchain.pem")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "privkey.pem"), key, 0600); err != nil {
		t.Fatal(err)
	}
	w := &Watcher{Directory: dir, Host: "nvr.example.com"}
	if _, err := w.Sample(time.Now()); err == nil {
		t.Fatal("outside symlink accepted")
	}
	if err := os.Remove(filepath.Join(dir, "fullchain.pem")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "actual.pem"), chain, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("actual.pem", filepath.Join(dir, "fullchain.pem")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Sample(time.Now()); err != nil {
		t.Fatal("inside symlink rejected", err)
	}
}
