package recording

// PRD REC-09: turning the ring buffer into a playable clip.
//
// A detection names a window; the ring holds fragments covering it. The clip is
// assembled by remuxing those fragments — never by transcoding, which is the same
// rule the export path follows and the reason a clip's boundaries land on
// fragments rather than on the exact millisecond.
//
// What this file deliberately does NOT do:
//
//   - It does not record continuously. The window is fixed when the job is
//     created, because an event that is still running would otherwise keep moving
//     its own extraction window.
//   - It does not touch a buffered fragment. Fragments are copied out, so the ring
//     keeps its history for the next detection. Moving them would make one event
//     steal footage from another.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/storage"
)

// QueueClip records that an event needs a clip assembled from the buffer.
//
// It is idempotent on the event: two detections for the same event (the stream
// and the reconciliation read) must not enqueue two jobs, and a second job would
// assemble the same footage twice.
func (s *Service) QueueClip(ctx context.Context, eventID, channelID id.ID, startedAt time.Time, endedAt *time.Time) error {
	window := ClipJobWindow{
		Start: startedAt.Add(-PreEventLead),
		End:   startedAt.Add(PostEventTrail),
	}
	if endedAt != nil {
		window.End = endedAt.Add(PostEventTrail)
	}
	_, err := s.DB.Pool.Exec(ctx, `INSERT INTO event_clip_jobs(event_id,channel_id,window_start,window_end)
		VALUES($1,$2,$3,$4) ON CONFLICT(event_id) DO NOTHING`,
		eventID, channelID, window.Start.UTC(), window.End.UTC())
	return err
}

// PreEventLead and PostEventTrail are the operator-chosen buffer window.
const (
	PreEventLead   = 5 * time.Second
	PostEventTrail = 5 * time.Second
)

// ClipJobWindow is the span a buffered clip must cover around an event, as the
// caller decided it.
type ClipJobWindow struct {
	Start time.Time
	End   time.Time
}

