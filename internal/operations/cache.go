package operations

// PRD REC-09: the event ring buffer lives in a tmpfs, and an operator deciding
// whether to enlarge it needs to see how full it is.
//
// This is host infrastructure, not business data, so it travels with the other
// DASH-02 figures and is administrator-only for the same reason.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// EventCache reports the in-memory event buffer's capacity and usage.
//
// Known is false when the cache is not mounted, which is a normal deployment:
// the feature is opt-in and a site that does not use it must not be shown a
// misleading 0%. The two cases are distinguished by Configured, so the page can
// say "未启用" rather than "0% used".
type EventCache struct {
	// Configured is true when the deployment reserved memory for the cache.
	Configured bool `json:"configured"`
	// Known is true when the figures below were actually read.
	Known   bool    `json:"known"`
	Path    string  `json:"path"`
	Total   uint64  `json:"total_bytes"`
	Used    uint64  `json:"used_bytes"`
	Percent float64 `json:"percent"`
}

// readEventCache measures the mount at path.
//
// It uses statfs rather than walking the directory: the ring buffer churns
// thousands of small files, and a walk would cost more than the measurement is
// worth while reporting the same number.
func readEventCache(path string) EventCache {
	out := EventCache{Path: path}
	if path == "" {
		return out
	}
	out.Configured = true
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		// A missing mount point is "not deployed here", not a failure to show:
		// the container is running without the cache configured.
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			return EventCache{Path: path}
		}
		return out
	}
	// Block counts are in units of Bsize; Bsize is the filesystem block size,
	// which for tmpfs equals the page size.
	blockSize := uint64(stat.Bsize)
	total := stat.Blocks * blockSize
	// A tmpfs reports its *limit* as Blocks and its current usage as
	// Total-Free, so "available to write" is Bavail. Using Total-Free would
	// count nothing allocated yet and always read 0%.
	used := (stat.Blocks - stat.Bfree) * blockSize
	if total == 0 {
		return out
	}
	out.Total = total
	out.Used = used
	out.Percent = round1(float64(used) / float64(total) * 100)
	out.Known = true
	return out
}

// attach the cache reading to the sampler so the resource call returns one
// snapshot observed at one instant.
func (h *hostSampler) withEventCache(ctx context.Context, in HostResources) HostResources {
	_ = ctx
	in.EventCache = readEventCache(h.eventCachePath)
	return in
}
