package recording

// PRD REC-09: the pre-event ring buffer.
//
// Measured on the live site before writing this: with max_second=3 against a
// camera whose GOP is 3.3 seconds, ZLM produced 3.3-4.0 second fragments of about
// 1.4 MB each, and it did NOT delete older ones. Two consequences drive the code
// below:
//
//  1. The ring's granularity is the camera's GOP, not the requested second. A
//     five-second lead-in therefore yields five to eight seconds of footage. That
//     is better than short, and the assembled clip reports its real span.
//  2. Eviction is ours. ZLM keeps writing until the tmpfs fills, so a buffer with
//     no sweep is a buffer that eventually stops the whole media server.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

// BufferFragment is one completed MP4 in the ring.
type BufferFragment struct {
	Path  string
	Start time.Time
	End   time.Time
	Size  int64
}

// ParseBufferFragment reads the start time out of ZLM's recording file name.
//
// ZLM writes <date>/<YYYY-MM-DD-HH-MM-SS-seq>.mp4 in the stream's directory. The
// name is the only timing evidence available here — there is no completion
// callback for these files because they are not published recordings — so the
// parser is deliberately strict: a name it cannot read is skipped rather than
// guessed at, because a wrong start time would place footage in the wrong window.
//
// The name is in the container's LOCAL time, not UTC. Measured on the live site:
// the media server runs with TZ=Asia/Shanghai and named a fragment 01:08 while
// UTC was 17:12 the previous day. Parsing that as UTC put every fragment eight
// hours in the future, so nothing ever looked expired and the ring grew without
// bound until it would have filled the tmpfs. The zone is therefore passed in and
// applied to both the name and the mtime.
func ParseBufferFragment(path string, info os.FileInfo, zone *time.Location) (BufferFragment, bool) {
	// A file ZLM is still writing carries a leading dot and has no moov yet.
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") || !strings.HasSuffix(base, ".mp4") {
		return BufferFragment{}, false
	}
	// 2026-10-19-00-16-31-7.mp4
	trimmed := strings.TrimSuffix(base, ".mp4")
	parts := strings.Split(trimmed, "-")
	if len(parts) < 7 {
		return BufferFragment{}, false
	}
	seqSeparated := strings.Join(parts[:6], "-")
	if zone == nil {
		zone = time.Local
	}
	local, err := time.ParseInLocation("2006-01-02-15-04-05", seqSeparated, zone)
	if err != nil {
		return BufferFragment{}, false
	}
	return BufferFragment{
		Path:  path,
		Start: local.UTC(),
		// The end is the instant ZLM closed the file. mtime is reported in the
		// process's location, and converting it to UTC keeps every comparison in
		// this package on one clock.
		End:  info.ModTime().UTC(),
		Size: info.Size(),
	}, true
}

// BufferEvictions returns the fragments that must be deleted to keep at least
// keepSeconds of history after now, and to stay under the byte budget.
//
// Two limits, because either one alone is insufficient: a fixed keep window
// bounds the time a detection can look back, and a byte budget bounds what the
// tmpfs can hold when several channels record at high bitrate. The site that
// prompted this measured 3.4 Mbps per channel, so sixteen channels at a thirty
// second window need about 200 MB — well inside a 512 MB cache, but a 4K site
// would not be, and an unbounded window would silently fill it.
//
// Eviction is oldest-first and never removes a fragment that still overlaps the
// oldest instant we promised to keep.
func BufferEvictions(fragments []BufferFragment, now time.Time, keepSeconds int, maxBytes int64) []BufferFragment {
	sorted := make([]BufferFragment, len(fragments))
	copy(sorted, fragments)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start.Before(sorted[j].Start) })

	cutoff := now.Add(-time.Duration(keepSeconds) * time.Second)
	var total int64
	for _, fragment := range sorted {
		total += fragment.Size
	}
	var evict []BufferFragment
	for _, fragment := range sorted {
		expired := !fragment.End.After(cutoff)
		overBudget := total > maxBytes
		if !expired && !overBudget {
			break
		}
		// Even when over budget, removal starts from the oldest end: the newest
		// history is what a detection arriving now needs.
		evict = append(evict, fragment)
		total -= fragment.Size
	}
	return evict
}

// BufferWindow selects the fragments covering [from, to], returning them in
// playback order plus the span they actually cover.
//
// The coverage is not the requested window: fragments are cut at keyframes, so
// the first one usually starts before `from` and the last one ends after `to`.
// Reporting the real span is what lets the assembled clip's index match its
// bytes (REC-11).
func BufferWindow(fragments []BufferFragment, from, to time.Time) ([]BufferFragment, time.Time, time.Time) {
	var picked []BufferFragment
	for _, fragment := range fragments {
		// Half-open overlap: a fragment ending exactly at `from` contributes
		// nothing, and one starting exactly at `to` is outside the window.
		if !fragment.End.After(from) || !fragment.Start.Before(to) {
			continue
		}
		picked = append(picked, fragment)
	}
	if len(picked) == 0 {
		return nil, time.Time{}, time.Time{}
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i].Start.Before(picked[j].Start) })
	start := picked[0].Start
	end := picked[0].End
	for _, fragment := range picked[1:] {
		if fragment.End.After(end) {
			end = fragment.End
		}
	}
	return picked, start, end
}

// ScanBufferFragments lists the completed fragments under one buffer directory.
//
// Exported so the integration tests exercise the same parser the worker uses;
// a second implementation in the tests is how parsing rules drift apart.
func ScanBufferFragments(root string) ([]BufferFragment, error) {
	var out []BufferFragment
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			// A directory that vanished mid-walk is normal: the sweeper deletes
			// from the same tree.
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".mp4") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if fragment, ok := ParseBufferFragment(path, info, time.Local); ok {
			out = append(out, fragment)
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return out, err
}