// AssembleClips drains queued clip jobs.
//
// Each job is best-effort: a channel whose buffer had already evicted the window,
// or a fragment that vanished between the listing and the copy, produces a
// failure recorded on the job rather than a crash. The event keeps whatever
// media_state the linking pass gave it, and the page shows "no footage" honestly.
func (s *Service) AssembleClips(ctx context.Context) {
	if s.DB == nil || s.EventCacheDir == "" || s.Probe == nil {
		return
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT j.event_id,j.channel_id,j.window_start,j.window_end,
			j.segment_id,j.clip_run_id,e.ended_at IS NULL
		FROM event_clip_jobs j LEFT JOIN events e ON e.id=j.event_id
		WHERE j.state IN ('queued','growing')
		ORDER BY (e.ended_at IS NULL) DESC, j.updated_at LIMIT 20`)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("clip job scan unavailable", "error", err)
		}
		return
	}
	var jobs []clipJob
	for rows.Next() {
		var item clipJob
		if err := rows.Scan(&item.Event, &item.Channel, &item.Start, &item.End, &item.Segment, &item.Run, &item.StillOpen); err != nil {
			rows.Close()
			return
		}
		jobs = append(jobs, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return
	}
	for _, item := range jobs {
		if ctx.Err() != nil {
			return
		}
		if err := s.assembleClip(ctx, item); err != nil {
			if ctx.Err() == nil {
				slog.Warn("clip assembly failed", "event", item.Event, "error", err)
			}
			_, _ = s.DB.Pool.Exec(ctx, `UPDATE event_clip_jobs SET state='failed',error_code=$2,updated_at=clock_timestamp() WHERE event_id=$1`,
				item.Event, clipErrorCode(err))
		}
	}
}

// clipJob is one unit of assembly work.
//
// Segment and Run are set once the clip exists: while an event runs the clip is
// re-cut from the accumulated fragments into the same file, so the event always
// points at one clip that simply gets longer. A row per assembly would leave the
// page choosing between forty versions of the same minute.
type clipJob struct {
	Event     id.ID
	Channel   id.ID
	Start     time.Time
	End       time.Time
	Segment   *id.ID
	Run       *id.ID
	StillOpen bool
}

// assembleClip cuts (or re-cuts) the clip for one job.
func (s *Service) assembleClip(ctx context.Context, job clipJob) error {
	eventID, channelID, from, to := job.Event, job.Channel, job.Start, job.End
	// Claiming the job first means a crash mid-assembly leaves it 'running'
	// rather than 'queued', so a retry does not duplicate a partially written
	// file. The sweeper below requeues stale claims.
	tag, err := s.DB.Pool.Exec(ctx, `UPDATE event_clip_jobs SET state='running',updated_at=clock_timestamp()
		WHERE event_id=$1 AND state IN ('queued','growing')`, eventID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	path := bufferPath(s.EventCacheDir, channelID)
	fragments, err := ScanBufferFragments(path)
	if err != nil {
		return err
	}
	picked, start, end := BufferWindow(fragments, from, to)

	// This run exists so the assembled clip has somewhere to belong: segments
	// reference a recording_runs row, and the foreign key is what keeps an index
	// entry from describing footage whose origin is unknown.
	var poolID, revisionID id.ID
	if err := s.DB.Pool.QueryRow(ctx, `SELECT revision.source_revision_id,ch.storage_pool_id
		FROM events ev JOIN channels ch ON ch.id=ev.channel_id
		JOIN LATERAL (SELECT ch.current_revision_id AS source_revision_id) revision ON true
		WHERE ev.id=$1 AND ch.storage_pool_id IS NOT NULL`, eventID).Scan(&revisionID, &poolID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errClipNoPool
		}
		return err
	}
	// Reuse the run when the clip already exists. A second run would leave the
	// previous file on disk with nothing pointing at it, and the naming rule
	// derives the target path from the run id — a new id means a new file.
	runID := id.ID("")
	if job.Run != nil {
		runID = *job.Run
	} else {
		created, err := id.New()
		if err != nil {
			return err
		}
		runID = created
	}
	work := ".work/event/" + string(runID)
	root, pool, err := s.Pools.OpenMediaRoot(ctx, poolID)
	if err != nil {
		return err
	}
	defer root.Close()

	// A clip that already exists is EXTENDED, never rebuilt from the ring.
	//
	// Rebuilding looked right and was wrong in two ways that only appear on a real
	// event. The ring keeps thirty seconds, so an event longer than that evicts its
	// own beginning: every re-cut started later and the assembled file could come
	// out SMALLER than the previous one (measured on the site: 13.7 MB, then
	// 10.9 MB for the same event). And the naming rule derives the file name from
	// the start instant, so a moving start named a different file each pass — the
	// index kept pointing at the first name while the identity columns described
	// the last, which the storage reconciler correctly read as "file replaced".
	//
	// Taking the published copy as the head of the next assembly fixes both: the
	// beginning cannot be evicted because it is no longer only in the ring, and the
	// name stops moving. Only fragments not yet folded into it are appended.
	var (
		head      probe.ConcatPart
		nameStart = start
		haveHead  bool
		headParts int
	)
	if job.Segment != nil {
		head, nameStart, haveHead, err = s.existingClipHead(ctx, *job.Segment, poolID, root)
		if err != nil {
			return err
		}
		if haveHead {
			// The head stands for however many fragments the published clip already
			// holds. The count lives on the segment's own media evidence, not on the
			// event: the event's media_segments is recomputed by the linking pass and
			// reading it here would count the same fragments twice.
			var prior int
			if err := s.DB.Pool.QueryRow(ctx, `SELECT coalesce((media_evidence->>'buffer_fragments')::int,0) FROM recording_segments WHERE id=$1`, *job.Segment).Scan(&prior); err != nil {
				return err
			}
			headParts = prior
		}
	}
	if haveHead {
		headEnd := nameStart.Add(head.Duration)
		fragments = fragmentsAfter(fragments, headEnd)
		picked, start, end = BufferWindow(fragments, from, to)
		if len(picked) == 0 {
			// Nothing new since the last assembly. Settling the job without
			// rewriting the file keeps a running-but-idle event from re-encoding
			// the same bytes every five seconds.
			return s.settleClip(ctx, eventID, job.StillOpen)
		}
	}
	if len(picked) == 0 {
		return errClipWindowEmpty
	}

	// The output is written inside the run's work directory, then named by the
	// standard rule, so the clip reads like every other recording in the library.
	outDir := filepath.Join(pool.Path, work)
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	staged := filepath.Join(outDir, "clip.mp4")
	parts := make([]probe.ConcatPart, 0, len(picked)+1)
	if haveHead {
		parts = append(parts, head)
	}
	for _, fragment := range picked {
		parts = append(parts, probe.ConcatPart{Path: fragment.Path, Duration: fragment.End.Sub(fragment.Start)})
	}
	evidence, err := s.Probe.ConcatMP4(ctx, parts, staged, nil)
	if err != nil {
		return err
	}
	if !evidence.Readable || evidence.Duration <= 0 {
		return errClipInvalid
	}
	info, err := os.Stat(staged)
	if err != nil {
		return err
	}

	channelNo, timezone, err := s.channelNaming(ctx, channelID)
	if err != nil {
		return err
	}
	// nameStart is fixed by the first assembly and read back from the segment on
	// every later one, so all assemblies of one clip land on one file.
	frozen, err := FreezePath(channelNo, nameStart, timezone, runID)
	if err != nil {
		return err
	}
	target := filepath.Join(pool.Path, frozen.RelativePath)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		return err
	}

	// Reuse the segment when the clip already exists, so the event keeps pointing
	// at one clip that grows rather than at a row per assembly.
	// The identity is what the storage reconciler verifies against the file later:
	// device, inode, size and mtime. Writing an empty object (the first version
	// did) makes every comparison fail, so each clip was marked 'damaged' on the
	// next reconciliation pass even though the file was intact.
	fileID, err := identity(info)
	if err != nil {
		return err
	}
	rawID, err := json.Marshal(fileID)
	if err != nil {
		return err
	}
	segmentID := id.ID("")
	if job.Segment != nil {
		segmentID = *job.Segment
	} else {
		created, err := id.New()
		if err != nil {
			return err
		}
		segmentID = created
	}
	// Two different paths, and using the wrong one is silent: the index must
	// point at the PUBLISHED file. Storing the staging path (what the first
	// version did) leaves an entry pointing into the work directory, which the
	// cleanup below deletes — so every clip reads as "missing" seconds after it
	// was assembled, and the footage looks lost when it is on disk under its
	// proper name.
	published, err := filepath.Rel(pool.Path, target)
	if err != nil {
		return err
	}
	err = s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if job.Run == nil {
			if _, err := tx.Exec(ctx, `INSERT INTO recording_runs(id,site_id,channel_id,source_revision_id,stream_session_id,pool_id,work_relative_path,purpose,state,started_at,ended_at)
				SELECT $1,ch.site_id,$2,$3,ss.id,$4,$5,'event','stopped',$6,$7
				FROM channels ch LEFT JOIN LATERAL (
					SELECT id FROM stream_sessions WHERE channel_id=ch.id AND purpose='main' ORDER BY created_at DESC LIMIT 1
				) ss ON true WHERE ch.id=$2`,
				runID, channelID, revisionID, poolID, work, start.UTC(), end.UTC()); err != nil {
				// A channel with no stream session at all still gets a run; the
				// session column is nullable for exactly this case.
				if _, fallbackErr := tx.Exec(ctx, `INSERT INTO recording_runs(id,site_id,channel_id,source_revision_id,pool_id,work_relative_path,purpose,state,started_at,ended_at)
					SELECT $1,ch.site_id,$2,$3,$4,$5,'event','stopped',$6,$7 FROM channels ch WHERE ch.id=$2`,
					runID, channelID, revisionID, poolID, work, start.UTC(), end.UTC()); fallbackErr != nil {
					return fallbackErr
				}
			}
		} else {
			// The row already exists; only its covered span moves. Its start is the
			// clip's real beginning, which the head kept even after the ring evicted
			// those fragments, so it must not follow the newest assembly's start.
			if _, err := tx.Exec(ctx, `UPDATE recording_runs SET started_at=$2,ended_at=$3 WHERE id=$1`, runID, nameStart.UTC(), end.UTC()); err != nil {
				return err
			}
		}
		if job.Segment == nil {
			evidenceJSON, err := json.Marshal(map[string]int{"buffer_fragments": len(picked)})
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,
				original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes,file_identity,media_evidence,state,time_evidence,ready_at)
				VALUES($1,$2,$3,$4,$5,$6,$6,$7,$8,$9,$7,$10,$11,$12,$13,'ready','completion',clock_timestamp())`,
				segmentID, channelID, revisionID, runID, poolID, published, start.UTC(), timezone, frozen.UTCOffsetSeconds, end.UTC(), info.Size(), rawID, evidenceJSON); err != nil {
				return err
			}
		} else {
			// A re-cut REPLACES the file, so the identity captured at the previous
			// assembly no longer describes it: the size grew and the rename gave it
			// a new inode. Leaving the old identity in place made the storage
			// reconciler mark the clip damaged on its next pass — which is why
			// clips recorded *after* the fix were still unusable. The identity
			// columns are immutable by trigger (they are part of the publication
			// identity), but file_identity is evidence, not identity: it is meant
			// to be refreshed whenever the bytes change.
			//
			// start_at stays at nameStart for the same reason the file name does:
			// it describes the bytes in the file, and the head guarantees the file
			// still begins there. Writing the newest assembly's start would claim
			// the clip begins later than it does.
			// media_evidence carries how many buffer fragments the file holds, so a
			// later assembly knows how much the head already covers without trusting
			// the event row (which the linking pass rewrites independently).
			evidenceJSON, err := json.Marshal(map[string]int{"buffer_fragments": len(picked) + headParts})
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE recording_segments SET start_at=$2,end_at=$3,size_bytes=$4,file_identity=$5,media_evidence=$6,ready_at=clock_timestamp() WHERE id=$1`,
				segmentID, nameStart.UTC(), end.UTC(), info.Size(), rawID, evidenceJSON); err != nil {
				return err
			}
		}
		// The location row is what the storage reconciler walks when it verifies
		// files and totals usage. Without it the clip is invisible to that pass —
		// it neither gets checked nor counted, and a deleted file would keep
		// reading as ready forever.
		if _, err := tx.Exec(ctx, `INSERT INTO recording_locations(segment_id,pool_id,relative_path,size_bytes)
			VALUES($1,$2,$3,$4) ON CONFLICT(segment_id,pool_id) DO UPDATE SET size_bytes=EXCLUDED.size_bytes,state='ready',verified_at=clock_timestamp()`,
			segmentID, poolID, published, info.Size()); err != nil {
			return err
		}
		// The event now has footage of its own, so its verdict becomes linked and
		// the page can play it. The span starts where the file starts, not where the
		// newest fragment does.
		_, err := tx.Exec(ctx, `UPDATE events SET media_state='linked',media_start_at=$2,media_end_at=$3,media_segments=$4,updated_at=clock_timestamp() WHERE id=$1`,
			eventID, nameStart.UTC(), end.UTC(), len(picked)+headParts)
		if err != nil {
			return err
		}
		// An event that is still running keeps its clip open for the next pass:
		// the page can already play what exists, and the window extends as
		// fragments accumulate. A finished event closes the job for good.
		next := "done"
		if job.StillOpen {
			next = "growing"
		}
		_, err = tx.Exec(ctx, `UPDATE event_clip_jobs SET state=$2,segment_id=$3,clip_run_id=$4,updated_at=clock_timestamp() WHERE event_id=$1`,
			eventID, next, segmentID, runID)
		return err
	})
	if err != nil {
		_ = os.Remove(target)
		return err
	}
	// The work directory held the staged file; the published copy is the segment.
	_ = os.RemoveAll(outDir)
	return nil
}

// channelNaming returns the channel number and its site's timezone, which the
// standard file rule needs.
func (s *Service) channelNaming(ctx context.Context, channelID id.ID) (int, string, error) {
	var number int
	var zone string
	err := s.DB.Pool.QueryRow(ctx, `SELECT c.channel_no,s.timezone FROM channels c JOIN sites s ON s.id=c.site_id WHERE c.id=$1`, channelID).Scan(&number, &zone)
	return number, zone, err
}

// existingClipHead returns the published clip of a re-cut as the first input of
// the next assembly.
//
// The returned start is read from the row rather than taken from the caller: the
// naming rule made that instant part of the file's identity, so an assembly that
// chose a different one would name a different file.
//
// A clip whose row exists but whose file does not (removed by the operator, or
// lost with a pool) is NOT a head. Appending to bytes that are gone would publish
// a file that starts mid-event while still claiming the old span; returning false
// makes this pass rebuild from whatever the ring still holds, which is the honest
// answer. The row is left as it is — the storage reconciler owns that verdict.
func (s *Service) existingClipHead(ctx context.Context, segmentID, poolID id.ID, root *os.Root) (probe.ConcatPart, time.Time, bool, error) {
	var start, end time.Time
	var path string
	if err := s.DB.Pool.QueryRow(ctx, `SELECT s.start_at,s.end_at,l.relative_path FROM recording_segments s
		JOIN recording_locations l ON l.segment_id=s.id AND l.pool_id=s.pool_id
		WHERE s.id=$1 AND s.pool_id=$2 AND s.state='ready' AND l.state='ready'`, segmentID, poolID).Scan(&start, &end, &path); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return probe.ConcatPart{}, time.Time{}, false, nil
		}
		return probe.ConcatPart{}, time.Time{}, false, err
	}
	file, err := storage.OpenMediaFile(root, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return probe.ConcatPart{}, time.Time{}, false, nil
		}
		return probe.ConcatPart{}, time.Time{}, false, err
	}
	if _, err := file.Stat(); err != nil {
		file.Close()
		return probe.ConcatPart{}, time.Time{}, false, err
	}
	file.Close()
	// The duration is the segment's recorded span: that is how much material the
	// head file holds, and it is what the ffconcat script needs to timestamp the
	// appended parts correctly. Deriving it from the file would need a probe of
	// its own on every pass.
	duration := end.Sub(start)
	if duration <= 0 {
		return probe.ConcatPart{}, time.Time{}, false, nil
	}
	return probe.ConcatPart{Path: filepath.Join(root.Name(), path), Duration: duration}, start, true, nil
}

// fragmentsAfter keeps the fragments the head does not already cover.
//
// The head ends where the previous assembly stopped; only material from then on
// is new. A fragment straddling that instant is dropped rather than split: a
// partial fragment cannot be appended without re-encoding it, which this path
// never does.
func fragmentsAfter(fragments []BufferFragment, after time.Time) []BufferFragment {
	out := make([]BufferFragment, 0, len(fragments))
	for _, fragment := range fragments {
		if fragment.Start.After(after) || fragment.Start.Equal(after) {
			out = append(out, fragment)
		}
	}
	return out
}

// settleClip closes a job whose clip is already as complete as the ring allows.
//
// This is what keeps a running-but-idle event from rewriting the same bytes every
// five seconds. A running event still goes back to 'growing' so the next message
// can extend it; a finished one is 'done'.
func (s *Service) settleClip(ctx context.Context, eventID id.ID, stillOpen bool) error {
	next := "done"
	if stillOpen {
		next = "growing"
	}
	_, err := s.DB.Pool.Exec(ctx, `UPDATE event_clip_jobs SET state=$2,updated_at=clock_timestamp() WHERE event_id=$1`, eventID, next)
	return err
}

var (
	errClipWindowEmpty = errors.New("event_clip_window_empty")
	errClipNoPool      = errors.New("event_clip_pool_unbound")
	errClipInvalid     = errors.New("event_clip_invalid")
)

func clipErrorCode(err error) string {
	switch {
	case errors.Is(err, errClipWindowEmpty):
		return "window_empty"
	case errors.Is(err, errClipNoPool):
		return "pool_unbound"
	case errors.Is(err, errClipInvalid):
		return "clip_invalid"
	default:
		return "assembly_failed"
	}
}

var _ = fmt.Sprintf
var _ = io.EOF

// EventClipJobRetention is how long a finished clip job is kept.
//
// Longer than the buffer itself and shorter than the event index: the job is
// useful while someone might ask "why does this event have no footage", and the
// answer is stable within a week.
const EventClipJobRetention = 7 * 24 * time.Hour

// RepairEventClipDamage restores event clips quarantined by the re-cut defect.
//
// Before a re-cut kept its head, every assembly of a running event wrote a NEW
// file: the name came from the start instant and the start moved forward as the
// ring evicted the beginning. The index kept pointing at the first (largest)
// file, while size_bytes and file_identity were overwritten with the last one's.
// The reconciler then compared a correct file against evidence about a different
// file and marked it damaged — the footage was intact and played normally.
//
// The fix cannot be a pure SQL migration: the identity lives in the inode, and
// only the file itself can say what it is. This pass re-reads each damaged event
// clip from the path the index already records and rewrites the evidence from
// the file that is actually there. A file that is genuinely absent or truncated
// is left damaged, so this cannot hide real loss.
//
// Idempotent and bounded: one batch per call, and rows it repairs stop being
// candidates.
func (s *Service) RepairEventClipDamage(ctx context.Context) {
	if s.DB == nil || s.Pools == nil {
		return
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT s.id,s.pool_id,l.relative_path
		FROM recording_segments s
		JOIN recording_locations l ON l.segment_id=s.id AND l.pool_id=s.pool_id
		JOIN recording_runs r ON r.id=s.run_id
		WHERE s.state='damaged' AND l.state='damaged' AND r.purpose='event'
		ORDER BY s.start_at DESC LIMIT 50`)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("event clip repair scan unavailable", "error", err)
		}
		return
	}
	type candidate struct {
		Segment id.ID
		Pool    id.ID
		Path    string
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.Segment, &item.Pool, &item.Path); err != nil {
			rows.Close()
			return
		}
		candidates = append(candidates, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return
	}
	repaired := 0
	for _, item := range candidates {
		if ctx.Err() != nil {
			return
		}
		root, _, err := s.Pools.OpenMediaRoot(ctx, item.Pool)
		if err != nil {
			continue
		}
		file, err := storage.OpenMediaFile(root, item.Path)
		if err != nil {
			root.Close()
			// A missing file is a real loss: the reconciler already recorded that
			// verdict and this pass must not overwrite it.
			continue
		}
		info, statErr := file.Stat()
		file.Close()
		root.Close()
		if statErr != nil {
			continue
		}
		// A file SMALLER than the index recorded is evidence of truncation, not of
		// stale evidence: the re-cut defect always produced files at least as large
		// as the one it replaced, so nothing it wrote can be shorter. Keeping these
		// damaged is what stops this pass from laundering real loss.
		var recordedSize int64
		if err := s.DB.Pool.QueryRow(ctx, `SELECT size_bytes FROM recording_segments WHERE id=$1`, item.Segment).Scan(&recordedSize); err != nil {
			continue
		}
		if info.Size() < recordedSize {
			continue
		}
		fileID, err := identity(info)
		if err != nil {
			continue
		}
		rawID, err := json.Marshal(fileID)
		if err != nil {
			continue
		}
		err = s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE recording_segments
				SET size_bytes=$2,file_identity=$3,state='ready',error_code=NULL,ready_at=clock_timestamp()
				WHERE id=$1 AND state='damaged'`, item.Segment, info.Size(), rawID); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE recording_locations SET size_bytes=$3,state='ready',verified_at=clock_timestamp()
				WHERE segment_id=$1 AND pool_id=$2 AND state='damaged'`, item.Segment, item.Pool, info.Size())
			return err
		})
		if err != nil {
			continue
		}
		repaired++
	}
	if repaired > 0 {
		slog.Info("event clips restored", "repaired", repaired)
	}
}

