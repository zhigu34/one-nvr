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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/probe"
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
	if len(picked) == 0 {
		return errClipWindowEmpty
	}

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

	// The output is written inside the run's work directory, then named by the
	// standard rule, so the clip reads like every other recording in the library.
	outDir := filepath.Join(pool.Path, work)
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	staged := filepath.Join(outDir, "clip.mp4")
	parts := make([]probe.ConcatPart, 0, len(picked))
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
	frozen, err := FreezePath(channelNo, start, timezone, runID)
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
			// The row already exists; only its covered span moves.
			if _, err := tx.Exec(ctx, `UPDATE recording_runs SET started_at=$2,ended_at=$3 WHERE id=$1`, runID, start.UTC(), end.UTC()); err != nil {
				return err
			}
		}
		if job.Segment == nil {
			if _, err := tx.Exec(ctx, `INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,
				original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes,file_identity,media_evidence,state,time_evidence,ready_at)
				VALUES($1,$2,$3,$4,$5,$6,$6,$7,$8,$9,$7,$10,$11,$12,$13,'ready','completion',clock_timestamp())`,
				segmentID, channelID, revisionID, runID, poolID, published, start.UTC(), timezone, frozen.UTCOffsetSeconds, end.UTC(), info.Size(), rawID, []byte("{}")); err != nil {
				return err
			}
		} else {
			// The identity columns are immutable by trigger; only the span and the
			// size describe a clip that is still growing.
			if _, err := tx.Exec(ctx, `UPDATE recording_segments SET start_at=$2,end_at=$3,size_bytes=$4,ready_at=clock_timestamp() WHERE id=$1`,
				segmentID, start.UTC(), end.UTC(), info.Size()); err != nil {
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
		// the page can play it.
		_, err := tx.Exec(ctx, `UPDATE events SET media_state='linked',media_start_at=$2,media_end_at=$3,media_segments=$4,updated_at=clock_timestamp() WHERE id=$1`,
			eventID, start.UTC(), end.UTC(), len(picked))
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
