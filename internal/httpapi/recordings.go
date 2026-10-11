package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/recording"
)

func (r *router) recordings(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	query, err := recordingQuery(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	page, err := r.d.Recordings.List(q.Context(), p, query)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, page)
}

// recordingQuery parses the channel/window plus the optional filters the
// management view adds. Absent filters mean "no restriction", so the playback
// caller keeps passing only the three required parameters.
func recordingQuery(q *http.Request) (recording.Query, error) {
	var out recording.Query
	var err error
	if out.ChannelID, err = id.Parse(q.URL.Query().Get("channel_id")); err != nil {
		return out, auth.ErrInvalid
	}
	if out.Start, err = time.Parse(time.RFC3339, q.URL.Query().Get("start")); err != nil {
		return out, auth.ErrInvalid
	}
	if out.End, err = time.Parse(time.RFC3339, q.URL.Query().Get("end")); err != nil {
		return out, auth.ErrInvalid
	}
	if out.Cursor, out.Limit, err = pagination(q); err != nil {
		return out, err
	}
	if raw := q.URL.Query().Get("source_revision_id"); raw != "" {
		if out.SourceRevisionID, err = id.Parse(raw); err != nil {
			return out, auth.ErrInvalid
		}
	}
	if raw := q.URL.Query().Get("pool_id"); raw != "" {
		if out.PoolID, err = id.Parse(raw); err != nil {
			return out, auth.ErrInvalid
		}
	}
	for _, raw := range q.URL.Query()["state"] {
		if raw == "" {
			return out, auth.ErrInvalid
		}
		out.States = append(out.States, raw)
	}
	if raw := q.URL.Query().Get("protected"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return out, auth.ErrInvalid
		}
		out.Protected = &value
	}
	return out, nil
}

// recordingContent streams one published segment. The body is written by
// hand rather than by http.ServeContent because the contract serves exactly one
// byte range, answers an unsatisfiable range with the JSON error envelope, and
// must persist an audit row before the first media byte: ServeContent would
// instead emit multipart/byteranges and a text/plain 416, and would let an
// unrecoverable audit failure happen after bytes had already left.
func (r *router) recordingContent(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	recordingID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	content, err := r.d.Recordings.OpenSegment(q.Context(), p, recordingID)
	if err != nil {
		fail(w, q, err)
		return
	}
	defer content.Close()
	plan, err := content.PlanRange(q.Header.Get("Range"), q.Header.Get("If-Range"))
	if err != nil {
		// A 416 advertises the resource length but no media byte.
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(content.Size, 10))
		w.Header().Set("ETag", content.ETag())
		fail(w, q, err)
		return
	}
	if err := r.d.Recordings.AuditPlayback(q.Context(), p, content, plan); err != nil {
		fail(w, q, err)
		return
	}
	status := http.StatusOK
	if plan.Partial {
		status = http.StatusPartialContent
		w.Header().Set("Content-Range", plan.ContentRange(content.Size))
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", content.ETag())
	w.Header().Set("Content-Length", strconv.FormatInt(plan.Length(), 10))
	w.WriteHeader(status)
	written, err := io.CopyN(w, io.NewSectionReader(content.File, plan.Start, plan.Length()), plan.Length())
	if err != nil {
		// The response is already committed; this is an operator signal only.
		slog.Warn("recording content stream interrupted", "segment", string(content.SegmentID), "bytes_written", written, "context_cancelled", q.Context().Err() != nil)
	}
}

// recordingStats summarises the same filters the list accepts, so the
// management view can show the scope of a selection before deleting it.
func (r *router) recordingStats(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	query, err := recordingQuery(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	stats, err := r.d.Recordings.Summarize(q.Context(), p, query)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, stats)
}

// setRecordingProtection marks or releases evidence protection. Only an
// administrator may change it (PRD §3): retention and deletion both skip a
// protected recording, so releasing protection is what makes deletion possible.
func (r *router) setRecordingProtection(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	recordingID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
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
	segment, err := r.d.Recordings.SetProtection(q.Context(), p, recordingID, *in.Protected)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, segment)
}

// deleteRecordingsLocal removes the local copy of the selected recordings. It is
// synchronous and per-item: unlinking a handful of files is fast, and the PRD
// requires a per-row result rather than a bulk claim, so an accepted row is
// never masked by a rejected one.
func (r *router) deleteRecordingsLocal(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	var in struct {
		SegmentIDs []id.ID `json:"segment_ids"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	if len(in.SegmentIDs) == 0 || len(in.SegmentIDs) > 100 {
		fail(w, q, auth.ErrInvalid)
		return
	}
	for _, segmentID := range in.SegmentIDs {
		if _, err := id.Parse(string(segmentID)); err != nil {
			fail(w, q, auth.ErrInvalid)
			return
		}
	}
	outcomes, err := r.d.Recordings.DeleteLocal(q.Context(), p, in.SegmentIDs, "manual")
	if err != nil {
		fail(w, q, err)
		return
	}
	var released int64
	for _, outcome := range outcomes {
		released += outcome.Bytes
	}
	respond(w, q, 200, struct {
		Items         []recording.DeleteOutcome `json:"items"`
		ReleasedBytes int64                     `json:"released_bytes"`
	}{outcomes, released})
}
