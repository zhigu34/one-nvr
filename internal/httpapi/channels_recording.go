package httpapi

import (
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"net/http"
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
