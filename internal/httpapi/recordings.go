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
	channelID, err := id.Parse(q.URL.Query().Get("channel_id"))
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	start, err := time.Parse(time.RFC3339, q.URL.Query().Get("start"))
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	end, err := time.Parse(time.RFC3339, q.URL.Query().Get("end"))
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	cursor, limit, err := pagination(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	page, err := r.d.Recordings.List(q.Context(), p, recording.Query{ChannelID: channelID, Start: start, End: end, Cursor: cursor, Limit: limit})
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, page)
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
