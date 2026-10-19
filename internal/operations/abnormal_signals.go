package operations

// PRD EVT-04: where each signal comes from.
//
// Every source here already existed and already answered the question. Nothing in
// this file probes a camera or opens a connection: a signal that requires its own
// poller would double the load and drift from the data the pages already show.
// The rule is that an abnormal event must agree with what the operator sees
// elsewhere, which is only true if both read the same rows.

import (
	"context"
	"fmt"

	"github.com/zhigu34/one-nvr/internal/id"
)

// abnormalFindings reads every signal and returns the conditions that hold now.
//
// A read failure returns an error for the whole sweep rather than an empty list:
// an empty list is indistinguishable from "everything is fine", and writing
// recoveries for conditions we simply could not see would erase real incidents.
func (s *Service) abnormalFindings(ctx context.Context) ([]finding, []string, error) {
	var out []finding
	// unknown lists the subjects this sweep could not judge. They are excluded
	// from the recovery decision: a stale probe means the health is unknown, and
	// closing an incident because we stopped being able to see it would report a
	// recovery that never happened.
	var unknown []string
	components, err := s.componentStates(ctx)
	if err != nil {
		return nil, nil, err
	}
	componentOut, componentUnknown := componentFindings(components)
	out = append(out, componentOut...)
	unknown = append(unknown, componentUnknown...)
	channelFindings, err := s.channelFindings(ctx)
	if err != nil {
		return nil, nil, err
	}
	out = append(out, channelFindings...)
	storageFindings, err := s.storageFindings(ctx)
	if err != nil {
		return nil, nil, err
	}
	out = append(out, storageFindings...)
	backlog, err := s.backlogFinding(ctx)
	if err != nil {
		return nil, nil, err
	}
	if backlog != nil {
		out = append(out, *backlog)
	}
	return out, unknown, nil
}

// componentState is one probe result. Fresh records whether the observation is
// still valid; a stale observation is not evidence of health.
type componentState struct {
	State string
	Fresh bool
	Known bool
}

// componentStates reads the probe table for the components EVT-04 names.
func (s *Service) componentStates(ctx context.Context) (map[string]componentState, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT name,state,expires_at>clock_timestamp() FROM component_observations
		WHERE name IN ('frigate','mqtt','zlm')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]componentState{}
	for rows.Next() {
		var name string
		var state string
		var fresh bool
		if err := rows.Scan(&name, &state, &fresh); err != nil {
			return nil, err
		}
		out[name] = componentState{State: state, Fresh: fresh, Known: true}
	}
	return out, rows.Err()
}

// componentFindings turns probe results into incidents.
//
// Only the two components whose loss blinds the product are reported: the
// detector (no events, no snapshots, no detection) and the broker (no events
// arrive even though the detector is running). The media server is deliberately
// excluded — a channel whose stream is gone is already reported per channel by
// camera_offline, and reporting the media server as well would put two rows on
// one symptom.
func componentFindings(states map[string]componentState) ([]finding, []string) {
	var out []finding
	var unknown []string
	// A component that has never been probed is unknown, not healthy, but it is
	// also not evidence of an outage: the first sweep after a worker start would
	// otherwise open a detector incident that closes a second later.
	for _, entry := range []struct {
		name    string
		kind    string
		summary string
		detail  string
	}{
		{"frigate", KindDetectorDown, "检测服务不可用", "Frigate 无响应：事件、抓拍与检测全部停止"},
		{"mqtt", KindBrokerDown, "MQTT 消息代理不可用", "事件消息无法到达：检测仍在运行，但事件不会进入索引"},
	} {
		state, ok := states[entry.name]
		if !ok || !state.Known {
			continue
		}
		// A stale probe is not proof either way. The subject is reported as
		// unknown so the reconciliation leaves its current verdict alone: health
		// that was never observed is unknown, and closing an incident because the
		// probe went quiet would announce a recovery that never happened.
		if !state.Fresh {
			unknown = append(unknown, entry.name)
			continue
		}
		if state.State == "healthy" {
			continue
		}
		out = append(out, finding{
			Kind:     entry.kind,
			Severity: SeverityCritical,
			Subject:  entry.name,
			Summary:  entry.summary,
			Detail:   entry.detail,
		})
	}
	return out, unknown
}

