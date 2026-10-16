package recording

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
)

// Export errors follow the shared failure vocabulary: a client-fixable refusal
// is a 4xx fault, and capacity reuses the storage-domain codes the frontend
// already knows how to recover from (run a pool check, retry once).
var (
	ErrExportWindowTooLong  = fault.New(422, "window_too_long", "单次导出不超过 30 分钟")
	ErrExportWindowInvalid  = fault.New(422, "window_invalid", "导出窗口无效")
	ErrExportNoSource       = fault.New(422, "export_no_source", "该时间范围没有可导出的已发布录像")
	ErrExportTooFragmented  = fault.New(422, "export_too_fragmented", "该范围的分段过多，请缩小时间范围")
	ErrExportPoolRequired   = fault.New(422, "pool_required", "该通道未绑定存储池，请指定导出输出池")
	ErrExportPoolUnknown    = fault.New(422, "pool_unknown", "导出输出池不可用")
	ErrExportLowSpace       = fault.New(409, "low_space", "输出池剩余空间不足")
	ErrExportCapacityKnown  = fault.New(409, "capacity_unknown", "输出池容量未知，请先检查存储池")
	ErrExportNotFound       = auth.ErrNotFound
	exportEstimateHeadroom  = 1.05
	exportArtifactRetention = 7 * 24 * time.Hour
)

// ExportAnalysis is the API projection of one analyzed window.
type ExportAnalysis struct {
	ChannelID       id.ID                    `json:"channel_id"`
	Start           time.Time                `json:"start"`
	End             time.Time                `json:"end"`
	CoverageMS      int64                    `json:"coverage_ms"`
	Groups          []ExportGroupView        `json:"groups"`
	Gaps            []ExportSpanView         `json:"gaps"`
	Unavailable     []ExportUnavailableView  `json:"unavailable"`
	DuplicateCount  int                      `json:"duplicate_count"`
	ReadyBytes      int64                    `json:"ready_bytes"`
	MediaConsistent bool                     `json:"media_consistent"`
}
type ExportGroupView struct {
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	DurationMS int64     `json:"duration_ms"`
	Segments   int       `json:"segments"`
	Bytes      int64     `json:"bytes"`
}
type ExportSpanView struct {
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	DurationMS int64     `json:"duration_ms"`
}
type ExportUnavailableView struct {
	SegmentID   id.ID     `json:"segment_id"`
	State       string    `json:"state"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
}

// Export is the task view. Artifacts ride along so one GET serves the drawer.
type Export struct {
	ID              id.ID            `json:"id"`
	ChannelID       id.ID            `json:"channel_id"`
	ChannelNo       int              `json:"channel_no"`
	PoolID          id.ID            `json:"pool_id"`
	RequestedStart  time.Time        `json:"requested_start"`
	RequestedEnd    time.Time        `json:"requested_end"`
	GapMode         string           `json:"gap_mode"`
	State           string           `json:"state"`
	Progress        int              `json:"progress"`
	Message         string           `json:"message"`
	ErrorCode       *string          `json:"error_code"`
	ActualStart     *time.Time       `json:"actual_start"`
	ActualEnd       *time.Time       `json:"actual_end"`
	RequestedByName string           `json:"requested_by_name"`
	CreatedAt       time.Time        `json:"created_at"`
	Artifacts       []ExportArtifact `json:"artifacts"`
}
type ExportArtifact struct {
	ID           id.ID     `json:"id"`
	FileName     string    `json:"file_name"`
	SizeBytes    int64     `json:"size_bytes"`
	SHA256       string    `json:"sha256"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	SegmentCount int       `json:"segment_count"`
	State        string    `json:"state"`
	Protected    bool      `json:"protected"`
	ExpiresAt    time.Time `json:"expires_at"`
}

