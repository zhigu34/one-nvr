package storage

import (
	"encoding/json"
	"errors"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"io"
	"os"
)

const markerName = ".one-nvr.json"

var reservedDirectories = []string{"recordings", "snapshots", "exports", ".work", ".meta"}

type Marker struct {
	Version int    `json:"version"`
	SiteID  id.ID  `json:"site_id"`
	PoolID  id.ID  `json:"pool_id"`
	Path    string `json:"path"`
}

func readMarker(root *os.Root) (Marker, error) {
	var m Marker
	info, err := root.Lstat(markerName)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return m, auth.ErrInvalid
	}
	file, err := root.Open(markerName)
	if err != nil {
		return m, auth.ErrInvalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return m, auth.ErrInvalid
	}
	dec := json.NewDecoder(io.LimitReader(file, 4097))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, auth.ErrInvalid
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || m.Version != 1 || m.Path == "" {
		return m, auth.ErrInvalid
	}
	if _, err := id.Parse(string(m.SiteID)); err != nil {
		return m, auth.ErrInvalid
	}
	if _, err := id.Parse(string(m.PoolID)); err != nil {
		return m, auth.ErrInvalid
	}
	return m, nil
}

// publishMarker is only used by initial registration. A valid orphan from an
// interrupted DB commit is reusable by its original site/path; probes never call it.
func publishMarker(root *os.Root, want Marker) (Marker, error) {
	if _, err := root.Lstat(markerName); err == nil {
		existing, err := readMarker(root)
		if err != nil || existing.SiteID != want.SiteID || existing.Path != want.Path {
			return Marker{}, auth.ErrConflict
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Marker{}, auth.ErrInvalid
	}
	for _, name := range reservedDirectories {
		if _, err := root.Lstat(name); err == nil {
			return Marker{}, auth.ErrConflict
		} else if !errors.Is(err, os.ErrNotExist) {
			return Marker{}, auth.ErrInvalid
		}
	}
	nonce, err := id.New()
	if err != nil {
		return Marker{}, err
	}
	temp := ".one-nvr-" + string(nonce) + ".tmp"
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Marker{}, auth.ErrInvalid
	}
	defer root.Remove(temp)
	data, err := json.Marshal(want)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return Marker{}, auth.ErrInvalid
	}
	if err = root.Link(temp, markerName); err != nil {
		return Marker{}, auth.ErrConflict
	}
	dir, err := root.Open(".")
	if err != nil {
		return Marker{}, auth.ErrInvalid
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return Marker{}, auth.ErrInvalid
	}
	return want, nil
}

func verifyMarker(root *os.Root, pool Pool) error {
	m, err := readMarker(root)
	if err != nil || m.SiteID != pool.SiteID || m.PoolID != pool.ID || m.Path != pool.Path {
		return auth.ErrConflict
	}
	return nil
}
