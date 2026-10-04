// Package secrets owns deployment identity and persistent private key material.
package secrets

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/zhigu34/one-nvr/internal/id"
	"os"
	"path/filepath"
)

type State struct {
	Version    int    `json:"version"`
	SiteID     id.ID  `json:"site_id"`
	MasterKey  string `json:"master_key"`
	CSRFKey    string `json:"csrf_key"`
	SetupToken string `json:"setup_token"`
}

func (s State) String() string { return "<one-nvr secrets redacted>" }
func randomKey() (string, error) {
	var b [32]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}
func Init(dataDir string) (State, error) {
	dir := filepath.Join(dataDir, "secrets")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return State{}, err
	}
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return State{}, err
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return State{}, fmt.Errorf("secret directory must be private and not a symlink")
	}
	filename := filepath.Join(dir, "one-nvr.json")
	if _, err := os.Lstat(filename); err == nil {
		return Load(dataDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return State{}, err
	}
	site, err := id.New()
	if err != nil {
		return State{}, err
	}
	master, err := randomKey()
	if err != nil {
		return State{}, err
	}
	csrf, err := randomKey()
	if err != nil {
		return State{}, err
	}
	token, err := randomKey()
	if err != nil {
		return State{}, err
	}
	state := State{1, site, master, csrf, token}
	raw, err := json.Marshal(state)
	if err != nil {
		return State{}, err
	}
	f, err := os.CreateTemp(dir, ".init-*")
	if err != nil {
		return State{}, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return State{}, err
	}
	if closeErr != nil {
		return State{}, closeErr
	}
	// Link publishes a fully synced file without replacing a concurrent initializer.
	if err = os.Link(f.Name(), filename); err != nil && !errors.Is(err, os.ErrExist) {
		return State{}, err
	}
	if d, err := os.Open(dir); err == nil {
		err = d.Sync()
		d.Close()
		if err != nil {
			return State{}, err
		}
	} else {
		return State{}, err
	}
	return Load(dataDir)
}
func Load(dataDir string) (State, error) {
	var state State
	dir := filepath.Join(dataDir, "secrets")
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return state, fmt.Errorf("invalid secret directory")
	}
	filename := filepath.Join(dir, "one-nvr.json")
	info, err = os.Lstat(filename)
	if err != nil {
		return state, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
		return state, fmt.Errorf("invalid secret file")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return state, err
	}
	defer root.Close()
	f, err := root.Open("one-nvr.json")
	if err != nil {
		return state, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return State{}, fmt.Errorf("corrupt secrets; refusing regeneration")
	}
	if state.Version != 1 {
		return State{}, fmt.Errorf("unsupported secret version")
	}
	if _, err := id.Parse(string(state.SiteID)); err != nil {
		return State{}, fmt.Errorf("invalid site identity")
	}
	for _, key := range []string{state.MasterKey, state.CSRFKey, state.SetupToken} {
		b, err := hex.DecodeString(key)
		if err != nil || len(b) != 32 {
			return State{}, fmt.Errorf("invalid secret key")
		}
	}
	return state, nil
}
