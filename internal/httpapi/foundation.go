package httpapi

import (
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/site"
	"net/http"
)

func (r *router) currentSite(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	out, err := r.d.Site.Current(q.Context(), p)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func (r *router) updateSite(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	expected, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in site.UpdateInput
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Site.Update(q.Context(), p, expected, in)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func (r *router) expandSite(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	expected, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Site.Expand(q.Context(), p, expected)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func (r *router) timezones(w http.ResponseWriter, q *http.Request) {
	respond(w, q, 200, site.Timezones())
}
func (r *router) listChannels(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	cursor, limit, err := pagination(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Channels.List(q.Context(), p, cursor, limit)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func (r *router) updateChannel(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	channelID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	expected, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in channel.UpdateInput
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Channels.Update(q.Context(), p, channelID, expected, in)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func (r *router) capabilities(w http.ResponseWriter, q *http.Request, _ auth.Principal, _ string) {
	out, err := r.d.Capabilities.Snapshot(q.Context())
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func (r *router) components(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	out, err := r.d.Operations.Components(q.Context(), p)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func (r *router) job(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	jobID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Operations.Job(q.Context(), p, jobID)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func (r *router) auditLogs(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	cursor, limit, err := pagination(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Operations.Audits(q.Context(), p, cursor, limit)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func (r *router) futureArchive(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if err := r.d.Auth.RequireAdmin(q.Context(), p); err != nil {
		fail(w, q, err)
		return
	}
	fail(w, q, r.d.Capabilities.Require("archive"))
}
func (r *router) futureIntelligence(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if err := r.d.Auth.RequireAdmin(q.Context(), p); err != nil {
		fail(w, q, err)
		return
	}
	fail(w, q, r.d.Capabilities.Require("intelligence"))
}
