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
	"sort"
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

// clipSettleDelay is how long after an event's end the clip keeps growing.
//
// The detector ends an event when it stops recognising the subject, which
// routinely happens while the person is still in frame — measured on this site,
// clips ended with the subject mid-stride. Six seconds covers a walk-out at
// normal pace plus the fragment boundary, and it costs nothing: the clip is
// already playable while it waits.
const clipSettleDelay = 6 * time.Second

// PreEventLead and PostEventTrail are the operator-chosen buffer window.
const (
	PreEventLead = 5 * time.Second
	// PostEventTrail is longer than the lead: the detector ends an event when it
	// loses the subject, typically while the person is still walking out of
	// frame, so the tail has to cover a departure rather than a detection.
	PostEventTrail = 10 * time.Second
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
	// An event is still "open" for settle purposes until ClipSettleDelay after it
	// ended. The detector closes an event when it loses the subject, which is
	// often while the person is still walking out of frame — finalising at that
	// instant cut the clip exactly where they were about to leave. Waiting lets
	// the tail fragments land so the clip covers the departure.
	//
	// The delay is a settle delay, not a re-open: ended_at is never rewritten, and
	// the link window the page shows still ends at the event's own end plus the
	// trail.
	rows, err := s.DB.Pool.Query(ctx, `SELECT j.event_id,j.channel_id,j.window_start,j.window_end,
			j.segment_id,j.clip_run_id,
			e.ended_at IS NULL OR e.ended_at > clock_timestamp() - $1::interval AS still_open
		FROM event_clip_jobs j LEFT JOIN events e ON e.id=j.event_id
		WHERE j.state IN ('queued','growing')
		ORDER BY (e.ended_at IS NULL OR e.ended_at > clock_timestamp() - $1::interval) DESC, j.updated_at LIMIT 20`,
		clipSettleDelay.String())
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
	// Every assembly is built from ORIGINAL fragments held in the event's own
	// parts directory — never from the previously published clip.
	//
	// Feeding the published file back in as the head was the second half of the
	// same mistake. It kept the start from moving, but it made the clip a
	// concatenation of a concatenation: each pass spliced AAC at a boundary the
	// previous pass had already spliced, and every generation left a bit more
	// invalid audio. Measured against real ZLM fragments: one splice produced no
	// AAC errors, splicing that result again produced 51. The live site's 20-pass
	// clips carried thousands, and Chrome fails the whole clip on an undecodable
	// audio track even though every video frame is intact.
	//
	// The ring cannot be the source either — it keeps thirty seconds, so a long
	// event's beginning is evicted before the next pass. Copying each fragment out
	// as it is used gives both properties at once: the parts directory always
	// holds the whole event from its first fragment, and every input stays an
	// original recording.
	partsDir := filepath.Join(pool.Path, work, "parts")
	if err := os.MkdirAll(partsDir, 0o700); err != nil {
		return err
	}
	nameStart := start
	if job.Segment != nil {
		var recorded time.Time
		if err := s.DB.Pool.QueryRow(ctx, `SELECT start_at FROM recording_segments WHERE id=$1`, *job.Segment).Scan(&recorded); err != nil {
			return err
		}
		nameStart = recorded
	}
	held, err := s.holdFragments(ctx, partsDir, fragments, from, to)
	if err != nil {
		return err
	}
	if len(held) == 0 {
		if job.Segment != nil {
			// Nothing new and nothing held: the clip is as complete as this pass can
			// make it. Settling avoids rewriting identical bytes every five seconds.
			return s.settleClip(ctx, eventID, job.StillOpen)
		}
		return errClipWindowEmpty
	}
	picked = held
	start = nameStart
	end = held[len(held)-1].End
	// The clip is built from every part held for this event, so the count in its
	// evidence is simply how many parts that is — there is no separate head now.
	partCount := len(held)

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
	// A stream copy is enough now that every input is an original fragment.
	//
	// Measured against real ZLM fragments: splicing eight of them produced no AAC
	// errors at all; splicing that RESULT again produced 51. The corruption was
	// never in the fragments or in a single splice — it came from feeding a
	// concatenation back into the concatenator. With the parts directory holding
	// originals, one splice is all this path ever does, so re-encoding the audio
	// would only cost fidelity for nothing.
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
			// The row already exists; only its covered span moves. Its start stays at
			// the clip's real beginning — the parts directory holds the first
			// fragment even after the ring evicted it, so the span must not follow
			// the newest assembly.
			if _, err := tx.Exec(ctx, `UPDATE recording_runs SET started_at=$2,ended_at=$3 WHERE id=$1`, runID, nameStart.UTC(), end.UTC()); err != nil {
				return err
			}
		}
		// Whether this clip is finished media is decided by the EVENT, not by the
		// fact that a playable file exists.
		//
		// A running event's clip is re-cut every few seconds, so publishing it as
		// 'ready' claimed far more than it should: 'ready' is what the archive
		// scheduler uploads, what an export concatenates, what the watermark and
		// quota paths are allowed to delete, and what pool usage counts. An event
		// that lasted a minute would upload half a minute to the cloud and report a
		// growing segment as finished footage. PRD PLAY-05 puts it plainly: only a
		// formally completed, verified MP4 is playable — and REC-09 forbids treating
		// an unfinished MP4 as a published one.
		//
		// 'finalizing' is exactly that state in this schema ("written, not yet
		// published") and the library page already renders it as 写入中. The clip
		// becomes 'ready' when the event ends, which is the moment the file stops
		// changing.
		segmentState := "finalizing"
		if !job.StillOpen {
			segmentState = "ready"
		}
		if job.Segment == nil {
			evidenceJSON, err := json.Marshal(map[string]int{"buffer_fragments": len(picked)})
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,
				original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes,file_identity,media_evidence,state,time_evidence,ready_at)
				VALUES($1,$2,$3,$4,$5,$6,$6,$7,$8,$9,$7,$10,$11,$12,$13,$14,'completion',CASE WHEN $14='ready' THEN clock_timestamp() END)`,
				segmentID, channelID, revisionID, runID, poolID, published, start.UTC(), timezone, frozen.UTCOffsetSeconds, end.UTC(), info.Size(), rawID, evidenceJSON, segmentState); err != nil {
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
			// it describes the bytes in the file, and the held parts guarantee the
			// file still begins there. Writing the newest assembly's start would
			// claim the clip begins later than it does.
			// media_evidence records how many parts the file was built from, which
			// is what the page's "N 段" reports.
			evidenceJSON, err := json.Marshal(map[string]int{"buffer_fragments": partCount})
			if err != nil {
				return err
			}
			// ready_at marks the moment the clip STOPPED changing, so it is only set
			// when this pass publishes it. A running event's clip has not finished
			// being written and must not carry a publication timestamp.
			if _, err := tx.Exec(ctx, `UPDATE recording_segments SET start_at=$2,end_at=$3,size_bytes=$4,file_identity=$5,media_evidence=$6,
				state=$7,error_code=NULL,ready_at=CASE WHEN $7='ready' THEN clock_timestamp() ELSE ready_at END WHERE id=$1`,
				segmentID, nameStart.UTC(), end.UTC(), info.Size(), rawID, evidenceJSON, segmentState); err != nil {
				return err
			}
		}
		// The location row is what the storage reconciler walks when it verifies
		// files and totals usage. Without it the clip is invisible to that pass —
		// it neither gets checked nor counted, and a deleted file would keep
		// reading as ready forever.
		// The location row carries the same verdict as the segment. Writing 'ready'
		// here while the segment is 'finalizing' would let the reconciler verify a
		// file that is still being rewritten, and mark it damaged on the next pass
		// for having changed — the same false verdict the identity refresh fixed.
		locationState := segmentState
		if _, err := tx.Exec(ctx, `INSERT INTO recording_locations(segment_id,pool_id,relative_path,size_bytes,state)
			VALUES($1,$2,$3,$4,$5) ON CONFLICT(segment_id,pool_id) DO UPDATE SET size_bytes=EXCLUDED.size_bytes,state=EXCLUDED.state,verified_at=clock_timestamp()`,
			segmentID, poolID, published, info.Size(), locationState); err != nil {
			return err
		}
		// The event now has footage of its own, so its verdict becomes linked. The
		// span starts where the file starts, not where the newest fragment does.
		// While the event runs the page shows 录像生成中 rather than a play button:
		// media_state says footage EXISTS, while the segment's own state says whether
		// that footage is finished. Conflating the two is what let a half-minute clip
		// be indexed as a finished recording.
		_, err := tx.Exec(ctx, `UPDATE events SET media_state='linked',media_start_at=$2,media_end_at=$3,media_segments=$4,updated_at=clock_timestamp() WHERE id=$1`,
			eventID, nameStart.UTC(), end.UTC(), partCount)
		if err != nil {
			return err
		}
		// An event that is still running keeps its clip open for the next pass: the
		// window extends as fragments accumulate, and the segment stays
		// 'finalizing'. A finished event closes the job for good and publishes.
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
	//
	// The parts directory survives while the event runs: it is the only copy of
	// the fragments the ring has already evicted, so deleting it here would make
	// the next pass rebuild from a ring that no longer holds the beginning. A
	// finished event has nothing more to assemble, so its parts go with the work
	// directory.
	if !job.StillOpen {
		_ = os.RemoveAll(outDir)
	} else {
		_ = os.Remove(staged)
	}
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

// holdFragments copies the window's fragments into the event's parts directory
// and returns every part held there, in play order.
//
// Copying rather than referencing is what makes an event longer than the ring's
// thirty seconds possible at all: the ring evicts from the old end, so a
// reference would dangle by the next pass. The copy is also what keeps every
// input an ORIGINAL recording — see the comment at the call site for why
// concatenating a published clip again corrupts its audio.
//
// It is idempotent: a fragment already held is not copied twice, so a pass that
// finds nothing new returns the same set and the clip is not rewritten.
func (s *Service) holdFragments(ctx context.Context, dir string, fragments []BufferFragment, from, to time.Time) ([]BufferFragment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	window, _, _ := BufferWindow(fragments, from, to)
	for _, fragment := range window {
		// The name already encodes the fragment's start instant, which is also its
		// identity inside this event; the extension keeps mp4tooling happy.
		name := filepath.Base(fragment.Path)
		target := filepath.Join(dir, name)
		if _, err := os.Stat(target); err == nil {
			continue
		}
		if err := copyFile(fragment.Path, target); err != nil {
			// A fragment evicted between the scan and the copy is expected: the ring
			// is a rolling buffer and another pass will hold what is still there.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
	}
	held, err := ScanBufferFragments(dir)
	if err != nil {
		return nil, err
	}
	out := make([]BufferFragment, 0, len(held))
	for _, fragment := range held {
		out = append(out, fragment)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// copyFile duplicates one file, creating the target's directory tree.
func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	sourceInfo, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(target)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(target)
		return err
	}
	// The mtime IS the fragment's end instant: ZLM names a fragment after the
	// moment it opened the file and the parser reads the close time from mtime.
	// A copy that left "now" behind would claim every held fragment lasted until
	// the copy ran, and the assembled clip's span — and its index entry — would
	// be wrong.
	if err := os.Chtimes(target, sourceInfo.ModTime(), sourceInfo.ModTime()); err != nil {
		_ = os.Remove(target)
		return err
	}
	return nil
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
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE event_clip_jobs SET state=$2,updated_at=clock_timestamp() WHERE event_id=$1`, eventID, next); err != nil {
			return err
		}
		if stillOpen {
			return nil
		}
		// The event ended without producing new fragments, so this is the pass that
		// publishes its clip. Without this the segment would stay 'finalizing'
		// forever — unplayable, unarchived, unexportable — precisely because nothing
		// else happened to trigger a re-cut.
		return s.publishClipTx(ctx, tx, eventID)
	})
	return err
}

