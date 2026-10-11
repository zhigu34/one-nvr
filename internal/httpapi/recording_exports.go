package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/recording"
)

// analyzeRecordingExport is the read-only range preview behind the export
// dialog: coverage, gaps, unavailable segments — before anything is created.
func (r *router) analyzeRecordingExport(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	channelID, err := id.Parse(q.URL.Query().Get("channel_id"))
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	start, end, err := exportWindow(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	analysis, err := r.d.Recordings.AnalyzeExport(q.Context(), p, channelID, start, end)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, analysis)
}

// createRecordingExport pins sources and enqueues the task. Like every command
// endpoint it takes an Idempotency-Key, so a transport retry never produces a
// second task.
func (r *router) createRecordingExport(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	var in struct {
		ChannelID id.ID  `json:"channel_id"`
		Start     int64  `json:"start"`
		End       int64  `json:"end"`
		GapMode   string `json:"gap_mode"`
		PoolID    id.ID  `json:"pool_id"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	export, jobID, err := r.d.Recordings.CreateExport(q.Context(), p, recording.ExportCreateInput{
		ChannelID:      in.ChannelID,
		Start:          time.UnixMilli(in.Start).UTC(),
		End:            time.UnixMilli(in.End).UTC(),
		GapMode:        in.GapMode,
		PoolID:         in.PoolID,
		IdempotencyKey: q.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 202, struct {
		ExportID id.ID `json:"export_id"`
		JobID    id.ID `json:"job_id"`
	}{export.ID, jobID})
}

func (r *router) listRecordingExports(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	channelID, err := id.Parse(q.URL.Query().Get("channel_id"))
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	limit, _ := strconv.Atoi(q.URL.Query().Get("limit"))
	exports, err := r.d.Recordings.ListExports(q.Context(), p, channelID, limit)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []recording.Export `json:"items"`
	}{exports})
}

func (r *router) getRecordingExport(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	exportID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	export, err := r.d.Recordings.GetExport(q.Context(), p, exportID)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, export)
}

// downloadRecordingExport streams the evidence package: the manifest plus every
// artifact, zipped on the fly. The manifest is derived, never stored, so the
// package can never disagree with its own hashes.
func (r *router) downloadRecordingExport(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	exportID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	export, files, manifest, err := r.d.Recordings.OpenExportBundle(q.Context(), p, exportID)
	if err != nil {
		fail(w, q, err)
		return
	}
	defer func() {
		for _, file := range files {
			file.File.Close()
		}
	}()
	download := recording.ExportDownload{ExportID: export.ID}
	if len(files) == 1 {
		download.ArtifactID = files[0].Artifact.ID
	}
	if err := r.d.Recordings.AuditExportDownload(q.Context(), p.UserID, download); err != nil {
		fail(w, q, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", export.FileName()+".zip"))
	w.Header().Set("Cache-Control", "private, no-store")
	_ = recording.WriteExportZip(w, manifest, files)
}

// recordingExportArtifactContent streams one artifact MP4 as a download.
func (r *router) recordingExportArtifactContent(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	exportID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	artifactID, err := id.Parse(q.PathValue("artifactId"))
	if err != nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	file, artifact, err := r.d.Recordings.OpenExportArtifact(q.Context(), p, exportID, artifactID)
	if err != nil {
		fail(w, q, err)
		return
	}
	defer file.Close()
	if err := r.d.Recordings.AuditExportDownload(q.Context(), p.UserID, recording.ExportDownload{ExportID: exportID, ArtifactID: artifact.ID}); err != nil {
		fail(w, q, err)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.SizeBytes, 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", artifact.FileName))
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	// The audit row precedes the first byte; an interrupted copy after that is
	// an operator-visible download failure, not an index problem.
	_, _ = io.Copy(w, file)
}

func (r *router) setExportArtifactProtection(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	exportID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	artifactID, err := id.Parse(q.PathValue("artifactId"))
	if err != nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	var in struct {
		Protected *bool `json:"protected"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	if in.Protected == nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	artifact, err := r.d.Recordings.SetExportArtifactProtection(q.Context(), p, exportID, artifactID, *in.Protected)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, artifact)
}

// protectRecordingWindow implements EXP-04: mark every ready segment the window
// intersects, in one request, with one audit record.
func (r *router) protectRecordingWindow(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	var in struct {
		ChannelID id.ID `json:"channel_id"`
		Start     int64 `json:"start"`
		End       int64 `json:"end"`
		Protected *bool `json:"protected"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	if in.Protected == nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	updated, err := r.d.Recordings.ProtectWindow(q.Context(), p, in.ChannelID, time.UnixMilli(in.Start).UTC(), time.UnixMilli(in.End).UTC(), *in.Protected)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Updated int `json:"updated"`
	}{updated})
}

// exportWindow parses the shared start/end query form (epoch milliseconds).
func exportWindow(q *http.Request) (time.Time, time.Time, error) {
	start, errStart := strconv.ParseInt(q.URL.Query().Get("start"), 10, 64)
	end, errEnd := strconv.ParseInt(q.URL.Query().Get("end"), 10, 64)
	if errStart != nil || errEnd != nil {
		return time.Time{}, time.Time{}, auth.ErrInvalid
	}
	return time.UnixMilli(start).UTC(), time.UnixMilli(end).UTC(), nil
}
