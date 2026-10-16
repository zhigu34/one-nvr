package recording

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/storage"
	"golang.org/x/sys/unix"
)

// Export task failure codes. Client-fixable refusals were rejected at creation;
// what fails here is environmental or material, and each code becomes the
// permanent-failure marker the jobs table stores alongside the export row.
const (
	exportErrSourceGone    = "source_unavailable"
	exportErrIncompatible  = "media_incompatible"
	exportErrLowSpace      = "low_space"
	exportErrOutputInvalid = "output_invalid"
	exportErrJobExhausted  = "job_exhausted"
)

// exportProgressInterval throttles the progress write-back: ffmpeg reports
// several times a second, the row only needs to move visibly.
const exportProgressInterval = 2 * time.Second

var exportFailureText = map[string]string{
	exportErrSourceGone:    "源录像在导出期间不可用",
	exportErrIncompatible:  "所选片段编码参数不一致，无法原码拼接",
	exportErrLowSpace:      "输出池剩余空间不足",
	exportErrOutputInvalid: "导出产物校验未通过",
	exportErrJobExhausted:  "导出任务多次失败，已停止重试",
}

// durationWithinTolerance says how far the remuxed artifact may drift from the
// nominal chain it was built from: end_at values are nominal and encoder
// timestamps are not frame-exact, so five seconds or ten percent — whichever is
// larger — is the honest bound.
func durationWithinTolerance(actual, expected time.Duration) bool {
	drift := actual - expected
	if drift < 0 {
		drift = -drift
	}
	limit := expected / 10
	if limit < 5*time.Second {
		limit = 5 * time.Second
	}
	return drift <= limit
}

// HandleExport executes one export job. A crash mid-run re-runs from scratch on
// the next attempt: the work directory is rebuilt, artifacts are published
// atomically and registered in the final transaction, so no partial state is
// ever visible or persisted.
func (s *Service) HandleExport(ctx context.Context, lease jobs.Lease) (jobs.Result, error) {
	var payload struct {
		ExportID id.ID `json:"export_id"`
	}
	if err := json.Unmarshal(lease.Payload, &payload); err != nil || payload.ExportID == "" {
		return jobs.Result{}, &jobs.PermanentFailure{Code: "invalid_payload"}
	}
	export, err := s.loadExport(ctx, payload.ExportID)
	if err != nil {
		return jobs.Result{}, &jobs.PermanentFailure{Code: "export_missing"}
	}
	if export.State != "queued" && export.State != "running" {
		// Already terminal: a retry that lands after the final transaction must
		// not produce a second artifact.
		return jobs.Result{Payload: lease.Payload}, nil
	}
	if err := s.touchExport(ctx, export.ID, "running", 0, "准备导出"); err != nil {
		return jobs.Result{}, err
	}
	sources, err := s.loadPinnedSources(ctx, export.ID)
	if err != nil {
		return jobs.Result{}, err
	}
	if len(sources) == 0 {
		return jobs.Result{}, s.failExport(ctx, export.ID, exportErrSourceGone)
	}
	// Verify every pinned source before any concat: a segment that lost its
	// file or identity while pinned ends the task, it does not silently produce
	// a shorter artifact than the manifest promised.
	paths := make(map[id.ID]string)
	for _, source := range sources {
		if source.State != "ready" {
			return jobs.Result{}, s.failExport(ctx, export.ID, exportErrSourceGone)
		}
		if _, cached := paths[source.PoolID]; !cached {
			_, pool, err := s.Pools.OpenMediaRoot(ctx, source.PoolID)
			if err != nil {
				// Pool offline is environmental: the job retries with backoff
				// and the reconciliation marks the export if it never recovers.
				return jobs.Result{}, err
			}
			paths[source.PoolID] = pool.Path
		}
		if err := verifySourceFile(ctx, s, source); err != nil {
			return jobs.Result{}, s.failExport(ctx, export.ID, exportErrSourceGone)
		}
	}
	segments := make([]sourceSegment, 0, len(sources))
	for _, source := range sources {
		segments = append(segments, sourceSegment{ID: source.ID, State: source.State, Start: source.Start, End: source.End, Bytes: source.Bytes, Media: source.Media})
	}
	if !mediaConsistent(segments) {
		return jobs.Result{}, s.failExport(ctx, export.ID, exportErrIncompatible)
	}
	if err := s.recheckExportCapacity(ctx, export.PoolID, exportEstimate(exportBytes(segments))); err != nil {
		return jobs.Result{}, s.failExport(ctx, export.ID, exportErrLowSpace)
	}
	groups := groupChain(segments)
	if export.GapMode == "merge" {
		groups = [][]sourceSegment{segments}
	}
	return s.runExportGroups(ctx, export, sources, paths, groups)
}

