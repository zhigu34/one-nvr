package storage

import (
	"bytes"
	"context"
	"fmt"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"io"
	"os"
	"syscall"
	"time"
)

const MinimumFreeBytes int64 = 10 * 1024 * 1024 * 1024

type Check struct {
	Service      string    `json:"service"`
	State        string    `json:"state"`
	Reason       string    `json:"reason"`
	ObservedAt   time.Time `json:"observed_at,omitzero"`
	ExpiresAt    time.Time `json:"expires_at,omitzero"`
	TotalBytes   int64     `json:"total_bytes"`
	FreeBytes    int64     `json:"free_bytes"`
	FilesystemID string    `json:"filesystem_id"`
}
type Probe struct {
	Roots   []string
	Service string
}

func (p *Probe) Check(ctx context.Context, pool Pool) (Check, error) {
	now := time.Now().UTC()
	out := Check{Service: p.Service, State: "unavailable", Reason: "root_unavailable", ObservedAt: now, ExpiresAt: now.Add(30 * time.Second)}
	if p.Service != "api" && p.Service != "worker" && p.Service != "zlm" {
		return out, auth.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	// Without a media source, neither API nor Worker can establish ZLM writeability.
	if p.Service == "zlm" {
		out.State = "pending"
		out.Reason = "test_source_required"
		return out, nil
	}
	root, canonical, err := openPool(p.Roots, pool.Path)
	if err != nil {
		return out, nil
	}
	defer root.Close()
	if canonical != pool.Path {
		return out, nil
	}
	out.Reason = "identity_unavailable"
	if verifyMarker(root, pool) != nil {
		return out, nil
	}
	out.Reason = "read_write_delete_failed"
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err = root.MkdirAll(".work/probes", 0700); err != nil {
		return out, nil
	}
	nonce, err := id.New()
	if err != nil {
		return out, err
	}
	path := ".work/probes/" + p.Service + "-" + string(nonce)
	if err = ctx.Err(); err != nil {
		return out, err
	}
	f, err := root.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return out, nil
	}
	defer root.Remove(path)
	payload := []byte("one-nvr pool probe " + string(nonce))
	if _, err = f.Write(payload); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return out, nil
	}
	reader, err := root.Open(path)
	if err != nil {
		return out, nil
	}
	read, err := io.ReadAll(io.LimitReader(reader, int64(len(payload)+1)))
	reader.Close()
	if err != nil || !bytes.Equal(read, payload) {
		return out, nil
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if err = root.Remove(path); err != nil {
		return out, nil
	}
	out.Reason = "capacity_unavailable"
	dir, err := root.Open(".")
	if err != nil {
		return out, nil
	}
	defer dir.Close()
	var fs syscall.Statfs_t
	if err = syscall.Fstatfs(int(dir.Fd()), &fs); err != nil {
		return out, nil
	}
	info, err := dir.Stat()
	if err != nil {
		return out, nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return out, nil
	}
	out.FilesystemID = fmt.Sprint(stat.Dev)
	// Saturate multiplication rather than overflowing a reported capacity.
	out.TotalBytes = capacityBytes(uint64(fs.Blocks), uint64(fs.Bsize))
	out.FreeBytes = capacityBytes(uint64(fs.Bavail), uint64(fs.Bsize))
	if out.FreeBytes > out.TotalBytes {
		out.FreeBytes = out.TotalBytes
	}
	out.State = "healthy"
	out.Reason = "read_write_delete_verified"
	return out, nil
}

func capacityBytes(blocks, size uint64) int64 {
	const max = uint64(1<<63 - 1)
	if size == 0 {
		return 0
	}
	if blocks > max/size {
		return int64(max)
	}
	return int64(blocks * size)
}

func (p Pool) Readiness(now time.Time) string {
	if !p.Enabled {
		return "disabled"
	}
	checks := map[string]Check{}
	for _, c := range p.Checks {
		checks[c.Service] = c
	}
	pending := false
	for _, name := range []string{"api", "worker", "zlm"} {
		c, ok := checks[name]
		if !ok || !c.ExpiresAt.After(now) {
			pending = true
			continue
		}
		if c.State == "unavailable" {
			return "unavailable"
		}
		if c.State != "healthy" {
			pending = true
		}
		if c.State == "healthy" && name != "zlm" && c.FreeBytes < MinimumFreeBytes {
			return "low_space"
		}
	}
	if pending {
		return "pending"
	}
	return "ready"
}

type Capacity struct {
	FilesystemCount int   `json:"filesystem_count"`
	TotalBytes      int64 `json:"total_bytes"`
	FreeBytes       int64 `json:"free_bytes"`
}

func AggregateCapacity(pools []Pool, now time.Time) Capacity {
	systems := map[string]Check{}
	for _, p := range pools {
		for _, c := range p.Checks {
			if c.Service == "zlm" || c.State != "healthy" || !c.ExpiresAt.After(now) || c.FilesystemID == "" {
				continue
			}
			old, ok := systems[c.FilesystemID]
			if !ok {
				systems[c.FilesystemID] = c
				continue
			}
			if c.FreeBytes < old.FreeBytes {
				old.FreeBytes = c.FreeBytes
			}
			if c.TotalBytes < old.TotalBytes {
				old.TotalBytes = c.TotalBytes
			}
			systems[c.FilesystemID] = old
		}
	}
	out := Capacity{FilesystemCount: len(systems)}
	for _, c := range systems {
		out.TotalBytes = saturatingAdd(out.TotalBytes, c.TotalBytes)
		out.FreeBytes = saturatingAdd(out.FreeBytes, c.FreeBytes)
	}
	return out
}
func saturatingAdd(a, b int64) int64 {
	if b > 1<<63-1-a {
		return 1<<63 - 1
	}
	return a + b
}