// publishClipTx marks a clip's segment and location ready.
//
// It is the single place the transition happens, so "finished" cannot come to
// mean two different things in two paths.
func (s *Service) publishClipTx(ctx context.Context, tx pgx.Tx, eventID id.ID) error {
	if _, err := tx.Exec(ctx, `UPDATE recording_segments s SET state='ready',error_code=NULL,ready_at=clock_timestamp()
		FROM event_clip_jobs j WHERE j.event_id=$1 AND s.id=j.segment_id AND s.state='finalizing'`, eventID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE recording_locations l SET state='ready',verified_at=clock_timestamp()
		FROM event_clip_jobs j WHERE j.event_id=$1 AND l.segment_id=j.segment_id AND l.state='finalizing'`, eventID)
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
	if s.DB == nil || s.Pools == nil || s.Probe == nil {
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
		if statErr != nil {
			file.Close()
			root.Close()
			continue
		}
		// Whether the bytes are whole is decided by reading them, not by comparing
		// sizes. The defect wrote the same event several times and the files can
		// differ by a lot — measured on the site: one clip was 13.6 MB on disk
		// against 32 MB in the index, because the index described the last assembly
		// and the file the first. A size rule would either refuse intact clips or
		// launder a truncated one; an MP4 ffprobe cannot parse is the evidence that
		// a file really is broken, and it is the same check the publication path
		// applies before a segment is allowed to be ready.
		media, probeErr := s.Probe.InspectMP4(ctx, file)
		file.Close()
		root.Close()
		if probeErr != nil || !media.Readable || media.Duration <= 0 {
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

// PublishSettledClips publishes clips whose event has ended.
//
// The normal path publishes a clip on the assembly pass that runs after the
// event's end message. This covers the case that path cannot reach: the end
// message arrived when the job was already 'done' (the event sat idle before it
// ended), so nothing re-queued the assembly and the segment stayed 'finalizing'
// forever — unplayable, unarchived, unexportable.
//
// It is a safety net, not the primary path: the WHERE clause only matches rows
// whose event has an end time, so a running event's clip is never published
// early.
func (s *Service) PublishSettledClips(ctx context.Context) {
	if s.DB == nil {
		return
	}
	tag, err := s.DB.Pool.Exec(ctx, `UPDATE recording_segments s SET state='ready',error_code=NULL,ready_at=clock_timestamp()
		FROM event_clip_jobs j JOIN events e ON e.id=j.event_id
		WHERE j.segment_id=s.id AND s.state='finalizing' AND e.ended_at IS NOT NULL`)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("settled clip publication unavailable", "error", err)
		}
		return
	}
	if tag.RowsAffected() == 0 {
		return
	}
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_locations l SET state='ready',verified_at=clock_timestamp()
		FROM event_clip_jobs j JOIN events e ON e.id=j.event_id
		WHERE j.segment_id=l.segment_id AND l.state='finalizing' AND e.ended_at IS NOT NULL`); err != nil && ctx.Err() == nil {
		slog.Warn("settled clip location publication unavailable", "error", err)
		return
	}
	slog.Info("settled event clips published", "published", tag.RowsAffected())
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
