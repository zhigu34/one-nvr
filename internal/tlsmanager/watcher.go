package tlsmanager

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"os"
	"sync"
	"syscall"
	"time"
)

type Sample struct {
	State      string   `json:"state"`
	Reason     string   `json:"reason"`
	Metadata   Metadata `json:"metadata"`
	chain, key []byte
	digest     [32]byte
}

// String deliberately excludes the private in-memory candidate material.
func (s Sample) String() string { return "TLS input " + s.State + " " + s.Reason }

type Watcher struct {
	Directory, Host string
	mu              sync.Mutex
	prior           [32]byte
	firstAt         time.Time
	failedDigest    [32]byte
	retryAt         time.Time
	lastError       error
}

func pairDigest(chain, key []byte) [32]byte {
	h := sha256.New()
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(chain)))
	h.Write(size[:])
	h.Write(chain)
	binary.BigEndian.PutUint64(size[:], uint64(len(key)))
	h.Write(size[:])
	h.Write(key)
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

type pairFiles struct {
	chain, key []byte
	infos      [2]os.FileInfo
}

func readPair(root *os.Root) (pairFiles, error) {
	var out pairFiles
	for i, name := range []string{"fullchain.pem", "privkey.pem"} {
		before, err := root.Stat(name)
		if err != nil || !before.Mode().IsRegular() || before.Size() > MaxPEMBytes {
			return out, validationError("tls_input_unavailable")
		}
		file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return out, validationError("tls_input_unavailable")
		}
		opened, err := file.Stat()
		if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
			file.Close()
			return out, validationError("tls_input_changed")
		}
		data, err := io.ReadAll(io.LimitReader(file, MaxPEMBytes+1))
		file.Close()
		after, statErr := root.Stat(name)
		if err != nil || statErr != nil || len(data) > MaxPEMBytes || !os.SameFile(opened, after) || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
			return out, validationError("tls_input_changed")
		}
		out.infos[i] = after
		if i == 0 {
			out.chain = data
		} else {
			out.key = data
		}
	}
	if len(out.chain)+len(out.key) > MaxPEMBytes {
		return out, validationError("tls_size_invalid")
	}
	for i, name := range []string{"fullchain.pem", "privkey.pem"} {
		after, err := root.Stat(name)
		if err != nil || !os.SameFile(out.infos[i], after) || out.infos[i].Size() != after.Size() || !out.infos[i].ModTime().Equal(after.ModTime()) {
			return out, validationError("tls_input_changed")
		}
	}
	return out, nil
}

func (w *Watcher) Sample(now time.Time) (Sample, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := Sample{State: "unavailable", Reason: "tls_input_unavailable"}
	failed := func(err error) (Sample, error) { w.firstAt = time.Time{}; return out, err }
	root, err := os.OpenRoot(w.Directory)
	if err != nil {
		return failed(validationError(out.Reason))
	}
	defer root.Close()
	first, err := readPair(root)
	if err != nil {
		return failed(err)
	}
	second, err := readPair(root)
	if err != nil {
		return failed(err)
	}
	for i := range first.infos {
		if !os.SameFile(first.infos[i], second.infos[i]) {
			return failed(validationError("tls_input_changed"))
		}
	}
	if !bytes.Equal(first.chain, second.chain) || !bytes.Equal(first.key, second.key) {
		return failed(validationError("tls_input_changed"))
	}
	dir, err := root.Open(".")
	if err != nil {
		return failed(validationError(out.Reason))
	}
	opened, err := dir.Stat()
	dir.Close()
	current, statErr := os.Stat(w.Directory)
	if err != nil || statErr != nil || !os.SameFile(opened, current) {
		return failed(validationError("tls_input_changed"))
	}
	digest := pairDigest(first.chain, first.key)
	if w.lastError != nil && w.failedDigest == digest && now.Before(w.retryAt) {
		return failed(w.lastError)
	}
	meta, err := Validate(first.chain, first.key, w.Host, now)
	if err != nil {
		w.failedDigest = digest
		w.retryAt = now.Add(30 * time.Second)
		w.lastError = err
		return failed(err)
	}
	w.lastError = nil
	out.digest = pairDigest(first.chain, first.key)
	out.Metadata = meta
	if w.firstAt.IsZero() || w.prior != out.digest || now.Before(w.firstAt) {
		w.prior = out.digest
		w.firstAt = now
	}
	out.State = "stabilizing"
	out.Reason = "stable_pair_required"
	if now.Sub(w.firstAt) >= 2*time.Second {
		out.State = "stable"
		out.Reason = "pair_validated"
		out.chain = first.chain
		out.key = first.key
	}
	return out, nil
}