// channelFindings reports per-camera conditions from the observation table.
//
// The queries read the same rows the channel pages read, so a camera shown as
// offline and a camera reported here cannot disagree.
func (s *Service) channelFindings(ctx context.Context) ([]finding, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT c.id,c.channel_no,c.channel_name,
			main.state,main.reason_code,main.expires_at>clock_timestamp(),
			coalesce(sub.state,''),coalesce(sub.expires_at>clock_timestamp(),false),
			coalesce(rec.state,''),coalesce(rec.reason_code,''),coalesce(rec.expires_at>clock_timestamp(),false)
		FROM channels c
		LEFT JOIN LATERAL (SELECT state,reason_code,expires_at FROM source_observations
			WHERE channel_id=c.id AND kind='main' ORDER BY observed_at DESC LIMIT 1) main ON true
		LEFT JOIN LATERAL (SELECT state,expires_at FROM source_observations
			WHERE channel_id=c.id AND kind='sub' ORDER BY observed_at DESC LIMIT 1) sub ON true
		LEFT JOIN LATERAL (SELECT state,reason_code,expires_at FROM source_observations
			WHERE channel_id=c.id AND kind='recording' ORDER BY observed_at DESC LIMIT 1) rec ON true
		WHERE c.enabled AND c.current_revision_id IS NOT NULL
		ORDER BY c.channel_no`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type row struct {
		channel             string
		number              int
		name, mainState     string
		mainReason          string
		mainFresh           bool
		subState            string
		subFresh, recFresh  bool
		recState, recReason string
	}
	var rowsOut []row
	for rows.Next() {
		var item row
		var mainFresh, subFresh, recFresh *bool
		var mainState, mainReason, subState, recState, recReason *string
		if err := rows.Scan(&item.channel, &item.number, &item.name,
			&mainState, &mainReason, &mainFresh, &subState, &subFresh, &recState, &recReason, &recFresh); err != nil {
			return nil, err
		}
		item.mainState = deref(mainState)
		item.mainReason = deref(mainReason)
		item.mainFresh = mainFresh != nil && *mainFresh
		item.subState = deref(subState)
		item.subFresh = subFresh != nil && *subFresh
		item.recState = deref(recState)
		item.recReason = deref(recReason)
		item.recFresh = recFresh != nil && *recFresh
		rowsOut = append(rowsOut, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []finding
	for _, item := range rowsOut {
		subject := item.channel
		label := fmt.Sprintf("CH%02d %s", item.number, item.name)
		channel := item.channel
		channelID := idOf(channel)
		// No observation at all means the sampler has not run yet, which is not
		// an outage. A stale observation is not evidence either.
		if !item.mainFresh {
			continue
		}
		switch item.mainState {
		case "unavailable":
			out = append(out, finding{
				Kind:     KindCameraOffline,
				Severity: SeverityWarning,
				Subject:  subject,
				Channel:  channelID,
				Summary:  label + " 码流不可用",
				Detail:   "来源无法拉流：" + item.mainReason,
			})
			// A camera that is offline explains its own recording stall; a second
			// row would report the same fault twice.
			continue
		case "degraded":
			out = append(out, finding{
				Kind:     KindStreamDegraded,
				Severity: SeverityWarning,
				Subject:  subject,
				Channel:  channelID,
				Summary:  label + " 码流异常",
				Detail:   item.mainReason,
			})
			continue
		}
		// Main is healthy: a sub stream that is configured and failing is its own
		// condition — the live tile falls back to main but the detection stream,
		// which reads sub, is gone.
		if item.subState == "unavailable" && item.subFresh {
			out = append(out, finding{
				Kind:     KindStreamDegraded,
				Severity: SeverityInfo,
				Subject:  subject + "/sub",
				Channel:  channelID,
				Summary:  label + " 子码流不可用",
				Detail:   "子码流无法拉流，检测与低带宽预览受影响",
			})
		}
		// Recording stalled: the stream is up but nothing is landing. This is the
		// condition EVT-04 calls out separately from the camera being down, and
		// the one that looks healthy everywhere else.
		if item.recFresh && item.recState == "unavailable" && item.recReason == "completion_stale" {
			out = append(out, finding{
				Kind:     KindRecordingStalled,
				Severity: SeverityWarning,
				Subject:  subject,
				Channel:  channelID,
				Summary:  label + " 录像停滞",
				Detail:   "码流在线但分片不再落盘",
			})
		}
	}
	return out, nil
}

// storageFindings reports pools that cannot be written or are running out.
//
// Reads storage_pool_checks, the same rows the storage page renders: a pool there
// shown as unavailable and a pool reported here are the same fact.
func (s *Service) storageFindings(ctx context.Context) ([]finding, error) {
	// The pool state is derived from its checks, not stored: storage_pools has no
	// state column. Reading the same conditions here is what keeps this page and
	// the storage page from disagreeing about one pool.
	rows, err := s.DB.Pool.Query(ctx, `SELECT p.id,p.name,
			EXISTS(SELECT 1 FROM storage_pool_checks WHERE pool_id=p.id AND state='unavailable' AND expires_at>clock_timestamp()),
			coalesce((SELECT reason_code FROM storage_pool_checks WHERE pool_id=p.id AND state='unavailable' AND expires_at>clock_timestamp() ORDER BY observed_at DESC LIMIT 1),''),
			EXISTS(SELECT 1 FROM storage_pool_checks WHERE pool_id=p.id AND service<>'zlm' AND state='healthy' AND expires_at>clock_timestamp() AND free_bytes>0 AND free_bytes<$1)
		FROM storage_pools p WHERE p.enabled`, storageMinimumFree)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type row struct {
		pool         string
		name         string
		writeFailing bool
		reason       string
		lowSpace     bool
	}
	var rowsOut []row
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.pool, &item.name, &item.writeFailing, &item.reason, &item.lowSpace); err != nil {
			return nil, err
		}
		rowsOut = append(rowsOut, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []finding
	for _, item := range rowsOut {
		switch {
		case item.writeFailing:
			out = append(out, finding{
				Kind:     KindStorageUnwritable,
				Severity: SeverityCritical,
				Subject:  item.pool,
				Summary:  "存储池 " + item.name + " 不可写",
				Detail:   "录像无法落盘：" + item.reason,
			})
		case item.lowSpace:
			// Capacity is a warning, not critical: recording pauses but existing
			// footage is intact and the pressure sweep may still reclaim space.
			out = append(out, finding{
				Kind:     KindCapacityLow,
				Severity: SeverityWarning,
				Subject:  item.pool,
				Summary:  "存储池 " + item.name + " 空间不足",
				Detail:   "剩余空间低于工作水位，新录像会被暂停",
			})
		}
	}
	return out, nil
}

// backlogFinding reports a detection queue that is falling behind.
//
// EVT-04 names "检测积压" (detection backlog). The signal used is jobs that have
// exhausted an attempt and are waiting on a retry: a single slow job is normal,
// but a queue with several retrying jobs means something downstream is refusing
// work rather than being briefly busy.
func (s *Service) backlogFinding(ctx context.Context) (*finding, error) {
	var retrying int
	if err := s.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE state='queued' AND attempt>0`).Scan(&retrying); err != nil {
		return nil, err
	}
	if retrying < abnormalBacklogThreshold {
		return nil, nil
	}
	return &finding{
		Kind:     KindQueueBacklog,
		Severity: SeverityWarning,
		Subject:  "jobs",
		Summary:  "任务队列积压",
		Detail:   fmt.Sprintf("%d 个任务正在重试，说明下游在拒绝工作而不是短暂繁忙", retrying),
	}, nil
}

// storageMinimumFree mirrors storage.MinimumFreeBytes. Duplicated rather than
// imported because operations must not depend on the storage package, and the
// number is a product decision (10 GB), not an implementation detail.
const storageMinimumFree int64 = 10 * 1024 * 1024 * 1024

// abnormalBacklogThreshold is how many retrying jobs constitute a backlog. Two is
// deliberately low: a site with a handful of channels should not normally have
// anything in retry, and a metric that only fires at fifty would never fire on
// this product's scale.
const abnormalBacklogThreshold = 3

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// idOf converts the scanned text form back to the id type.
func idOf(raw string) *id.ID {
	if raw == "" {
		return nil
	}
	value := id.ID(raw)
	return &value
}
