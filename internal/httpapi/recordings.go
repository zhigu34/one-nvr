package httpapi

import (
	"net/http"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
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
func (r *router) recordingContent(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	recordingID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	if err := r.d.Recordings.AuthorizeSegment(q.Context(), p, recordingID); err != nil {
		fail(w, q, err)
		return
	}
	fail(w, q, fault.New(501, "feature_not_available", "录像内容播放将在 M1-C 阶段提供"))
}
