package httpapi

import (
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/tlsmanager"
	"net/http"
)

func (r *router) tlsReady(w http.ResponseWriter, q *http.Request, p auth.Principal) bool {
	if err := r.d.Auth.RequireAdmin(q.Context(), p); err != nil {
		fail(w, q, err)
		return false
	}
	if r.d.TLS == nil {
		fail(w, q, fault.New(501, "not_available", "证书管理尚未配置"))
		return false
	}
	return true
}
func (r *router) tlsState(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if !r.tlsReady(w, q, p) {
		return
	}
	out, err := r.d.TLS.Current(q.Context(), p)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func (r *router) tlsImport(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if !r.tlsReady(w, q, p) {
		return
	}
	var in struct {
		Chain string `json:"fullchain_pem"`
		Key   string `json:"private_key_pem"`
	}
	if err := decodeLimit(w, q, &in, 2*tlsmanager.MaxPEMBytes); err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.TLS.Import(q.Context(), p, []byte(in.Chain), []byte(in.Key))
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 201, out)
}
func (r *router) tlsWatch(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if !r.tlsReady(w, q, p) {
		return
	}
	version, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in struct {
		AutoApply *bool `json:"auto_apply"`
	}
	if err = decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	if in.AutoApply == nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	if err = r.d.TLS.SetAutoApply(q.Context(), p, version, *in.AutoApply); err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.TLS.Current(q.Context(), p)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func acceptedJob(w http.ResponseWriter, q *http.Request, jobID id.ID) {
	respond(w, q, 202, struct {
		JobID id.ID `json:"job_id"`
	}{jobID})
}
func (r *router) tlsCheck(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if !r.tlsReady(w, q, p) {
		return
	}
	jobID, err := r.d.TLS.RequestDirectoryCheck(q.Context(), p)
	if err != nil {
		fail(w, q, err)
		return
	}
	acceptedJob(w, q, jobID)
}
func (r *router) tlsApply(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if !r.tlsReady(w, q, p) {
		return
	}
	target, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	version, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	jobID, err := r.d.TLS.RequestApply(q.Context(), p, target, version)
	if err != nil {
		fail(w, q, err)
		return
	}
	acceptedJob(w, q, jobID)
}
func (r *router) tlsRollback(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if !r.tlsReady(w, q, p) {
		return
	}
	version, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	jobID, err := r.d.TLS.RequestRollback(q.Context(), p, version)
	if err != nil {
		fail(w, q, err)
		return
	}
	acceptedJob(w, q, jobID)
}
