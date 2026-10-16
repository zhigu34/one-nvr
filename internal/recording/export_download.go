package recording

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
)

// ExportDownload identifies one download event for the audit trail.
type ExportDownload struct {
	ExportID   id.ID
	ArtifactID id.ID
}

var ErrExportNotReady = fault.New(409, "export_not_ready", "导出未完成，暂无可下载的产物")

// ArtifactFile pairs one artifact row with its open pool file, ready to be
// streamed into the evidence ZIP.
type ArtifactFile struct {
	Artifact ExportArtifact
	File     *os.File
}

// OpenExportBundle performs the permission check once, verifies the export is
// downloadable, opens every artifact file and derives the manifest document.
// The caller owns the returned files.
func (s *Service) OpenExportBundle(ctx context.Context, p auth.Principal, exportID id.ID) (Export, []ArtifactFile, []byte, error) {
	export, err := s.GetExport(ctx, p, exportID)
	if err != nil {
		return Export{}, nil, nil, err
	}
	if export.State != "succeeded" || len(export.Artifacts) == 0 {
		return Export{}, nil, nil, ErrExportNotReady
	}
	manifest, err := s.buildExportManifest(ctx, export)
	if err != nil {
		return Export{}, nil, nil, err
	}
	root, _, err := s.Pools.OpenMediaRoot(ctx, export.PoolID)
	if err != nil {
		return Export{}, nil, nil, err
	}
	defer root.Close()
	files := make([]ArtifactFile, 0, len(export.Artifacts))
	for seq, artifact := range export.Artifacts {
		if artifact.State != "ready" {
			for _, open := range files {
				open.File.Close()
			}
			return Export{}, nil, nil, auth.ErrNotFound
		}
		file, err := root.Open(artifactRelativePath(export.ID, seq+1))
		if err != nil {
			for _, open := range files {
				open.File.Close()
			}
			if os.IsNotExist(err) {
				return Export{}, nil, nil, auth.ErrNotFound
			}
			return Export{}, nil, nil, err
		}
		files = append(files, ArtifactFile{Artifact: artifact, File: file})
	}
	return export, files, manifest, nil
}

// OpenExportArtifact hands out one artifact file for a direct MP4 download.
// Expiry is enforced here: an expired artifact is gone the same way a deleted
// recording is, and its row only remains to tell the drawer what happened.
func (s *Service) OpenExportArtifact(ctx context.Context, p auth.Principal, exportID, artifactID id.ID) (*os.File, ExportArtifact, error) {
	export, err := s.GetExport(ctx, p, exportID)
	if err != nil {
		return nil, ExportArtifact{}, err
	}
	if export.State != "succeeded" {
		return nil, ExportArtifact{}, ErrExportNotReady
	}
	for seq, artifact := range export.Artifacts {
		if artifact.ID != artifactID {
			continue
		}
		if artifact.State != "ready" {
			return nil, ExportArtifact{}, auth.ErrNotFound
		}
		root, _, err := s.Pools.OpenMediaRoot(ctx, export.PoolID)
		if err != nil {
			return nil, ExportArtifact{}, err
		}
		defer root.Close()
		file, err := root.Open(artifactRelativePath(export.ID, seq+1))
		if err != nil {
			if os.IsNotExist(err) {
				return nil, ExportArtifact{}, auth.ErrNotFound
			}
			return nil, ExportArtifact{}, err
		}
		return file, artifact, nil
	}
	return nil, ExportArtifact{}, auth.ErrNotFound
}

// AuditExportDownload records who took the evidence, before the first byte
// leaves. It runs in its own transaction; a download the audit could not record
// must not start.
func (s *Service) AuditExportDownload(ctx context.Context, actor id.ID, download ExportDownload) error {
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		details, err := json.Marshal(map[string]any{"artifact_id": download.ArtifactID})
		if err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: actor, Action: "recording.export_downloaded", ObjectID: download.ExportID, Details: details})
	})
}