// evictBuffer removes expired fragments, returning how many bytes were freed.
//
// Files are unlinked, not truncated: a half-removed MP4 would parse as damaged
// and could be handed to the assembler as if it were complete.
func evictBuffer(fragments []BufferFragment, now time.Time, keepSeconds int, maxBytes int64) (int64, error) {
	var freed int64
	for _, fragment := range BufferEvictions(fragments, now, keepSeconds, maxBytes) {
		if err := os.Remove(fragment.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return freed, err
		}
		freed += fragment.Size
	}
	return freed, nil
}

// BufferSweepInterval bounds how long the ring may sit over its limits. Three
// seconds keeps the cache well inside its budget at the measured fragment rate
// (about one fragment per 3.3 seconds per channel) without walking the tree on
// every scheduler tick.
const BufferSweepInterval = 3 * time.Second

// RunBufferSweeper evicts expired fragments for every buffer this worker owns.
func (s *Service) RunBufferSweeper(ctx context.Context) {
	ticker := time.NewTicker(BufferSweepInterval)
	defer ticker.Stop()
	for {
		s.sweepBuffers(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// EventCacheDir is where this process sees the buffer mount, empty when the
// deployment reserved no memory for it.
func (s *Service) eventCacheRoot() string {
	return s.EventCacheDir
}

// sweepBuffers evicts expired fragments for every buffer this worker owns.
//
// Ownership matters: the cache path is per-worker, and two workers sweeping the
// same tree would race each other's unlinks. Rows carry an owner token for that
// reason, and the heartbeat below keeps a dead owner's row from blocking the
// channel forever.
func (s *Service) sweepBuffers(ctx context.Context) {
	root := s.eventCacheRoot()
	if root == "" || s.DB == nil {
		return
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT channel_id,cache_path,keep_seconds,owner_token FROM event_buffers`)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("event buffer sweep unavailable", "error", err)
		}
		return
	}
	type buffer struct {
		channel    id.ID
		path       string
		keep       int
		ownerToken string
	}
	var buffers []buffer
	for rows.Next() {
		var item buffer
		if err := rows.Scan(&item.channel, &item.path, &item.keep, &item.ownerToken); err != nil {
			rows.Close()
			return
		}
		buffers = append(buffers, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return
	}
	for _, item := range buffers {
		if ctx.Err() != nil {
			return
		}
		// A buffer owned by another worker on the same host would still share
		// the tmpfs only if the mount were shared; each container has its own.
		// Skipping is therefore not a correctness measure but avoids two
		// sweeps fighting over the same directory after a restart overlap.
		if item.ownerToken != s.bufferOwnerToken() {
			continue
		}
		fragments, err := ScanBufferFragments(item.path)
		if err != nil {
			slog.Warn("event buffer scan failed", "channel", item.channel, "error", err)
			continue
		}
		if _, err := evictBuffer(fragments, time.Now().UTC(), item.keep, s.bufferByteBudget()); err != nil {
			slog.Warn("event buffer eviction failed", "channel", item.channel, "error", err)
		}
	}
}

// bufferByteBudget divides the cache between the channels currently buffering.
//
// Every channel gets an equal share of whatever the deployment reserved. A fixed
// per-channel budget would either waste the cache on a two-channel site or
// overcommit it on a thirty-two channel one; the measured fragment size (about
// 1.4 MB per 3.3 seconds at 3.4 Mbps) makes an equal share predictable.
func (s *Service) bufferByteBudget() int64 {
	var count int64
	if s.DB != nil {
		_ = s.DB.Pool.QueryRow(context.Background(), `SELECT count(*) FROM event_buffers`).Scan(&count)
	}
	total := s.EventCacheBytes
	if total <= 0 || count <= 0 {
		return 0
	}
	return total / count
}

// bufferOwner is this process's identity for the rows it claims.
//
// It is generated once per process: a restart must not adopt the previous
// process's rows, because the tmpfs they describe is empty after a restart. The
// stale sweep uses the heartbeat to clear those rows so the channel buffers
// again instead of reporting "already buffering" with nothing writing.
var (
	bufferOwnerOnce  sync.Once
	bufferOwnerValue string
)

func (s *Service) bufferOwnerToken() string {
	bufferOwnerOnce.Do(func() {
		token, err := id.New()
		if err != nil {
			token = id.ID(time.Now().UTC().Format("20060102150405.000000000"))
		}
		bufferOwnerValue = string(token)
	})
	return bufferOwnerValue
}

// touchBuffers refreshes the heartbeat of the buffers this process owns.
//
// A buffer whose heartbeat stops is a buffer whose worker is gone. The scheduler
// treats a stale row as no buffer, so the channel either starts a fresh one (if
// the worker is restarted) or reports the buffer as unavailable rather than
// leaving an operator to wonder why event footage never appears.
func (s *Service) TouchBuffers(ctx context.Context) {
	if s.DB == nil {
		return
	}
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE event_buffers SET heartbeat_at=clock_timestamp() WHERE owner_token=$1`, s.bufferOwnerToken()); err != nil && ctx.Err() == nil {
		slog.Warn("event buffer heartbeat unavailable", "error", err)
	}
}

// BufferStaleAfter is how long a buffer row may go without a heartbeat before the
// scheduler stops trusting it. Two minutes is far longer than the sweep interval
// and comfortably shorter than an operator's patience.
const BufferStaleAfter = 2 * time.Minute
