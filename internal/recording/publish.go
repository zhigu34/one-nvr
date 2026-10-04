package recording

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/storage"
)

type FilePublisher interface {
	Move(context.Context, *os.Root, string, string, os.FileInfo) error
}
type NativePublisher struct{}

func (NativePublisher) Move(ctx context.Context, root *os.Root, original, target string, expected os.FileInfo) error {
	return atomicPublish(ctx, root, original, target, expected)
}

type fileIdentity struct {
	Device, Inode  uint64
	Size, Modified int64
}

func identity(info os.FileInfo) (fileIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() {
		return fileIdentity{}, ErrPublicationConflict
	}
	return fileIdentity{Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Size: info.Size(), Modified: info.ModTime().UnixNano()}, nil
}
func (f fileIdentity) matches(info os.FileInfo) bool {
	actual, err := identity(info)
	return err == nil && actual == f
}

type publication struct {
	Segment
	Original, Target string
	Identity         fileIdentity
	Evidence         probe.FileEvidence
}

const publicationColumns = `s.id,s.channel_id,s.source_revision_id,s.pool_id,s.run_id,s.start_at,s.end_at,s.size_bytes,s.state,s.naming_timezone,s.utc_offset_seconds,s.original_relative_path,s.target_relative_path,s.file_identity,s.media_evidence`

func scanPublication(row pgx.Row) (publication, error) {
	var p publication
	var rawIdentity, rawMedia []byte
	err := row.Scan(&p.ID, &p.ChannelID, &p.SourceRevisionID, &p.PoolID, &p.RunID, &p.Start, &p.End, &p.Bytes, &p.State, &p.NamingTimezone, &p.UTCOffsetSeconds, &p.Original, &p.Target, &rawIdentity, &rawMedia)
	if err != nil {
		return p, err
	}
	if json.Unmarshal(rawIdentity, &p.Identity) != nil || json.Unmarshal(rawMedia, &p.Evidence) != nil {
		return p, ErrPublicationConflict
	}
	p.CheckState = p.State
	return p, nil
}

type inboxLease struct{ ID, Token id.ID }

