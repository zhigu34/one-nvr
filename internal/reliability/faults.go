package reliability

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
)

// Fault categories. PRD REL-02 enumerates exactly these eight, and the list is
// closed: a reason this package does not recognise becomes "unknown" rather
// than inventing a ninth category that no requirement describes.
const (
	CategoryUpstream    = "upstream_unavailable"
	CategoryAuthConfig  = "auth_or_config"
	CategoryRecording   = "recording_service"
	CategoryStorage     = "storage_directory"
	CategoryDetection   = "detection_service"
	CategoryMonitoring  = "monitoring_unknown"
	CategoryMaintenance = "maintenance_or_switch"
	CategoryUnknown     = "unknown"
)

// categoryFor maps an observation reason to its category.
//
// The mapping is deliberately conservative in one specific way: a timeout or a
// dead stream proves "the source or its link is unavailable" and nothing more.
// PRD REL-02 forbids reading that as a broken camera, so upstream reasons stay
// in CategoryUpstream and are never refined into hardware conclusions here.
// Anything that would require evidence we do not have is left to the operator,
// who can confirm a cause without rewriting the automatic one.
func categoryFor(kind, reason string) string {
	switch reason {
	// The source or its path to us.
	case "source_unavailable", "sub_source_unavailable", "source_observation_failed",
		"source_observation_unknown", "source_recovery_failed", "source_rollback_failed",
		"sub_source_unavailable_rollback":
		return CategoryUpstream
	// Credentials and configuration: things an operator changes in a form.
	case "source_test_failed", "source_test_expired", "source_config_invalid",
		"source_not_configured", "sub_not_configured", "zlm_write_evidence_unavailable",
		"credentials_rejected", "authentication_failed":
		return CategoryAuthConfig
	// The recorder or the media server itself.
	case "recording_output_stalled", "recording_completion_unavailable",
		"recording_publication_unavailable", "recording_publication_conflict",
		"recording_not_ready", "recording_change_failed", "first_completion_pending":
		return CategoryRecording
	// The pool the bytes land in.
	case "pool_unavailable", "pool_disabled", "pool_unwritable", "pool_unknown",
		"pool_observation_stale", "pool_probe_failed", "capacity_unknown",
		"bitrate_unknown", "low_space":
		return CategoryStorage
	// Frigate or the broker.
	case "frigate_unavailable", "mqtt_unavailable", "detection_not_configured",
		"detection_publish_failed":
		return CategoryDetection
	// Our own evidence gap.
	case "monitoring_gap", "observation_stale", "observation_missing":
		return CategoryMonitoring
	// A source switch, whether planned or not.
	case "source_switch", "pool_switch", "source_change_failed":
		return CategoryMaintenance
	}
	// A recording kind with no more specific reason is still the recorder.
	if kind == "recording" {
		return CategoryRecording
	}
	if kind == "storage" || kind == "pool" {
		return CategoryStorage
	}
	return CategoryUnknown
}

// CategoryFor exposes the mapping for the API and for tests, so the vocabulary
// lives in one place.
func CategoryFor(kind, reason string) string { return categoryFor(kind, reason) }

// Categories lists the attribution vocabulary this build classifies into. The
// API reports it so a client renders the categories that exist rather than a
// copy that can drift.
func Categories() []string {
	return []string{
		CategoryUpstream, CategoryAuthConfig, CategoryRecording, CategoryStorage,
		CategoryDetection, CategoryMonitoring, CategoryMaintenance, CategoryUnknown,
	}
}