// SweepEventClipOrphans removes the duplicate files the re-cut defect left behind.
//
// Every assembly named its output after the start instant, and the start moved as
// the ring evicted the beginning, so one event could leave seven files. Only the
// file the index records is referenced: the others are the same event at earlier
// extents and nothing will ever play them. Measured on the live site: 58 healed
// clips had left 100+ orphans on disk.
//
// The published file is never a candidate — it is matched by the segment's own
// target_relative_path, not by a naming rule — so a naming change cannot delete
// footage.
func (s *Service) SweepEventClipOrphans(ctx context.Context) {
	if s.DB == nil || s.Pools == nil {
		return
	}
	// One row per (pool, channel, run): the run id is the part of the file name
	// that is unique to one clip, so grouping on it finds exactly the duplicates
	// this defect created.
	rows, err := s.DB.Pool.Query(ctx, `SELECT DISTINCT s.pool_id,s.channel_id,s.run_id,c.channel_no
		FROM recording_segments s JOIN recording_runs r ON r.id=s.run_id JOIN channels c ON c.id=s.channel_id
		WHERE r.purpose='event' LIMIT 200`)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("event clip orphan scan unavailable", "error", err)
		}
		return
	}
	type clipRun struct {
		Pool    id.ID
		Channel id.ID
		Run     id.ID
		Number  int
	}
	var clipRuns []clipRun
	for rows.Next() {
		var item clipRun
		if err := rows.Scan(&item.Pool, &item.Channel, &item.Run, &item.Number); err != nil {
			rows.Close()
			return
		}
		clipRuns = append(clipRuns, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return
	}
	removed := 0
	for _, item := range clipRuns {
		if ctx.Err() != nil {
			return
		}
		var published string
		err := s.DB.Pool.QueryRow(ctx, `SELECT target_relative_path FROM recording_segments WHERE run_id=$1 LIMIT 1`, item.Run).Scan(&published)
		if err != nil {
			continue
		}
		root, pool, err := s.Pools.OpenMediaRoot(ctx, item.Pool)
		if err != nil {
			continue
		}
		root.Close()
		orphans, err := clipOrphans(pool.Path, item.Run, published)
		if err != nil {
			continue
		}
		for _, orphan := range orphans {
			if err := os.Remove(orphan); err != nil && !errors.Is(err, os.ErrNotExist) {
				slog.Warn("event clip orphan removal failed", "path", orphan, "error", err)
				continue
			}
			removed++
		}
	}
	if removed > 0 {
		slog.Info("event clip orphans removed", "removed", removed)
	}
}