const exportColumns = `e.id,e.channel_id,e.channel_no,e.pool_id,e.requested_start,e.requested_end,e.gap_mode,e.state,e.progress,e.message,e.error_code,e.actual_start,e.actual_end,e.requested_by_name,e.created_at`

func scanExport(row pgx.Row) (Export, error) {
	var out Export
	err := row.Scan(&out.ID, &out.ChannelID, &out.ChannelNo, &out.PoolID, &out.RequestedStart, &out.RequestedEnd, &out.GapMode, &out.State, &out.Progress, &out.Message, &out.ErrorCode, &out.ActualStart, &out.ActualEnd, &out.RequestedByName, &out.CreatedAt)
	return out, err
}

// AnalyzeExport runs the read-only range preview: what the window covers, where
// its gaps are, and which indexed segments in it cannot serve. It deliberately
// does not touch pool files — only the worker verifies media on disk.
func (s *Service) AnalyzeExport(ctx context.Context, p auth.Principal, channelID id.ID, start, end time.Time) (ExportAnalysis, error) {
	var out ExportAnalysis
	if err := validateExportWindow(start, end); err != nil {
		return out, err
	}
	if s.Auth == nil {
		return out, auth.ErrNotFound
	}
	var segments []sourceSegment
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Export, tx); err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return auth.ErrNotFound
			}
			return err
		}
		var err error
		segments, err = loadWindowSegments(ctx, tx, channelID, start, end)
		return err
	})
	if err != nil {
		return out, err
	}
	return exportAnalysisView(channelID, start, end, analyzeExportWindow(start, end, segments)), nil
}

func exportAnalysisView(channelID id.ID, start, end time.Time, plan exportPlan) ExportAnalysis {
	out := ExportAnalysis{
		ChannelID:       channelID,
		Start:           start,
		End:             end,
		CoverageMS:      plan.coverage().Milliseconds(),
		Groups:          make([]ExportGroupView, 0, len(plan.Groups)),
		Gaps:            make([]ExportSpanView, 0, len(plan.Gaps)),
		Unavailable:     make([]ExportUnavailableView, 0, len(plan.Unavailable)),
		DuplicateCount:  plan.DuplicateCount,
		ReadyBytes:      plan.ReadyBytes,
		MediaConsistent: plan.MediaConsistent,
	}
	for _, group := range plan.Groups {
		out.Groups = append(out.Groups, ExportGroupView{Start: group.Start, End: group.End, DurationMS: group.duration().Milliseconds(), Segments: len(group.Segments), Bytes: group.Bytes})
	}
	for _, gap := range plan.Gaps {
		out.Gaps = append(out.Gaps, ExportSpanView{Start: gap.Start, End: gap.End, DurationMS: gap.DurationMS()})
	}
	for _, segment := range plan.Unavailable {
		out.Unavailable = append(out.Unavailable, ExportUnavailableView{SegmentID: segment.ID, State: segment.State, Start: segment.Start, End: segment.End})
	}
	return out
}

func validateExportWindow(start, end time.Time) error {
	if start.IsZero() || end.IsZero() || !end.After(start) || end.Sub(start) < time.Second {
		return ErrExportWindowInvalid
	}
	if end.Sub(start) > ExportMaxWindow {
		return ErrExportWindowTooLong
	}
	return nil
}

