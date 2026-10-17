package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/reliability"
)

// parseWindow reads the from/to pair every history endpoint takes. Both are
// required: an interval query with no window would scan a year of rows to
// answer a question nobody asked.
func parseWindow(q *http.Request) (time.Time, time.Time, error) {
	from, err := time.Parse(time.RFC3339, q.URL.Query().Get("from"))
	if err != nil {
		return time.Time{}, time.Time{}, auth.ErrInvalid
	}
	to, err := time.Parse(time.RFC3339, q.URL.Query().Get("to"))
	if err != nil {
		return time.Time{}, time.Time{}, auth.ErrInvalid
	}
	if !to.After(from) {
		return time.Time{}, time.Time{}, auth.ErrInvalid
	}
	return from, to, nil
}

// reliabilityIntervals handles GET /api/v1/channels/{id}/reliability/intervals.
//
// It answers "what was this channel's state, and when" — the fact layer the
// reliability page draws as time bands. It reports intervals rather than rates
// on purpose: PRD REL-04 requires every ratio to carry its numerator,
// denominator and unjudgeable time, and those arrive with the batch that
// computes them.
func (r *router) reliabilityIntervals(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Reliability == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	channelID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	from, to, err := parseWindow(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var kinds []string
	if raw := q.URL.Query()["kind"]; len(raw) > 0 {
		kinds = raw
	}
	page, err := r.d.Reliability.ListIntervals(q.Context(), p, channelID, from, to, kinds)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, page)
}

// reliabilityFaults handles GET /api/v1/faults.
//
// With a channel it is the same evidence view as intervals; without one it
// spans channels, which is an administrator's comparison (PRD REL-05).
func (r *router) reliabilityFaults(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Reliability == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	from, to, err := parseWindow(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	query := reliability.FaultQuery{
		Category: q.URL.Query().Get("category"),
		State:    q.URL.Query().Get("state"),
		From:     from,
		To:       to,
	}
	if raw := q.URL.Query().Get("channel_id"); raw != "" {
		parsed, err := id.Parse(raw)
		if err != nil {
			fail(w, q, auth.ErrInvalid)
			return
		}
		query.ChannelID = parsed
	}
	items, err := r.d.Reliability.ListFaults(q.Context(), p, query)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []reliability.FaultView `json:"items"`
		// The category vocabulary travels with the list so a client renders
		// what this build actually classifies rather than a copy that can
		// drift from the server's.
		Categories []string `json:"categories"`
	}{items, reliability.Categories()})
}

