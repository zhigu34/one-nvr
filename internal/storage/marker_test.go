package storage

import (
	"context"
	"github.com/zhigu34/one-nvr/internal/id"
	"os"
	"path/filepath"
	"testing"
)

func markedPool(t *testing.T) (Pool, []string) {
	t.Helper()
	base := t.TempDir()
	path := filepath.Join(base, "disk")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	root, canonical, err := openPool([]string{base}, path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	siteID, _ := id.New()
	poolID, _ := id.New()
	marker, err := publishMarker(root, Marker{Version: 1, SiteID: siteID, PoolID: poolID, Path: canonical})
	if err != nil {
		t.Fatal(err)
	}
	return Pool{ID: marker.PoolID, SiteID: siteID, Path: canonical, Enabled: true}, []string{base}
}

func TestMarkerNotRecreated(t *testing.T) {
	pool, roots := markedPool(t)
	if err := os.Remove(filepath.Join(pool.Path, ".one-nvr.json")); err != nil {
		t.Fatal(err)
	}
	check, err := (&Probe{Roots: roots, Service: "api"}).Check(context.Background(), pool)
	if err != nil || check.State != "unavailable" || check.Reason != "identity_unavailable" {
		t.Fatalf("lost identity: %+v %v", check, err)
	}
	if _, err := os.Stat(filepath.Join(pool.Path, ".one-nvr.json")); !os.IsNotExist(err) {
		t.Fatal("identity recreated")
	}
}

func TestReservedDirectoryCannotBeTakenOver(t *testing.T) {
	for _, name := range reservedDirectories {
		t.Run(name, func(t *testing.T) {
			path := t.TempDir()
			root, err := os.OpenRoot(path)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := root.Mkdir(name, 0700); err != nil {
				t.Fatal(err)
			}
			siteID, _ := id.New()
			poolID, _ := id.New()
			if _, err := publishMarker(root, Marker{Version: 1, SiteID: siteID, PoolID: poolID, Path: path}); err == nil {
				t.Fatal("existing business directory taken over")
			}
		})
	}
}

func TestLostRootNotRecreated(t *testing.T) {
	pool, roots := markedPool(t)
	if err := os.Rename(pool.Path, pool.Path+"-offline"); err != nil {
		t.Fatal(err)
	}
	check, err := (&Probe{Roots: roots, Service: "worker"}).Check(context.Background(), pool)
	if err != nil || check.State != "unavailable" || check.Reason != "root_unavailable" {
		t.Fatalf("lost root: %+v %v", check, err)
	}
	if _, err := os.Stat(pool.Path); !os.IsNotExist(err) {
		t.Fatal("root recreated")
	}
}