func (s *Service) claimInbox(ctx context.Context, inboxID id.ID) (inboxLease, error) {
	token, err := id.New()
	if err != nil {
		return inboxLease{}, err
	}
	var found id.ID
	err = s.DB.Pool.QueryRow(ctx, `UPDATE hook_inbox SET state='processing',fencing_token=$2,lease_expires_at=clock_timestamp()+interval '30 seconds',attempts=attempts+1 WHERE id=$1 AND ((state='pending' AND available_at<=clock_timestamp()) OR (state='processing' AND lease_expires_at<clock_timestamp())) AND run_id IN (SELECT id FROM recording_runs WHERE purpose='continuous') RETURNING id`, inboxID, token).Scan(&found)
	return inboxLease{ID: inboxID, Token: token}, err
}
func (s *Service) renewInbox(ctx context.Context, lease inboxLease) error {
	tag, err := s.DB.Pool.Exec(ctx, `UPDATE hook_inbox SET lease_expires_at=clock_timestamp()+interval '30 seconds' WHERE id=$1 AND state='processing' AND fencing_token=$2 AND lease_expires_at>clock_timestamp()`, lease.ID, lease.Token)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrPublicationUnavailable
	}
	return nil
}
func checkInboxFence(ctx context.Context, tx pgx.Tx, lease inboxLease) error {
	var found id.ID
	err := tx.QueryRow(ctx, `SELECT id FROM hook_inbox WHERE id=$1 AND state='processing' AND fencing_token=$2 AND lease_expires_at>clock_timestamp() FOR UPDATE`, lease.ID, lease.Token).Scan(&found)
	if err != nil {
		return ErrPublicationUnavailable
	}
	return nil
}
func (s *Service) Publish(ctx context.Context, inboxID id.ID) (segment Segment, failure error) {
	if s.Probe == nil || s.Pools == nil {
		return Segment{}, ErrPublicationUnavailable
	}
	var raw []byte
	var runID *id.ID
	var inboxState string
	if err := s.DB.Pool.QueryRow(ctx, "SELECT payload,run_id,state FROM hook_inbox WHERE id=$1", inboxID).Scan(&raw, &runID, &inboxState); err != nil {
		return Segment{}, err
	}
	var c Completion
	if json.Unmarshal(raw, &c) != nil || s.validate(c) != nil || runID == nil {
		return Segment{}, ErrCompletionInvalid
	}
	var run Run
	var channelNo int
	var timezone string
	if err := s.DB.Pool.QueryRow(ctx, `SELECT r.id,r.site_id,r.channel_id,r.source_revision_id,r.stream_session_id,r.pool_id,r.work_relative_path,r.purpose,r.state,r.created_at,c.channel_no,site.timezone FROM recording_runs r JOIN channels c ON c.id=r.channel_id JOIN sites site ON site.id=r.site_id WHERE r.id=$1`, *runID).Scan(&run.ID, &run.SiteID, &run.ChannelID, &run.SourceRevisionID, &run.SessionID, &run.PoolID, &run.WorkRelativePath, &run.Purpose, &run.State, &run.CreatedAt, &channelNo, &timezone); err != nil {
		return Segment{}, err
	}
	if run.SiteID != s.siteID || run.Purpose != "continuous" {
		return Segment{}, ErrCompletionInvalid
	}
	root, pool, err := s.Pools.OpenMediaRoot(ctx, run.PoolID)
	if err != nil {
		return Segment{}, err
	}
	defer root.Close()
	original, err := filepath.Rel(pool.Path, c.FilePath)
	if err != nil || !strings.HasPrefix(original, run.WorkRelativePath+"/") {
		return Segment{}, ErrCompletionInvalid
	}
	recordingID, err := StableID(run.SiteID, run.PoolID, run.ID, original)
	if err != nil {
		return Segment{}, err
	}
	existing, findErr := scanPublication(s.DB.Pool.QueryRow(ctx, "SELECT "+publicationColumns+" FROM recording_segments s WHERE s.id=$1", recordingID))
	if findErr == nil && existing.State == "ready" && inboxState == "processed" {
		return existing.Segment, nil
	}
	if findErr != nil && !errors.Is(findErr, pgx.ErrNoRows) {
		return Segment{}, findErr
	}
	lease, err := s.claimInbox(ctx, inboxID)
	if err != nil {
		return Segment{}, ErrPublicationUnavailable
	}
	work, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-work.Done():
				return
			case <-ticker.C:
				bounded, finish := context.WithTimeout(work, 5*time.Second)
				err := s.renewInbox(bounded, lease)
				finish()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	succeeded := false
	defer func() {
		close(done)
		cancel()
		<-renewed
		if !succeeded {
			release, finish := context.WithTimeout(context.Background(), 3*time.Second)
			defer finish()
			if errors.Is(failure, ErrPublicationConflict) {
				s.DB.WithinTx(release, func(tx pgx.Tx) error {
					if err := checkInboxFence(release, tx, lease); err != nil {
						return err
					}
					_, err := tx.Exec(release, `UPDATE recording_segments SET state='conflict',error_code='publication_conflict' WHERE id=$1 AND state IN ('finalizing','conflict')`, recordingID)
					return err
				})
			}
			s.DB.Pool.Exec(release, `UPDATE hook_inbox SET state='pending',fencing_token=NULL,lease_expires_at=NULL,available_at=clock_timestamp()+interval '1 second',error_code='publication_retry_required' WHERE id=$1 AND state='processing' AND fencing_token=$2`, lease.ID, lease.Token)
		}
	}()
	intent := existing
	var file *os.File
	if errors.Is(findErr, pgx.ErrNoRows) {
		file, err = storage.OpenMediaFile(root, original)
		if err != nil {
			return Segment{}, err
		}
		defer file.Close()
		before, err := file.Stat()
		if err != nil || before.Size() != c.Size {
			return Segment{}, ErrPublicationConflict
		}
		media, err := s.Probe.InspectMP4(work, file)
		if work.Err() != nil {
			return Segment{}, work.Err()
		}
		after, statErr := file.Stat()
		if statErr != nil || !sameClosedFile(before, after) {
			return Segment{}, ErrPublicationConflict
		}
		if err != nil && !errors.Is(err, probe.ErrFileInvalid) {
			return Segment{}, ErrPublicationUnavailable
		}
		if err != nil || !media.Readable || media.Size != c.Size || media.Duration <= 0 || media.Video.Width <= 0 || media.Video.Height <= 0 || media.Video.Codec == "" {
			if err := s.quarantineCompletion(work, lease, run, c, original, after, media, channelNo, timezone, recordingID); err != nil {
				return Segment{}, err
			}
			return Segment{}, ErrPublicationConflict
		}
		after, err = file.Stat()
		if err != nil || !sameClosedFile(before, after) {
			return Segment{}, ErrPublicationConflict
		}
		frozen, err := FreezePath(channelNo, c.StartTime, timezone, recordingID)
		if err != nil {
			return Segment{}, err
		}
		fileID, err := identity(after)
		if err != nil {
			return Segment{}, err
		}
		intent = publication{Segment: Segment{ID: recordingID, ChannelID: run.ChannelID, SourceRevisionID: run.SourceRevisionID, PoolID: run.PoolID, RunID: run.ID, Start: c.StartTime, End: c.StartTime.Add(media.Duration), Bytes: media.Size, State: "finalizing", NamingTimezone: frozen.Timezone, UTCOffsetSeconds: frozen.UTCOffsetSeconds, CheckState: "finalizing"}, Original: original, Target: frozen.RelativePath, Identity: fileID, Evidence: media}
		rawID, _ := json.Marshal(fileID)
		rawMedia, _ := json.Marshal(media)
		err = s.DB.WithinTx(work, func(tx pgx.Tx) error {
			if err := checkInboxFence(work, tx, lease); err != nil {
				return err
			}
			_, err := tx.Exec(work, `INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes,file_identity,media_evidence,time_evidence) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$8,$11,$12,$13,$14,$15) ON CONFLICT(run_id,original_relative_path) DO NOTHING`, intent.ID, intent.ChannelID, intent.SourceRevisionID, intent.RunID, intent.PoolID, intent.Original, intent.Target, intent.Start, intent.NamingTimezone, intent.UTCOffsetSeconds, intent.End, intent.Bytes, rawID, rawMedia, completionEvidence(c.TimeEvidence))
			return err
		})
		if err != nil {
			return Segment{}, err
		}
		// This is real ZLM completion/file evidence, not a refreshed synthetic check.
		e := zlm.WriteEvidence{PoolID: run.PoolID, RecordingID: run.ID, FilePath: c.FilePath, Size: media.Size, Duration: media.Duration, VideoVerified: true, ObservedAt: time.Now().UTC()}
		if err := s.Pools.PublishZLMEvidence(work, run.PoolID, run.ID, e); err != nil {
			return Segment{}, err
		}
	}
	if file == nil {
		file, err = storage.OpenMediaFile(root, intent.Original)
		if err == nil {
			defer file.Close()
		}
	}
	if file != nil {
		info, err := file.Stat()
		if err != nil || !intent.Identity.matches(info) {
			return Segment{}, ErrPublicationConflict
		}
		if err := work.Err(); err != nil {
			return Segment{}, err
		}
		mover := s.Files
		if mover == nil {
			mover = NativePublisher{}
		}
		if err := mover.Move(work, root, intent.Original, intent.Target, info); err != nil {
			return Segment{}, err
		}
	} else {
		if _, err := root.Lstat(intent.Original); !os.IsNotExist(err) {
			return Segment{}, ErrPublicationUnavailable
		}
		// A crashed worker may have committed the rename but not the ready row.
		target, err := storage.OpenMediaFile(root, intent.Target)
		if err != nil {
			return Segment{}, ErrPublicationUnavailable
		}
		info, err := target.Stat()
		target.Close()
		if err != nil || !intent.Identity.matches(info) {
			return Segment{}, ErrPublicationConflict
		}
		for _, path := range []string{intent.Original, intent.Target} {
			dir, err := openParent(root, path, false)
			if err != nil {
				return Segment{}, ErrPublicationUnavailable
			}
			err = dir.Sync()
			dir.Close()
			if err != nil {
				return Segment{}, ErrPublicationUnavailable
			}
		}
	}
	if err := work.Err(); err != nil {
		return Segment{}, err
	}
	target, err := storage.OpenMediaFile(root, intent.Target)
	if err != nil {
		return Segment{}, err
	}
	info, err := target.Stat()
	target.Close()
	if err != nil || !intent.Identity.matches(info) {
		return Segment{}, ErrPublicationConflict
	}
	err = s.DB.WithinTx(work, func(tx pgx.Tx) error {
		if err := checkInboxFence(work, tx, lease); err != nil {
			return err
		}
		tag, err := tx.Exec(work, `UPDATE recording_segments SET state='ready',ready_at=clock_timestamp(),error_code=NULL WHERE id=$1 AND state IN ('finalizing','conflict')`, intent.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrPublicationConflict
		}
		if _, err := tx.Exec(work, `INSERT INTO recording_locations(segment_id,pool_id,relative_path,size_bytes) VALUES($1,$2,$3,$4) ON CONFLICT(segment_id,pool_id) DO NOTHING`, intent.ID, intent.PoolID, intent.Target, intent.Bytes); err != nil {
			return err
		}
		if _, err := tx.Exec(work, `UPDATE recording_runs SET last_completion_at=$2 WHERE id=$1 AND (last_completion_at IS NULL OR last_completion_at<$2)`, run.ID, c.StartTime); err != nil {
			return err
		}
		_, err = tx.Exec(work, `UPDATE hook_inbox SET state='processed',processed_at=clock_timestamp(),lease_expires_at=NULL,fencing_token=NULL,error_code=NULL WHERE id=$1 AND fencing_token=$2`, lease.ID, lease.Token)
		return err
	})
	if err != nil {
		return Segment{}, err
	}
	succeeded = true
	intent.State = "ready"
	intent.CheckState = "ready"
	return intent.Segment, nil
}

