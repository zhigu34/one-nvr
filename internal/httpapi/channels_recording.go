package httpapi

import (
	"errors"
	"net/http"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/recording"
)

func (r *router) sourceStatus(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	ch, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Sources.GetStatus(q.Context(), p, ch)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func (r *router) recordingPolicy(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	ch, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Sources.GetPolicy(q.Context(), p, ch)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func (r *router) setRecordingPolicy(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	ch, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	version, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in struct {
		Mode string `json:"mode"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Sources.SetPolicy(q.Context(), p, ch, version, in.Mode, q.Header.Get("Idempotency-Key"))
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 202, out)
}

// setRecordingPolicies applies one recording mode to many channels. Each row
// carries its own expected version, so the request has no single If-Match; a
// row that conflicts or is unauthorized is reported in place.
func (r *router) setRecordingPolicies(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	var in struct {
		Items []channel.PolicyItem `json:"items"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Sources.SetPolicies(q.Context(), p, in.Items, q.Header.Get("Idempotency-Key"))
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 202, out)
}
func (r *router) bindChannelPool(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	ch, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	version, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in struct {
		PoolID id.ID `json:"pool_id"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Sources.BindPool(q.Context(), p, ch, in.PoolID, version, q.Header.Get("Idempotency-Key"))
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 202, out)
}

// startManualRecording begins a bounded manual recording window (REC-03).
//
// The body is {minutes}; an omitted value means the PRD's 30-minute default.
// This is not a policy change: the channel's configured mode is untouched, and
// when the window expires recording returns to whatever the policy says.
func (r *router) startManualRecording(w http.ResponseWriter, q *http.Request, p auth.Principal, rest string) {
	ch, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in struct {
		Minutes int `json:"minutes"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Recordings.StartManual(q.Context(), p, ch, in.Minutes)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 201, out)
}

// stopManualRecording ends a manual window early. Ending one that is not
// running is a success: the desired state already holds.
func (r *router) stopManualRecording(w http.ResponseWriter, q *http.Request, p auth.Principal, rest string) {
	ch, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	if err := r.d.Recordings.StopManual(q.Context(), p, ch); err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct{}{})
}

// manualRecording reports the channel's running window, or 404 when none.
func (r *router) manualRecording(w http.ResponseWriter, q *http.Request, p auth.Principal, rest string) {
	ch, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Recordings.ActiveManual(q.Context(), p, ch)
	if errors.Is(err, recording.ErrManualWindowMissing) {
		fail(w, q, fault.New(404, "manual_recording_absent", "该通道当前没有手动录像"))
		return
	}
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