// loadWindowSegments reads every segment intersecting the window in concat
// order, without touching pool files.
func loadWindowSegments(ctx context.Context, tx pgx.Tx, channelID id.ID, start, end time.Time) ([]sourceSegment, error) {
	rows, err := tx.Query(ctx, `SELECT `+publicationColumns+` FROM recording_segments s WHERE s.channel_id=$1 AND s.start_at<$3 AND s.end_at>$2 ORDER BY s.start_at,s.id`, channelID, start.UTC(), end.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sourceSegment
	for rows.Next() {
		item, err := scanPublication(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sourceSegment{ID: item.ID, State: item.State, Start: item.Start, End: item.End, Bytes: item.Bytes, Media: item.Media})
	}
	return out, rows.Err()
}

// ExportCreateInput carries one create request. The idempotency key is the
// transport-retry guard: the same key never yields a second task, a replay of
// the same POST simply resolves to the first one.
type ExportCreateInput struct {
	ChannelID      id.ID
	Start, End     time.Time
	GapMode        string
	PoolID         id.ID
	IdempotencyKey string
}

// CreateExport validates the request, pins the ready source segments and
// enqueues the job in one transaction, so a crash between pinning and queueing
// leaves nothing behind. The pinned list is the export's temporary evidence
// reference: deletion paths consult it while the task is queued or running.
func (s *Service) CreateExport(ctx context.Context, p auth.Principal, in ExportCreateInput) (Export, id.ID, error) {
	var out Export
	var jobID id.ID
	if err := validateExportWindow(in.Start, in.End); err != nil {
		return out, "", err
	}
	if in.GapMode != "merge" && in.GapMode != "split" {
		return out, "", auth.ErrInvalid
	}
	if len(in.IdempotencyKey) < 8 || len(in.IdempotencyKey) > 128 {
		return out, "", auth.ErrInvalid
	}
	if s.Auth == nil {
		return out, "", auth.ErrNotFound
	}
	var exportID id.ID
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, in.ChannelID, auth.Export, tx); err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return auth.ErrNotFound
			}
			return err
		}
		var channelNo int
		var boundPool *id.ID
		if err := tx.QueryRow(ctx, `SELECT channel_no,storage_pool_id FROM channels WHERE id=$1`, in.ChannelID).Scan(&channelNo, &boundPool); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return err
		}
		poolID := in.PoolID
		if poolID == "" {
			if boundPool == nil {
				return ErrExportPoolRequired
			}
			poolID = *boundPool
		}
		var enabled bool
		if err := tx.QueryRow(ctx, `SELECT enabled FROM storage_pools WHERE id=$1`, poolID).Scan(&enabled); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrExportPoolUnknown
			}
			return err
		}
		if !enabled {
			return ErrExportPoolUnknown
		}
		segments, err := loadWindowSegments(ctx, tx, in.ChannelID, in.Start, in.End)
		if err != nil {
			return err
		}
		plan := analyzeExportWindow(in.Start, in.End, segments)
		if len(plan.Groups) == 0 {
			return ErrExportNoSource
		}
		if in.GapMode == "split" && len(plan.Groups) > ExportMaxArtifacts {
			return ErrExportTooFragmented
		}
		if err := s.checkExportCapacity(ctx, tx, poolID, plan.ReadyBytes); err != nil {
			return err
		}
		var username string
		if err := tx.QueryRow(ctx, `SELECT username FROM users WHERE id=$1`, p.UserID).Scan(&username); err != nil {
			return err
		}
		exportID, err = id.New()
		if err != nil {
			return err
		}
		payload, err := json.Marshal(struct {
			ExportID id.ID `json:"export_id"`
		}{exportID})
		if err != nil {
			return err
		}
		// The job row goes in first so the export row can carry its real id:
		// one INSERT, no placeholder to patch up later.
		jobID, err = jobs.Enqueue(ctx, tx, jobs.Input{Kind: "recording.export", ObjectID: exportID, IdempotencyKey: in.IdempotencyKey, Payload: payload})
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO recording_exports(id,channel_id,channel_no,pool_id,requested_start,requested_end,gap_mode,state,requested_by,requested_by_name,idempotency_key,job_id) VALUES($1,$2,$3,$4,$5,$6,$7,'queued',$8,$9,$10,$11)`,
			exportID, in.ChannelID, channelNo, poolID, in.Start.UTC(), in.End.UTC(), in.GapMode, p.UserID, username, in.IdempotencyKey, jobID); err != nil {
			return err
		}
		for seq, segment := range exportChain(plan) {
			if _, err = tx.Exec(ctx, `INSERT INTO recording_export_sources(export_id,seq,segment_id) VALUES($1,$2,$3)`, exportID, seq+1, segment.ID); err != nil {
				return err
			}
		}
		details, err := json.Marshal(map[string]any{"channel_id": in.ChannelID, "start": in.Start.UTC(), "end": in.End.UTC(), "gap_mode": in.GapMode, "pool_id": poolID})
		if err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "recording.export_requested", ObjectID: exportID, Details: details})
	})
	if err != nil {
		if errors.Is(err, jobs.ErrIdempotencyConflict) {
			return out, "", fault.New(409, "idempotency_conflict", "请求已被处理，请勿重复提交")
		}
		return out, "", err
	}
	out, err = s.loadExport(ctx, exportID)
	return out, jobID, err
}

// exportChain flattens the plan into the pinned concat order: groups in start
// order, each group's segments in start order.
func exportChain(plan exportPlan) []sourceSegment {
	var chain []sourceSegment
	for _, group := range plan.Groups {
		chain = append(chain, group.Segments...)
	}
	return chain
}

// checkExportCapacity compares the fresh pool capacity with the estimated
// artifact size. It is a second gate too — the worker re-checks before writing.
func (s *Service) checkExportCapacity(ctx context.Context, tx pgx.Tx, poolID id.ID, sourceBytes int64) error {
	var free *int64
	if err := tx.QueryRow(ctx, `SELECT min(free_bytes) FROM storage_pool_checks WHERE pool_id=$1 AND state='healthy' AND expires_at>clock_timestamp()`, poolID).Scan(&free); err != nil {
		return err
	}
	if free == nil {
		return ErrExportCapacityKnown
	}
	estimate := int64(float64(sourceBytes)*exportEstimateHeadroom) + 4096
	if *free < estimate {
		return ErrExportLowSpace
	}
	return nil
}

// getExportRow reloads one export (with artifacts) for the response body.
func (s *Service) getExportRow(ctx context.Context, out Export, p auth.Principal, _ error) (Export, error) {
	export, err := s.GetExport(ctx, p, out.ID)
	if err != nil {
		return Export{}, err
	}
	return export, nil
}

// ListExports pages one channel's export history, newest first. The drawer is
// the only consumer, so a bounded page without a cursor is enough.
func (s *Service) ListExports(ctx context.Context, p auth.Principal, channelID id.ID, limit int) ([]Export, error) {
	if limit < 1 || limit > 50 {
		limit = 20
	}
	if s.Auth == nil {
		return nil, auth.ErrNotFound
	}
	var ids []id.ID
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Export, tx); err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return auth.ErrNotFound
			}
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+exportColumns+` FROM recording_exports e WHERE e.channel_id=$1 ORDER BY e.created_at DESC,e.id DESC LIMIT $2`, channelID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanExport(rows)
			if err != nil {
				return err
			}
			ids = append(ids, item.ID)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	exports := make([]Export, 0, len(ids))
	for _, exportID := range ids {
		export, err := s.loadExport(ctx, exportID)
		if err != nil {
			return nil, err
		}
		exports = append(exports, export)
	}
	return exports, nil
}

// GetExport returns one task with its artifacts. Visibility follows the
// channel's export grant, not the creator: whoever may export a channel may
// inspect and download its evidence.
func (s *Service) GetExport(ctx context.Context, p auth.Principal, exportID id.ID) (Export, error) {
	var channelID id.ID
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT channel_id FROM recording_exports WHERE id=$1`, exportID).Scan(&channelID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return err
		}
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Export, tx); err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return auth.ErrNotFound
			}
			return err
		}
		return nil
	})
	if err != nil {
		return Export{}, err
	}
	return s.loadExport(ctx, exportID)
}