func completionEvidence(value string) string {
	if value == "" {
		return "completion"
	}
	return value
}

// Quarantine a structurally invalid, unchanged closed file without moving or
// deleting it. The fenced diagnostic retains provenance for operator review.
func (s *Service) quarantineCompletion(ctx context.Context, lease inboxLease, run Run, c Completion, original string, info os.FileInfo, media probe.FileEvidence, channelNo int, timezone string, recordingID id.ID) error {
	frozen, err := FreezePath(channelNo, c.StartTime, timezone, recordingID)
	if err != nil {
		return err
	}
	fileID, err := identity(info)
	if err != nil {
		return err
	}
	rawID, _ := json.Marshal(fileID)
	rawMedia, _ := json.Marshal(media)
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := checkInboxFence(ctx, tx, lease); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes,file_identity,media_evidence,time_evidence,state,error_code) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$8,$11,$12,$13,$14,$15,'damaged','media_requires_review') ON CONFLICT(run_id,original_relative_path) DO NOTHING`, recordingID, run.ChannelID, run.SourceRevisionID, run.ID, run.PoolID, original, frozen.RelativePath, c.StartTime, timezone, frozen.UTCOffsetSeconds, c.StartTime.Add(c.Duration), info.Size(), rawID, rawMedia, completionEvidence(c.TimeEvidence)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE hook_inbox SET state='diagnostic',error_code='media_requires_review',lease_expires_at=NULL,fencing_token=NULL WHERE id=$1 AND fencing_token=$2`, lease.ID, lease.Token)
		return err
	})
}