// manifestSource is one pinned source segment as the evidence document reports
// it: identities and time ranges, never filesystem paths.
type manifestSource struct {
	SegmentID        id.ID     `json:"segment_id"`
	SourceRevisionID id.ID     `json:"source_revision_id"`
	Start            time.Time `json:"start"`
	End              time.Time `json:"end"`
}
type manifestArtifact struct {
	ID           id.ID     `json:"id"`
	File         string    `json:"file"`
	SHA256       string    `json:"sha256"`
	SizeBytes    int64     `json:"size_bytes"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	SegmentCount int       `json:"segment_count"`
}
type exportManifest struct {
	Format      string    `json:"format"`
	TaskID      id.ID     `json:"task_id"`
	GeneratedAt time.Time `json:"generated_at"`
	CreatedBy   struct {
		ID   id.ID  `json:"id"`
		Name string `json:"name"`
	} `json:"created_by"`
	Channel struct {
		ID   id.ID  `json:"id"`
		No   int    `json:"no"`
		Name string `json:"name"`
	} `json:"channel"`
	Requested struct {
		Start time.Time `json:"start"`
		End   time.Time `json:"end"`
	} `json:"requested"`
	Actual *struct {
		Start time.Time `json:"start"`
		End   time.Time `json:"end"`
	} `json:"actual,omitempty"`
	GapMode   string             `json:"gap_mode"`
	Gaps      []ExportSpanView   `json:"gaps"`
	Artifacts []manifestArtifact `json:"artifacts"`
	Sources   []manifestSource   `json:"sources"`
}

// buildExportManifest derives the evidence document: EXP-03's fields, from the
// index, with the same seam rule the analysis showed the operator.
func (s *Service) buildExportManifest(ctx context.Context, export Export) ([]byte, error) {
	manifest := exportManifest{
		Format:      "one-nvr-export-manifest/1",
		TaskID:      export.ID,
		GeneratedAt: time.Now().UTC(),
		GapMode:     export.GapMode,
	}
	manifest.CreatedBy.Name = export.RequestedByName
	manifest.Channel.ID = export.ChannelID
	manifest.Channel.No = export.ChannelNo
	manifest.Requested.Start = export.RequestedStart
	manifest.Requested.End = export.RequestedEnd
	if export.ActualStart != nil && export.ActualEnd != nil {
		manifest.Actual = &struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
		}{Start: *export.ActualStart, End: *export.ActualEnd}
	}
	for _, artifact := range export.Artifacts {
		manifest.Artifacts = append(manifest.Artifacts, manifestArtifact{
			ID: artifact.ID, File: artifact.FileName, SHA256: artifact.SHA256,
			SizeBytes: artifact.SizeBytes, Start: artifact.Start, End: artifact.End,
			SegmentCount: artifact.SegmentCount,
		})
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT channel_name FROM channels WHERE id=$1`, export.ChannelID).Scan(&manifest.Channel.Name); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT requested_by FROM recording_exports WHERE id=$1`, export.ID).Scan(&manifest.CreatedBy.ID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT s.id,s.source_revision_id,s.start_at,s.end_at FROM recording_export_sources es JOIN recording_segments s ON s.id=es.segment_id WHERE es.export_id=$1 ORDER BY es.seq`, export.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		var spans []sourceSegment
		for rows.Next() {
			var source manifestSource
			if err := rows.Scan(&source.SegmentID, &source.SourceRevisionID, &source.Start, &source.End); err != nil {
				return err
			}
			manifest.Sources = append(manifest.Sources, source)
			spans = append(spans, sourceSegment{ID: source.SegmentID, State: "ready", Start: source.Start, End: source.End})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		plan := analyzeExportWindow(export.RequestedStart, export.RequestedEnd, spans)
		for _, gap := range plan.Gaps {
			manifest.Gaps = append(manifest.Gaps, ExportSpanView{Start: gap.Start, End: gap.End, DurationMS: gap.DurationMS()})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(manifest)
}

// FileName is the base name both the MP4 and the ZIP download use.
func (e Export) FileName() string {
	return fmt.Sprintf("CH%02d_%s", e.ChannelNo, e.RequestedStart.UTC().Format("20060102_150405"))
}

// WriteExportZip streams the evidence package: the manifest first, then every
// artifact stored uncompressed (MP4 does not deflate). The files are consumed
// and closed here; on error the interrupted body is the caller's signal.
func WriteExportZip(w io.Writer, manifest []byte, files []ArtifactFile) error {
	archive := zip.NewWriter(w)
	entry, err := archive.CreateHeader(&zip.FileHeader{Name: "manifest.json", Method: zip.Deflate, Modified: time.Now().UTC()})
	if err != nil {
		return err
	}
	if _, err := entry.Write(manifest); err != nil {
		return err
	}
	for _, file := range files {
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: file.Artifact.FileName, Method: zip.Store, Modified: file.Artifact.End.UTC()})
		if err != nil {
			file.File.Close()
			return err
		}
		if _, err := io.Copy(entry, file.File); err != nil {
			file.File.Close()
			return err
		}
		file.File.Close()
	}
	return archive.Close()
}