func (s *Service) loadExport(ctx context.Context, exportID id.ID) (Export, error) {
	var out Export
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		item, err := scanExport(tx.QueryRow(ctx, `SELECT `+exportColumns+` FROM recording_exports e WHERE e.id=$1`, exportID))
		if err != nil {
			return err
		}
		out = item
		rows, err := tx.Query(ctx, `SELECT id,file_name,size_bytes,sha256,start_at,end_at,segment_count,state,protected,expires_at FROM recording_export_artifacts WHERE export_id=$1 ORDER BY start_at,id`, exportID)
		if err != nil {
			return err
		}
		defer rows.Close()
		out.Artifacts = []ExportArtifact{}
		for rows.Next() {
			var artifact ExportArtifact
			if err := rows.Scan(&artifact.ID, &artifact.FileName, &artifact.SizeBytes, &artifact.SHA256, &artifact.Start, &artifact.End, &artifact.SegmentCount, &artifact.State, &artifact.Protected, &artifact.ExpiresAt); err != nil {
				return err
			}
			out.Artifacts = append(out.Artifacts, artifact)
		}
		return rows.Err()
	})
	return out, err
}

// ProtectWindow implements EXP-04: one request marks every ready segment
// intersecting the window. It shares the per-segment protection semantics with
// SetProtection — retention and deletion skip what it marks — and the same
// audit discipline: the state change and its record travel together.
func (s *Service) ProtectWindow(ctx context.Context, p auth.Principal, channelID id.ID, start, end time.Time, protected bool) (int, error) {
	if s.Auth == nil {
		return 0, auth.ErrNotFound
	}
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return 0, err
	}
	if start.IsZero() || end.IsZero() || !end.After(start) || end.Sub(start) > 31*24*time.Hour {
		return 0, auth.ErrInvalid
	}
	var updated int
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE recording_segments SET protected=$4,protected_at=CASE WHEN $4 THEN clock_timestamp() ELSE NULL END WHERE channel_id=$1 AND state='ready' AND start_at<$3 AND end_at>$2 AND protected<>$4`, channelID, start.UTC(), end.UTC(), protected)
		if err != nil {
			return err
		}
		updated = int(tag.RowsAffected())
		action := "recording.window_unprotected"
		if protected {
			action = "recording.window_protected"
		}
		details, err := json.Marshal(map[string]any{"channel_id": channelID, "start": start.UTC(), "end": end.UTC(), "updated": updated})
		if err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: action, ObjectID: channelID, Details: details})
	})
	return updated, err
}

// SetExportArtifactProtection is EXP-05's permanent keep: an artifact the
// operator protects outlives the default seven-day expiry. Administrator-only,
// mirroring segment protection.
func (s *Service) SetExportArtifactProtection(ctx context.Context, p auth.Principal, exportID, artifactID id.ID, protected bool) (ExportArtifact, error) {
	var out ExportArtifact
	if s.Auth == nil {
		return out, auth.ErrNotFound
	}
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return out, err
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var channelID id.ID
		if err := tx.QueryRow(ctx, `SELECT e.channel_id FROM recording_export_artifacts a JOIN recording_exports e ON e.id=a.export_id WHERE a.id=$1 AND a.export_id=$2`, artifactID, exportID).Scan(&channelID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE recording_export_artifacts SET protected=$3 WHERE id=$1 AND export_id=$2 AND state='ready'`, artifactID, exportID, protected); err != nil {
			return err
		}
		action := "recording.export_artifact_unprotected"
		if protected {
			action = "recording.export_artifact_protected"
		}
		details, err := json.Marshal(map[string]any{"export_id": exportID, "protected": protected})
		if err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: action, ObjectID: artifactID, Details: details})
	})
	if err != nil {
		return out, err
	}
	export, err := s.loadExport(ctx, exportID)
	if err != nil {
		return out, err
	}
	for _, artifact := range export.Artifacts {
		if artifact.ID == artifactID {
			return artifact, nil
		}
	}
	return out, auth.ErrNotFound
}
