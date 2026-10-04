package recording

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/storage"
)

var nativeFileName = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9]{2}-[0-9]{2}-[0-9]{2}-[0-9]+\.mp4$`)

func nativeStart(receipt runReceipt, relative string) (time.Time, bool) {
	prefix := receipt.WorkRelativePath + "/record/" + receipt.Key.App + "/" + receipt.Key.Stream + "/"
	if receipt.NativeTimezone != "UTC" || !strings.HasPrefix(relative, prefix) {
		return time.Time{}, false
	}
	parts := strings.Split(strings.TrimPrefix(relative, prefix), "/")
	if len(parts) != 2 || !nativeFileName.MatchString(parts[1]) {
		return time.Time{}, false
	}
	start, err := time.ParseInLocation("2006-01-02-15-04-05", parts[1][:19], time.UTC)
	if err != nil || start.Format("2006-01-02") != parts[0] || start.Before(receipt.CreatedAt.Add(-30*time.Second)) || start.After(time.Now().Add(30*time.Second)) {
		return time.Time{}, false
	}
	return start, true
}
func (s *Service) discover(ctx context.Context) error {
	s.discoveryMu.Lock()
	defer s.discoveryMu.Unlock()
	var after any
	if s.discoveryAfter != "" {
		after = s.discoveryAfter
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT id,created_at FROM recording_runs WHERE site_id=$1 AND purpose='continuous' AND ($2::uuid IS NULL OR (created_at,id)<($3,$2::uuid)) ORDER BY created_at DESC,id DESC LIMIT 64`, s.siteID, after, s.discoveryTime)
	if err != nil {
		return err
	}
	type scanRun struct {
		ID      id.ID
		Created time.Time
	}
	var runs []scanRun
	for rows.Next() {
		var run scanRun
		if err := rows.Scan(&run.ID, &run.Created); err != nil {
			rows.Close()
			return err
		}
		runs = append(runs, run)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var first error
	for _, run := range runs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.discoverRun(ctx, run.ID); err != nil && first == nil {
			first = err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.discoveryAfter = run.ID
		s.discoveryTime = run.Created
	}
	if len(runs) < 64 {
		s.discoveryAfter = ""
		s.discoveryTime = time.Time{}
	}
	return first
}
func (s *Service) discoverRun(ctx context.Context, runID id.ID) error {
	want, err := s.receipt(ctx, runID)
	if err != nil {
		return err
	}
	root, pool, err := s.Pools.OpenMediaRoot(ctx, want.PoolID)
	if err != nil {
		return err
	}
	defer root.Close()
	receipt, err := readReceipt(root, receiptPath(runID))
	if err != nil {
		return nil
	} // No receipt means no automatic takeover.
	if receipt != want || receipt.SiteID != pool.SiteID {
		return ErrPublicationConflict
	}
	var paths []string
	if s.discoveryFiles == nil {
		s.discoveryFiles = make(map[id.ID]string)
	}
	after := s.discoveryFiles[runID]
	last := ""
	complete := true
	visited := 0
	err = fs.WalkDir(root.FS(), receipt.WorkRelativePath, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return fs.SkipDir
			}
			return walkErr
		}
		if path <= after {
			if entry.IsDir() && path < after && !strings.HasPrefix(after, path+"/") {
				return fs.SkipDir
			}
			return nil
		}
		visited++
		if visited > 2048 {
			complete = false
			return fs.SkipAll
		}
		last = path
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".mp4" && !strings.HasPrefix(entry.Name(), ".") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		var known bool
		if err := s.DB.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recording_segments WHERE run_id=$1 AND original_relative_path=$2) OR EXISTS(SELECT 1 FROM hook_inbox WHERE run_id=$1 AND payload->>'FilePath'=$3)`, runID, path, filepath.Join(pool.Path, path)).Scan(&known); err != nil {
			return err
		}
		if known {
			continue
		}
		file, err := storage.OpenMediaFile(root, path)
		if err != nil {
			continue
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			continue
		}
		media, probeErr := s.Probe.InspectMP4(ctx, file)
		after, statErr := file.Stat()
		file.Close()
		if statErr != nil || !sameClosedFile(info, after) {
			continue
		}
		start, reliable := nativeStart(receipt, path)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if probeErr != nil && !errors.Is(probeErr, probe.ErrFileInvalid) {
			return ErrPublicationUnavailable
		}
		if probeErr != nil || !media.Readable || media.Duration <= 0 || media.Video.Width <= 0 || media.Video.Height <= 0 || media.Video.Codec == "" {
			if err := s.rememberUnverified(ctx, receipt, path, info, media, "damaged"); err != nil {
				return err
			}
			continue
		}
		if !reliable {
			if err := s.rememberUnverified(ctx, receipt, path, info, media, "provisional"); err != nil {
				return err
			}
			continue
		}
		completion := Completion{Key: receipt.Key, MediaServerID: "one-nvr-" + string(receipt.SiteID), FilePath: filepath.Join(pool.Path, path), Size: info.Size(), StartTime: start.UTC(), Duration: media.Duration, TimeEvidence: "recovered"}
		if err := s.Accept(ctx, completion); err != nil {
			return err
		}
	}
	if complete {
		delete(s.discoveryFiles, runID)
	} else if last != "" {
		s.discoveryFiles[runID] = last
	}
	return nil
}
func (s *Service) rememberUnverified(ctx context.Context, receipt runReceipt, path string, info os.FileInfo, media probe.FileEvidence, state string) error {
	recordingID, err := StableID(receipt.SiteID, receipt.PoolID, receipt.RunID, path)
	if err != nil {
		return err
	}
	var channelNo int
	var timezone string
	if err := s.DB.Pool.QueryRow(ctx, "SELECT c.channel_no,s.timezone FROM channels c JOIN sites s ON s.id=c.site_id WHERE c.id=$1 AND c.site_id=$2", receipt.ChannelID, receipt.SiteID).Scan(&channelNo, &timezone); err != nil {
		return err
	}
	// An estimated time is explicitly provisional and never publishes ready media.
	start := info.ModTime().UTC()
	duration := media.Duration
	if duration <= 0 {
		duration = time.Second
	}
	frozen, err := FreezePath(channelNo, start, timezone, recordingID)
	if err != nil {
		return err
	}
	fileID, err := identity(info)
	if err != nil {
		return err
	}
	rawID, _ := json.Marshal(fileID)
	rawMedia, _ := json.Marshal(media)
	_, err = s.DB.Pool.Exec(ctx, `INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes,file_identity,media_evidence,state,time_evidence,error_code) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$8,$11,$12,$13,$14,$15,'provisional','media_requires_review') ON CONFLICT(run_id,original_relative_path) DO NOTHING`, recordingID, receipt.ChannelID, receipt.SourceRevisionID, receipt.RunID, receipt.PoolID, path, frozen.RelativePath, start, timezone, frozen.UTCOffsetSeconds, start.Add(duration), info.Size(), rawID, rawMedia, state)
	return err
}
