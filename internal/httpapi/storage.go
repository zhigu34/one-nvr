package httpapi

import (
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/storage"
	"net/http"
)

func (r *router) listPools(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	cursor, limit, err := pagination(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Storage.ListPage(q.Context(), p, cursor, limit)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func (r *router) registerPool(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	var in storage.RegisterInput
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Storage.Register(q.Context(), p, in)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 201, out)
}
func (r *router) updatePool(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	poolID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	expected, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in storage.UpdateInput
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Storage.Update(q.Context(), p, poolID, expected, in)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func (r *router) deletePool(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	poolID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	expected, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	if err := r.d.Storage.Delete(q.Context(), p, poolID, expected); err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct{}{})
}
func (r *router) testPool(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	poolID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	jobID, err := r.d.Storage.RequestCheck(q.Context(), p, poolID)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 202, struct {
		JobID id.ID `json:"job_id"`
	}{jobID})
}
