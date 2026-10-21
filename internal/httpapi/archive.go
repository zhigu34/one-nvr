package httpapi

import (
	"net/http"

	"github.com/zhigu34/one-nvr/internal/archive"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

// listArchiveTargets handles GET /api/v1/archive-targets.
func (r *router) listArchiveTargets(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	targets, err := r.d.Archive.ListTargets(q.Context(), p)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []archive.Target `json:"items"`
	}{targets})
}

// decodeTargetBody is the shared body of register/update. The password is
// write-only: empty means "keep the stored one" on update.
func decodeTargetBody(w http.ResponseWriter, q *http.Request) (archive.TargetInput, bool) {
	var in struct {
		Name               string `json:"name"`
		BaseURL            string `json:"base_url"`
		RemoteRoot         string `json:"remote_root"`
		Username           string `json:"username"`
		Password           string `json:"password"`
		CloudRetentionDays int    `json:"cloud_retention_days"`
		Enabled            *bool  `json:"enabled"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return archive.TargetInput{}, false
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	days := in.CloudRetentionDays
	if days < 1 {
		days = 365
	}
	return archive.TargetInput{
		Name:               in.Name,
		BaseURL:            in.BaseURL,
		RemoteRoot:         in.RemoteRoot,
		Username:           in.Username,
		Password:           in.Password,
		CloudRetentionDays: days,
		Enabled:            enabled,
	}, true
}

func (r *router) registerArchiveTarget(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	in, ok := decodeTargetBody(w, q)
	if !ok {
		return
	}
	target, err := r.d.Archive.RegisterTarget(q.Context(), p, in)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 201, target)
}

func (r *router) updateArchiveTarget(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	targetID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	in, ok := decodeTargetBody(w, q)
	if !ok {
		return
	}
	target, err := r.d.Archive.UpdateTarget(q.Context(), p, targetID, in)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, target)
}

func (r *router) deleteArchiveTarget(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	targetID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	if err := r.d.Archive.DeleteTarget(q.Context(), p, targetID); err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct{}{})
}

// testArchiveTarget probes a stored target, or the submitted draft before it
// exists. Synchronous and bounded like the ONVIF probe.
func (r *router) testArchiveTarget(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	var in struct {
		ID                 *id.ID `json:"id"`
		Name               string `json:"name"`
		BaseURL            string `json:"base_url"`
		RemoteRoot         string `json:"remote_root"`
		Username           string `json:"username"`
		Password           string `json:"password"`
		CloudRetentionDays int    `json:"cloud_retention_days"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	var draft *archive.TargetInput
	if in.ID == nil {
		days := in.CloudRetentionDays
		if days < 1 {
			days = 365
		}
		draft = &archive.TargetInput{
			Name: in.Name, BaseURL: in.BaseURL, RemoteRoot: in.RemoteRoot,
			Username: in.Username, Password: in.Password, CloudRetentionDays: days, Enabled: true,
		}
	}
	caps, err := r.d.Archive.TestTarget(q.Context(), p, targetIDOrEmpty(in.ID), draft)
	if err != nil {
		// The verdict goes in the message; the evidence rides along as fields so
		// the page can show which step failed and what the server answered
		// ("write" + "HTTP 409") instead of only the conclusion.
		fields := map[string]string{}
		if caps.Stage != "" {
			fields["stage"] = caps.Stage
		}
		if caps.Detail != "" {
			fields["detail"] = caps.Detail
		}
		if mapped, ok := archiveFaultWithFields(err, fields); ok {
			fail(w, q, mapped)
			return
		}
		fail(w, q, err)
		return
	}
	respond(w, q, 200, caps)
}

func targetIDOrEmpty(value *id.ID) id.ID {
	if value == nil {
		return ""
	}
	return *value
}

// requestArchiveUploads handles POST /api/v1/recordings/archive: one upload
// task per ready segment, per-row archive grant, partial success.
func (r *router) requestArchiveUploads(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	var in struct {
		TargetID   id.ID   `json:"target_id"`
		SegmentIDs []id.ID `json:"segment_ids"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	created, err := r.d.Archive.CreateUploads(q.Context(), p, in.TargetID, in.SegmentIDs)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 202, struct {
		Queued []id.ID `json:"queued"`
	}{created})
}

// retryArchiveFailures handles POST /api/v1/archive-targets/{id}/retry: every
// copy that stopped without finishing is queued again.
func (r *router) retryArchiveFailures(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	targetID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	revived, err := r.d.Archive.RetryFailures(q.Context(), p, targetID)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 202, struct {
		Retried int `json:"retried"`
	}{revived})
}

// listArchiveTargetStatuses handles GET /api/v1/archive-targets/status: what
// each target is actually doing, as opposed to how it is configured.
func (r *router) listArchiveTargetStatuses(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	items, err := r.d.Archive.TargetStatuses(q.Context(), p)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []archive.TargetStatus `json:"items"`
	}{items})
}

// listChannelArchiveStatuses handles GET /api/v1/archive-channels: every
// channel bound to a target, with its footage counts and the local retention
// window that applies to it.
func (r *router) listChannelArchiveStatuses(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	items, err := r.d.Archive.ChannelArchiveStatuses(q.Context(), p)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []archive.ChannelArchiveStatus `json:"items"`
	}{items})
}

// listArchivePolicies handles GET /api/v1/archive-policies.
func (r *router) listArchivePolicies(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	policies, err := r.d.Archive.ListPolicies(q.Context(), p)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []archive.Policy `json:"items"`
	}{policies})
}

// upsertArchivePolicy handles PUT /api/v1/archive-policies: one channel's
// binding. mode manual with no target removes the binding.
func (r *router) upsertArchivePolicy(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	var in struct {
		ChannelID id.ID  `json:"channel_id"`
		TargetID  id.ID  `json:"target_id"`
		Mode      string `json:"mode"`
		Schedule  string `json:"schedule"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	policy, err := r.d.Archive.UpsertPolicy(q.Context(), p, archive.Policy{
		ChannelID: in.ChannelID, TargetID: in.TargetID, Mode: in.Mode, Schedule: in.Schedule,
	})
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, policy)
}

// deleteCloudCopy handles DELETE /api/v1/recordings/{id}/cloud-copy: remove one
// recording's remote object without touching its local copy.
//
// A separate route from the local delete because they are different decisions: the
// trash button on the recordings page removes the LOCAL file (and keeps the cloud
// copy playable), while this one removes the CLOUD object. Folding them together
// would make "delete" mean two things depending on which copy happened to exist.
func (r *router) deleteCloudCopy(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	recordingID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	if err := r.d.Archive.DeleteCloudCopy(q.Context(), p, recordingID); err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct{}{})
}