// stateTransition decides whether a state change deserves a fault.
//
// Not every transition is a fault: a channel being configured for the first
// time, or a deliberate stop, is not something that went wrong. Returning false
// keeps those out of the fault table entirely rather than recording them and
// filtering them in every reader.
func stateTransition(previous *Interval, next Observation) (fault bool, resolved bool) {
	if next.Exclusion != "" {
		return false, false
	}
	switch next.State {
	case "unavailable", "unknown":
		// A gap in our own evidence is a monitoring fault, not a device one,
		// but it is still something the operator must see.
		if next.Reason == "monitoring_gap" || next.Reason == "observation_stale" {
			return true, false
		}
		// Going unavailable is a fault. Going merely unknown is not: "we could
		// not tell" is not the same as "it was down", and recording it as a
		// fault would invent an outage out of an observation gap.
		if next.State == "unavailable" {
			return true, false
		}
		return false, false
	case "healthy":
		// Recovery. Only worth closing a fault if one was open; the caller
		// checks that.
		return false, true
	}
	return false, false
}

// RecordFaults folds one observation into the fault history.
//
// It runs in the same transaction as the interval fold, so a fault and the
// interval change that caused it commit together: a crash cannot leave an
// outage with no interval behind it, or a failed interval with no fault.
func (s *Service) recordFaultsTx(ctx context.Context, tx pgx.Tx, in Observation, previous *Interval) error {
	// Recovery first: closing an open fault is how a healthy sample is
	// interpreted, and it must happen even if no new fault opens.
	if in.State == "healthy" && in.Exclusion == "" {
		if err := s.resolveFaultsTx(ctx, tx, in); err != nil {
			return err
		}
	}
	fault, _ := stateTransition(previous, in)
	if !fault {
		return nil
	}

	category := categoryFor(in.Kind, in.Reason)
	maintenance := false
	if category == CategoryMaintenance {
		maintenance = true
	}
	faultKind := faultKindFor(in.Kind)
	evidence, _ := json.Marshal(map[string]any{
		"state":           in.State,
		"reason":          in.Reason,
		"observed_at":     in.ObservedAt.UTC().Format(time.RFC3339),
		"kind":            in.Kind,
		"previous_state":  stateOf(previous),
		"previous_reason": reasonOf(previous),
	})

	// One open fault per channel and kind. A repeated observation of the same
	// failure refreshes the existing row — PRD REL-02 requires the same
	// continuous fault to update the original event, and repeated callbacks to
	// be idempotent. The upsert is the fence: a second worker racing here
	// updates the winner's row instead of filing a duplicate.
	_, err := tx.Exec(ctx, `INSERT INTO fault_events
			(id,channel_id,source_revision_id,session_id,kind,state,started_at,initial_reason,category,evidence,maintenance,revisions)
		VALUES($1,$2,$3,$4,$5,'open',$6,$7,$8,$9,$10,'[]')
		ON CONFLICT (channel_id,kind) WHERE state='open' DO UPDATE SET
			evidence=EXCLUDED.evidence,updated_at=clock_timestamp()`,
		mustID(), in.ChannelID, in.SourceRevisionID, in.SessionID, faultKind,
		in.ObservedAt.UTC(), in.Reason, category, evidence, maintenance)
	return err
}

func (s *Service) resolveFaultsTx(ctx context.Context, tx pgx.Tx, in Observation) error {
	faultKind := faultKindFor(in.Kind)
	// Everything that was open for this channel and kind is over: the stream is
	// producing video again. The duration is measured from the fault's own start
	// to this confirming observation.
	_, err := tx.Exec(ctx, `UPDATE fault_events SET
			state='resolved',ended_at=$3,
			duration_ms=GREATEST(0,EXTRACT(EPOCH FROM ($3-started_at))*1000)::bigint,
			recovery='auto',updated_at=clock_timestamp()
		WHERE channel_id=$1 AND kind=$2 AND state='open'`,
		in.ChannelID, faultKind, in.ObservedAt.UTC())
	return err
}

func faultKindFor(kind string) string {
	switch kind {
	case "main", "sub", "recording", "storage", "media_server", "monitoring", "switch":
		return kind
	default:
		return "main"
	}
}

func stateOf(i *Interval) string {
	if i == nil {
		return ""
	}
	return i.State
}

func reasonOf(i *Interval) string {
	if i == nil {
		return ""
	}
	return i.Reason
}

func mustID() id.ID {
	i, err := id.New()
	if err != nil {
		panic(err)
	}
	return i
}
