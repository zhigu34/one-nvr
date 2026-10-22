// Package capability separates deployment intent from delivered functionality.
//
// The distinction exists so an enabled module is never advertised as working
// merely because a switch is on: `enabled` is the operator's intent, `state`
// is what this version can actually do right now, and `reason` says why. The
// PRD is explicit that a configured-but-unhealthy dependency must read as
// broken, not as available.
package capability

import (
	"context"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/operations"
)

type Capability struct {
	Enabled bool `json:"enabled"`
	// Implemented is the version scope: whether this build carries the module's
	// business logic at all. It is a property of the release, not of the
	// deployment, so an enabled module with a broken dependency reports
	// Implemented=true and State=unavailable — the two must not be conflated
	// (that conflation is what made a shipped module read as "尚未实现").
	Implemented bool   `json:"implemented"`
	State       string `json:"state"`
	Reason      string `json:"reason"`
}
type Snapshot struct {
	Intelligence Capability `json:"frigate"`
	Archive      Capability `json:"cloud_archive"`
}
type Service struct {
	Operations *operations.Service
	// FrigateEnabled is the one deployment switch that removes a capability:
	// detection and events are built on Frigate and MQTT, so without them the
	// feature does not exist. Cloud archive has no such switch (see archive()).
	FrigateEnabled bool
}

// The four states an enabled module can be in, plus disabled. They map to the
// PRD's operator-facing wording: 功能未启用 / 引导配置 / 运行异常 / 版本尚未提供.
const (
	StateDisabled      = "disabled"
	StateNotConfigured = "not_configured"
	StateHealthy       = "healthy"
	StateUnavailable   = "unavailable"
	StateNotAvailable  = "not_available"
	// StateUnverified is "configured but never tested". It is deliberately not
	// StateUnavailable: reporting an untested target as 运行异常 says it is broken,
	// and the operator goes looking for a fault that has not happened. Measured on
	// this site: 5 verified copies were being uploaded through a target whose card
	// read 运行异常, because nobody had ever pressed 测试.
	StateUnverified = "unverified"
)

// Snapshot reports both optional modules as this build and this deployment
// actually stand. Intelligence and archive are both delivered: their modules
// carry real business logic, so a deployment that has not configured them is
// told to configure them rather than told the feature does not exist.
func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	intelligence, err := s.intelligence(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	archive, err := s.archive(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Intelligence: intelligence, Archive: archive}, nil
}

// intelligence derives the detector module's state from whether any channel
// asks for detection and from the observed health of its dependencies. Frigate
// and MQTT are the two halves of the pipeline: without the broker there are no
// events, so either one being down is a module-wide fault.
func (s *Service) intelligence(ctx context.Context) (Capability, error) {
	out := Capability{Enabled: s.FrigateEnabled, Implemented: true}
	if !s.FrigateEnabled {
		out.State, out.Reason = StateDisabled, "feature_disabled"
		return out, nil
	}
	var configured bool
	if err := s.Operations.DB.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM detection_configs WHERE enabled)`).Scan(&configured); err != nil {
		return out, err
	}
	if !configured {
		out.State, out.Reason = StateNotConfigured, "detection_not_configured"
		return out, nil
	}
	components, err := s.Operations.Snapshot(ctx)
	if err != nil {
		return out, err
	}
	if faulty, ok := unhealthy(components, "frigate", "mqtt"); ok {
		out.State, out.Reason = StateUnavailable, faulty
		return out, nil
	}
	out.State, out.Reason = StateHealthy, "probe_succeeded"
	return out, nil
}

// archive derives the cloud archive module's state from its registered targets.
//
// It deliberately does not consult OpenListEnabled. That switch is a *module*
// switch in the deployment sense — whether the bundled OpenList container ships
// and is probed — not a statement about whether cloud archiving exists.
// Archiving's substrate is whichever WebDAV target the operator mounted; the
// bundled service is only the convenient default among them, and a site
// reaching its own NAS over WebDAV needs none of it. Tying the feature to that
// switch would report "not enabled" for a capability that is working.
//
// What the module actually needs is a target that answers. A single unhealthy
// target does not make it unavailable either: PRD §14.5 requires the other
// WebDAV endpoints to keep working while one is down, so one reachable target
// is enough. The per-target detail lives in the target list, where it belongs.
func (s *Service) archive(ctx context.Context) (Capability, error) {
	out := Capability{Enabled: true, Implemented: true}
	var total, healthy int
	if err := s.Operations.DB.Pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE last_check->>'state'='healthy') FROM archive_targets WHERE enabled`).Scan(&total, &healthy); err != nil {
		return out, err
	}
	switch {
	case total == 0:
		out.State, out.Reason = StateNotConfigured, "archive_target_missing"
	case healthy > 0:
		out.State, out.Reason = StateHealthy, "target_probe_succeeded"
	default:
		// Nobody has a healthy verdict. Before calling that an outage, ask whether
		// anyone ever measured it: a target saved and never tested has an empty
		// last_check, and "we have not looked" is not "it is broken". Uploads may
		// well be succeeding — they were on this site.
		var tested int
		if err := s.Operations.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM archive_targets WHERE enabled AND last_check IS NOT NULL`).Scan(&tested); err != nil {
			return out, err
		}
		if tested == 0 {
			out.State, out.Reason = StateUnverified, "archive_target_untested"
		} else {
			out.State, out.Reason = StateUnavailable, "archive_target_unreachable"
		}
	}
	return out, nil
}

// unhealthy reports the first dependency that is not healthy, with the reason
// the sampler recorded. An unobserved or expired dependency counts as unhealthy:
// "we have not managed to prove this works" is not "this works".
func unhealthy(components []operations.Component, names ...string) (string, bool) {
	index := make(map[string]operations.Component, len(components))
	for _, component := range components {
		index[component.Name] = component
	}
	for _, name := range names {
		component, ok := index[name]
		if !ok || component.State != "healthy" {
			reason := "dependency_not_observed"
			if ok && component.Reason != "" {
				reason = component.Reason
			}
			return reason, true
		}
	}
	return "", false
}

// Require gates a module's actions on the deployment switch.
//
// Only intelligence has such a switch that actually removes the capability:
// detection and events are built on Frigate and MQTT, so with those absent the
// feature does not exist and its endpoints answer accordingly. Cloud archive has
// no equivalent: it works against any WebDAV target, so nothing about the
// deployment disables it and this always permits. The method is kept (and kept
// honest) rather than deleted because the placeholder endpoints still ask it,
// and because a future module may genuinely need the distinction.
func (s *Service) Require(feature string) error {
	switch feature {
	case "intelligence":
		if !s.FrigateEnabled {
			return fault.New(409, "feature_disabled", "该功能未启用")
		}
		return nil
	case "archive":
		return nil
	default:
		return auth.ErrInvalid
	}
}