func exportEstimate(sourceBytes int64) int64 {
	return int64(float64(sourceBytes)*exportEstimateHeadroom) + 4096
}

// loadPinnedSources reads the concat-ordered source chain pinned at creation.
func (s *Service) loadPinnedSources(ctx context.Context, exportID id.ID) ([]publication, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT `+publicationColumns+` FROM recording_export_sources es JOIN recording_segments s ON s.id=es.segment_id WHERE es.export_id=$1 ORDER BY es.seq`, exportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []publication
	for rows.Next() {
		item, err := scanPublication(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// verifySourceFile re-checks the published identity through the pool root, so
// concat never reads a replaced or truncated file.
func verifySourceFile(ctx context.Context, s *Service, source publication) error {
	root, _, err := s.Pools.OpenMediaRoot(ctx, source.PoolID)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := storage.OpenMediaFile(root, source.Target)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !source.Identity.matches(info) {
		return ErrPublicationConflict
	}
	return nil
}

func exportBytes(segments []sourceSegment) int64 {
	var total int64
	for _, segment := range segments {
		total += segment.Bytes
	}
	return total
}

// groupChain splits the pinned chain into contiguous groups: a hole longer than
// SeamTolerance starts a new one. Split mode writes one artifact per group.
func groupChain(segments []sourceSegment) [][]sourceSegment {
	var groups [][]sourceSegment
	var current []sourceSegment
	for _, segment := range segments {
		if len(current) > 0 && segment.Start.After(current[len(current)-1].End.Add(SeamTolerance)) {
			groups = append(groups, current)
			current = nil
		}
		current = append(current, segment)
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}

type exportProgressWriter struct {
	service   *Service
	ctx       context.Context
	exportID  id.ID
	total     time.Duration
	completed time.Duration
	last      time.Time
}

// at reports overall progress across all artifacts of the task, throttled so a
// fast remux does not hammer the row.
func (w *exportProgressWriter) at(groupPortion float64, message string) {
	now := time.Now()
	if now.Sub(w.last) < exportProgressInterval {
		return
	}
	w.last = now
	overall := 1
	if w.total > 0 {
		ratio := (w.completed + time.Duration(groupPortion*float64(w.total))) / w.total
		overall = int(ratio * 98)
		if overall < 1 {
			overall = 1
		}
		if overall > 98 {
			overall = 98
		}
	}
	_ = w.service.touchExport(w.ctx, w.exportID, "running", overall, message)
}

// runExportGroups concatenates each group, verifies and publishes each artifact
// atomically, then registers everything in one final transaction.
func (s *Service) runExportGroups(ctx context.Context, export Export, pubs []publication, paths map[id.ID]string, groups [][]sourceSegment) (jobs.Result, error) {
	outRoot, outPool, err := s.Pools.OpenMediaRoot(ctx, export.PoolID)
	if err != nil {
		return jobs.Result{}, err
	}
	defer outRoot.Close()
	workRel := filepath.Join(".work", "exports", string(export.ID))
	_ = outRoot.RemoveAll(workRel)
	workDir, err := openParent(outRoot, filepath.Join(workRel, "list.txt"), true)
	if err != nil {
		return jobs.Result{}, err
	}
	workDir.Close()

	byID := make(map[id.ID]publication, len(pubs))
	for _, pub := range pubs {
		byID[pub.ID] = pub
	}
	total := time.Duration(0)
	for _, group := range groups {
		for _, segment := range group {
			total += segment.End.Sub(segment.Start)
		}
	}
	progress := &exportProgressWriter{service: s, ctx: ctx, exportID: export.ID, total: total, last: time.Now().Add(-exportProgressInterval)}
	var published []string
	var artifacts []ExportArtifact
	actualStart, actualEnd := time.Time{}, time.Time{}
	// A failure after publishing leaves files no row claims: the deferred
	// cleanup removes the work directory and any unregistered artifact.
	defer func() {
		_ = outRoot.RemoveAll(workRel)
		for _, target := range published {
			_ = outRoot.Remove(target)
		}
	}()
	completed := time.Duration(0)
	for index, group := range groups {
		seq := index + 1
		parts := make([]probe.ConcatPart, 0, len(group))
		groupTotal := time.Duration(0)
		for _, segment := range group {
			source := byID[segment.ID]
			groupTotal += segment.End.Sub(segment.Start)
			parts = append(parts, probe.ConcatPart{
				Path:     filepath.Join(paths[source.PoolID], source.Target),
				Duration: segment.End.Sub(segment.Start),
			})
		}
		workOut := filepath.Join(workRel, fmt.Sprintf("out-%03d.mp4", seq))
		targetRel := artifactRelativePath(export.ID, seq)
		evidence, err := s.Probe.ConcatMP4(ctx, parts, filepath.Join(outPool.Path, workOut), func(portion float64) {
			progress.at(portion*float64(groupTotal)/float64(total), fmt.Sprintf("拼接第 %d/%d 段", seq, len(groups)))
		})
		if err != nil {
			return jobs.Result{}, s.failExport(ctx, export.ID, exportErrOutputInvalid)
		}
		if !evidence.Readable || evidence.Duration <= 0 || !durationWithinTolerance(evidence.Duration, groupTotal) {
			return jobs.Result{}, s.failExport(ctx, export.ID, exportErrOutputInvalid)
		}
		digest, err := hashFile(filepath.Join(outPool.Path, workOut))
		if err != nil {
			return jobs.Result{}, err
		}
		if err := publishExportArtifact(outRoot, workOut, targetRel); err != nil {
			return jobs.Result{}, err
		}
		published = append(published, targetRel)
		artifactID, err := id.New()
		if err != nil {
			return jobs.Result{}, err
		}
		spanStart, spanEnd := group[0].Start, group[len(group)-1].End
		if actualStart.IsZero() || spanStart.Before(actualStart) {
			actualStart = spanStart
		}
		if spanEnd.After(actualEnd) {
			actualEnd = spanEnd
		}
		artifacts = append(artifacts, ExportArtifact{
			ID:           artifactID,
			FileName:     fmt.Sprintf("CH%02d_%s_%03d.mp4", export.ChannelNo, export.RequestedStart.UTC().Format("20060102_150405"), seq),
			SizeBytes:    evidence.Size,
			SHA256:       digest,
			Start:        spanStart,
			End:          spanEnd,
			SegmentCount: len(group),
		})
		completed += groupTotal
		progress.completed = completed
		progress.at(0, fmt.Sprintf("已完成 %d/%d 段", seq, len(groups)))
	}
	if err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		for i, artifact := range artifacts {
			if _, err := tx.Exec(ctx, `INSERT INTO recording_export_artifacts(id,export_id,pool_id,relative_path,file_name,size_bytes,sha256,start_at,end_at,segment_count,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
				artifact.ID, export.ID, export.PoolID, artifactRelativePath(export.ID, i+1), artifact.FileName, artifact.SizeBytes, artifact.SHA256, artifact.Start.UTC(), artifact.End.UTC(), artifact.SegmentCount, time.Now().UTC().Add(exportArtifactRetention)); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE recording_exports SET state='succeeded',progress=100,message='',actual_start=$2,actual_end=$3 WHERE id=$1 AND state IN ('queued','running')`, export.ID, actualStart.UTC(), actualEnd.UTC())
		return err
	}); err != nil {
		return jobs.Result{}, err
	}
	published = nil
	return jobs.Result{Payload: []byte(`{"state":"succeeded"}`)}, nil
}

// artifactRelativePath is the pool-internal location of one artifact: the
// exports/ namespace the storage marker reserved for exactly this purpose.
func artifactRelativePath(exportID id.ID, seq int) string {
	return filepath.Join("exports", string(exportID), fmt.Sprintf("%03d.mp4", seq))
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// publishExportArtifact moves the verified work file into the pool's exports/
// namespace with a no-replace rename, then syncs both directories. The work and
// target trees share one pool, so the rename never crosses a filesystem.
func publishExportArtifact(root *os.Root, workRel, targetRel string) error {
	from, err := openParent(root, workRel, false)
	if err != nil {
		return ErrPublicationUnavailable
	}
	defer from.Close()
	workFile, err := openRegularAt(from, filepath.Base(workRel))
	if err != nil {
		return ErrPublicationConflict
	}
	defer workFile.Close()
	info, err := workFile.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return ErrPublicationConflict
	}
	workFile.Close()
	to, err := openParent(root, targetRel, true)
	if err != nil {
		return ErrPublicationUnavailable
	}
	defer to.Close()
	if err := renameNoReplace(int(from.Fd()), filepath.Base(workRel), int(to.Fd()), filepath.Base(targetRel)); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return ErrPublicationConflict
		}
		return ErrPublicationUnavailable
	}
	return to.Sync()
}

// touchExport advances the task row's progress. It only moves queued/running
// rows, so a late progress write can never resurrect a terminal export.
func (s *Service) touchExport(ctx context.Context, exportID id.ID, state string, progress int, message string) error {
	_, err := s.DB.Pool.Exec(ctx, `UPDATE recording_exports SET state=$2,progress=$3,message=$4 WHERE id=$1 AND state IN ('queued','running')`, exportID, state, progress, message)
	return err
}

// failExport marks the task failed and terminalizes the job in one move: the
// row carries the operator-facing record, the PermanentFailure stops retries.
func (s *Service) failExport(ctx context.Context, exportID id.ID, code string) error {
	if err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE recording_exports SET state='failed',progress=100,error_code=$2,message=$3 WHERE id=$1 AND state IN ('queued','running')`, exportID, code, exportFailureText[code])
		return err
	}); err != nil {
		return err
	}
	return &jobs.PermanentFailure{Code: code}
}

