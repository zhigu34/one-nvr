package httpapi

import (
	"net/http"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/site"
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

func (r *router) channelSummaries(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	out, err := r.d.Sources.Summaries(q.Context(), p)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}

func (r *router) channelSlots(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	out, err := r.d.Channels.Slots(q.Context(), p)
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
	// Audits get a higher ceiling than the shared default: one cleanup round
	// writes hundreds of rows sharing a timestamp, and the page collapses those
	// into a single line. At 50 per page a collapsed first page can show three
	// rows, which reads as missing data rather than as a large batch.
	cursor, limit, err := paginationLimit(q, 100, 200)
	if err != nil {
		fail(w, q, err)
		return
	}
	prefix := q.URL.Query().Get("action_prefix")
	out, err := r.d.Operations.Audits(q.Context(), p, prefix, cursor, limit)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}

// futureArchive guards the M1-A placeholder path. The module's business is
// delivered, but not at this address: cloud archive is reached through the
// target and upload endpoints. A caller arriving here is told where to go
// instead of being told the feature does not exist.
func (r *router) futureArchive(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if err := r.d.Auth.RequireAdmin(q.Context(), p); err != nil {
		fail(w, q, err)
		return
	}
	if err := r.d.Capabilities.Require("archive"); err != nil {
		fail(w, q, err)
		return
	}
	fail(w, q, fault.New(501, "endpoint_not_available", "该占位端点未实现：云归档请使用 /api/v1/recordings/archive 与 /api/v1/archive-targets"))
}

// futureIntelligence guards the M1-A placeholder path for the same reason.
func (r *router) futureIntelligence(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if err := r.d.Auth.RequireAdmin(q.Context(), p); err != nil {
		fail(w, q, err)
		return
	}
	if err := r.d.Capabilities.Require("intelligence"); err != nil {
		fail(w, q, err)
		return
	}
	fail(w, q, fault.New(501, "endpoint_not_available", "该占位端点未实现：检测规则请使用 /api/v1/detection/rules"))
}