// clipOrphans lists files whose name carries the run id but which the index does
// not point at.
func clipOrphans(poolPath string, runID id.ID, published string) ([]string, error) {
	suffix := "_" + strings.ReplaceAll(string(runID), "-", "") + ".mp4"
	publishedPath := filepath.Join(poolPath, published)
	// Only the directory the published clip is in: every assembly of one run
	// names its file after that same start instant, so the duplicates are always
	// siblings. Walking the whole recordings tree per run would make this sweep
	// cost grow with the size of the library.
	dir := filepath.Dir(publishedPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if path == publishedPath {
			continue
		}
		out = append(out, path)
	}
	return out, nil
}

// SweepEventClipJobs removes clip jobs that are settled and old.
//
// Only 'done' and 'failed' rows are removed. A 'queued' or 'growing' row is live
// work, and deleting it would strand an event whose clip is still being cut.
func (s *Service) SweepEventClipJobs(ctx context.Context) {
	if s.DB == nil {
		return
	}
	rows, err := s.DB.Pool.Exec(ctx, `DELETE FROM event_clip_jobs
		WHERE state IN ('done','failed') AND updated_at < clock_timestamp()-make_interval(secs => $1)`,
		EventClipJobRetention.Seconds())
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("event clip job retention unavailable", "error", err)
		}
		return
	}
	if n := rows.RowsAffected(); n > 0 {
		slog.Info("event clip jobs swept", "removed", n)
	}
}