// confirmFault handles PATCH /api/v1/faults/{id}.
//
// An administrator records what actually happened. The automatic attribution
// stays exactly as it was: PRD REL-02 forbids rewriting it, because that is how
// an outage quietly stops being one.
func (r *router) confirmFault(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Reliability == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	faultID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in struct {
		Reason   string `json:"reason"`
		Category string `json:"category"`
		Note     string `json:"note"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	if err := r.d.Reliability.ConfirmFault(q.Context(), p, faultID, in.Reason, in.Category, in.Note); err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Confirmed bool `json:"confirmed"`
	}{true})
}

// faultImpact handles GET /api/v1/faults/{id}/impact: the real business impact
// of a component incident, as the union of its channels rather than their sum.
func (r *router) faultImpact(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Reliability == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	faultID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	impact, err := r.d.Reliability.FaultImpact(q.Context(), p, faultID)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		ImpactMS int64 `json:"impact_ms"`
	}{impact.Milliseconds()})
}

// reliabilityObligations handles GET /api/v1/channels/{id}/reliability/obligations.
//
// This is the recording-completeness denominator (PRD REL-03): the stretches
// during which recording was required, and the stretches it was explicitly not
// required with the reason. It exists as its own endpoint because the reasons
// matter as much as the totals — "not required because you disabled the channel"
// and "required but nothing was recorded" are different answers, and a single
// number would merge them.
func (r *router) reliabilityObligations(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Reliability == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	channelID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	from, to, err := parseWindow(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	// The obligation timeline is evidence about a channel's media, so it
	// travels with the playback grant — the same grant the intervals endpoint
	// requires.
	if err := r.d.Auth.RequireChannel(q.Context(), p, channelID, auth.Playback); err != nil {
		fail(w, q, err)
		return
	}
	items, err := r.d.Reliability.ListObligations(q.Context(), channelID, from, to)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []reliability.ObligationView `json:"items"`
	}{items})
}

// reliabilityIntegrity handles GET /api/v1/channels/{id}/reliability/integrity.
//
// It returns the four durations and their attribution, never a bare percentage:
// PRD REL-04 requires every rate to carry its numerator, denominator,
// unjudgeable time and verification level, and a lone percentage is exactly the
// figure this chapter exists to prevent.
func (r *router) reliabilityIntegrity(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Reliability == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	channelID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	from, to, err := parseWindow(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	coverage, err := r.d.Reliability.RecordingCoverage(q.Context(), p, channelID, from, to)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, coverage)
}

// reliabilityMetrics handles GET /api/v1/channels/{id}/reliability/metrics.
//
// This is where the chapter's rate values arrive, and the shape is the
// requirement: PRD REL-04 forbids a lone percentage, so every figure travels
// with its numerator, denominator and unjudgeable time, and the denominators
// that cannot be evaluated say why (no observation / not applicable / no
// samples) instead of dividing by zero and printing 0%.
//
// All stream kinds are returned separately. There is deliberately no merged
// value: "a healthy sub stream must not mask an unavailable main stream" is a
// statement about the shape of the answer, so the shape has no room for it.
func (r *router) reliabilityMetrics(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Reliability == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	channelID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	from, to, err := parseWindow(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	items, err := r.d.Reliability.ChannelMetricsAllKinds(q.Context(), p, channelID, from, to)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []reliability.Metrics `json:"items"`
	}{items})
}

// reliabilityTrend handles GET /api/v1/channels/{id}/reliability/trend.
//
// The per-day series the reliability page draws. Read from the daily rollup so
// a 90-day window does not scan every interval row it contains; when the day is
// still inside the minute tier the figure is the same either way, which the
// integration tests assert rather than assume.
func (r *router) reliabilityTrend(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Reliability == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	channelID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	from, to, err := parseWindow(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	kind := q.URL.Query().Get("kind")
	if kind == "" {
		kind = "main"
	}
	items, err := r.d.Reliability.DailyTrend(q.Context(), p, channelID, kind, from, to)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []reliability.TrendPoint `json:"items"`
		// Precision says which tier answered, per REL-06.
		Precision string `json:"precision"`
	}{items, reliability.PrecisionDaily})
}

// reliabilitySummary handles GET /api/v1/reliability/summary.
//
// A cross-channel view for the overview page's abnormal-channel list and the
// operations page's comparison (REL-05). It is an administrator read: comparing
// channels means reading media evidence about all of them.
//
// It sums durations per stream kind and returns the channel count alongside.
// There is deliberately no average availability: averaging rates across
// channels with different observed durations would weight them equally and
// produce a number that describes nothing.
func (r *router) reliabilitySummary(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Reliability == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	if err := r.d.Auth.RequireAdmin(q.Context(), p); err != nil {
		fail(w, q, err)
		return
	}
	from, to, err := parseWindow(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	items, err := r.d.Reliability.ChannelComparison(q.Context(), from, to)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []reliability.ChannelSummary `json:"items"`
	}{items})
}

// reliabilityComplianceCSV handles
// GET /api/v1/channels/{id}/reliability/export.csv.
//
// The download carries what PRD REL-05 names: the denominator, excluded and
// unverified time, the metric version and the time zone — so nobody can quote a
// percentage from this file without the thing it is a fraction of. The zone
// comes from the site, matching how every other timestamp in the product is
// interpreted.
func (r *router) reliabilityComplianceCSV(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Reliability == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	channelID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	from, to, err := parseWindow(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	zone := "UTC"
	if r.d.Site != nil {
		if site, err := r.d.Site.Current(q.Context(), p); err == nil && site.Timezone != "" {
			zone = site.Timezone
		}
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "reliability-"+string(channelID)+".csv"))
	w.Header().Set("Cache-Control", "private, no-store")
	// Streamed rather than buffered: a 90-day export is small, but writing
	// directly means a long window cannot be turned into a memory spike.
	if err := r.d.Reliability.WriteComplianceCSV(q.Context(), p, channelID, from, to, zone, w); err != nil {
		// The headers are already sent, so the failure cannot become a JSON
		// error body; logging is all that is left, and the truncated file is
		// visibly short.
		slog.Warn("reliability CSV export incomplete", "channel", channelID, "error", err)
	}
}

// reliabilityAlerts handles GET /api/v1/reliability/alerts.
func (r *router) reliabilityAlerts(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Reliability == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	from, to, err := parseWindow(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var channelID *id.ID
	if raw := q.URL.Query().Get("channel_id"); raw != "" {
		value, err := id.Parse(raw)
		if err != nil {
			fail(w, q, auth.ErrInvalid)
			return
		}
		channelID = &value
	}
	items, err := r.d.Reliability.ListAlerts(q.Context(), p, channelID, from, to)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []reliability.Alert `json:"items"`
		// Kinds is the vocabulary this build evaluates, so the settings UI
		// renders the rules that exist rather than a copy that can drift.
		Kinds []string `json:"kinds"`
	}{items, reliability.AlertKinds()})
}

// reliabilityAlertRules handles GET/POST /api/v1/reliability/alert-rules.
//
// Thresholds are configuration, so both directions are administrator-only. The
// page that owns them shows the same vocabulary the evaluator implements.
func (r *router) reliabilityAlertRules(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Reliability == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	switch q.Method {
	case http.MethodGet:
		items, err := r.d.Reliability.ListAlertRules(q.Context(), p)
		if err != nil {
			fail(w, q, err)
			return
		}
		respond(w, q, 200, struct {
			Items []reliability.AlertRule `json:"items"`
			Kinds []string                `json:"kinds"`
		}{items, reliability.AlertKinds()})
	default:
		var in reliability.AlertRuleInput
		if err := decode(w, q, &in); err != nil {
			fail(w, q, err)
			return
		}
		rule, err := r.d.Reliability.SetAlertRule(q.Context(), p, in)
		if err != nil {
			fail(w, q, err)
			return
		}
		respond(w, q, 200, rule)
	}
}

// reliabilityAlertRuleDelete handles DELETE /api/v1/reliability/alert-rules/{id}.
func (r *router) reliabilityAlertRuleDelete(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Reliability == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	ruleID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	if err := r.d.Reliability.DeleteAlertRule(q.Context(), p, ruleID); err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Deleted bool `json:"deleted"`
	}{true})
}

// recordingPolicyHistory handles GET /api/v1/recording-policy-history.
//
// The timeline an operator needs to answer "why does this month's denominator
// differ from last month's". It is an administrator read: the mode history of
// every channel is configuration, not media, and the channel-scoped version of
// the same question is already answered by the obligations endpoint.
func (r *router) recordingPolicyHistory(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Reliability == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	if err := r.d.Auth.RequireAdmin(q.Context(), p); err != nil {
		fail(w, q, err)
		return
	}
	channelID := id.ID(q.URL.Query().Get("channel_id"))
	if channelID == "" {
		fail(w, q, auth.ErrInvalid)
		return
	}
	from, to, err := parseWindow(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	items, err := r.d.Reliability.ListPolicyVersions(q.Context(), channelID, from, to)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []reliability.PolicyVersionView `json:"items"`
		// Precision travels with the timeline because a version this migration
		// backfilled has no observed start, and a reader must be able to tell.
		Precision string `json:"precision"`
	}{items, reliability.PolicyPrecision(items, from)})
}

// listMaintenance handles GET /api/v1/maintenance-windows.
func (r *router) listMaintenance(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Reliability == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	from, to, err := parseWindow(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	items, err := r.d.Reliability.ListMaintenance(q.Context(), p, from, to)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []reliability.MaintenanceWindow `json:"items"`
	}{items})
}

// registerMaintenance handles POST /api/v1/maintenance-windows.
//
// A registered window reclassifies the faults inside it as planned without
// deleting them: the outage happened, the operator is telling us it was
// expected. PRD REL-02 wants planned stops counted apart, and it also wants a
// backfilled window to be recognisable as one, so the registration time is
// stored beside the window.
func (r *router) registerMaintenance(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Reliability == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	var in struct {
		ChannelID *id.ID `json:"channel_id"`
		StartsAt  string `json:"starts_at"`
		EndsAt    string `json:"ends_at"`
		Note      string `json:"note"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	starts, err := time.Parse(time.RFC3339, in.StartsAt)
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	ends, err := time.Parse(time.RFC3339, in.EndsAt)
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	window, err := r.d.Reliability.RegisterMaintenance(q.Context(), p, reliability.MaintenanceWindow{
		ChannelID: in.ChannelID,
		StartsAt:  starts,
		EndsAt:    ends,
		Note:      in.Note,
	})
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 201, window)
}