// recheckExportCapacity is the worker-side gate against a pool that filled up
// between creation and execution.
func (s *Service) recheckExportCapacity(ctx context.Context, poolID id.ID, estimate int64) error {
	var free *int64
	if err := s.DB.Pool.QueryRow(ctx, `SELECT min(free_bytes) FROM storage_pool_checks WHERE pool_id=$1 AND state='healthy' AND expires_at>clock_timestamp()`, poolID).Scan(&free); err != nil {
		return err
	}
	if free == nil || *free < estimate {
		return ErrExportLowSpace
	}
	return nil
}

// ReconcileExports terminalizes tasks whose job exhausted its attempts while
// the row still says queued/running. The jobs table terminalizes on claim, but
// it cannot know about the export row, so this runs beside the workers.
func (s *Service) ReconcileExports(ctx context.Context) {
	if s.DB == nil {
		return
	}
	_, err := s.DB.Pool.Exec(ctx, `UPDATE recording_exports SET state='failed',progress=100,error_code=$2,message=$3 WHERE state IN ('queued','running') AND EXISTS(SELECT 1 FROM jobs j WHERE j.id=recording_exports.job_id AND j.state='failed')`, "", exportErrJobExhausted, exportFailureText[exportErrJobExhausted])
	if err != nil && ctx.Err() == nil {
		slog.Warn("export reconciliation unavailable")
	}
}
