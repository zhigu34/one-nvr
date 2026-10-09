package channel

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

func (s *SourceService) GetStatus(ctx context.Context, p auth.Principal, ch id.ID) (Status, error) {
	var out Status
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAnyChannelTx(ctx, p, ch, tx); err != nil {
			return err
		}
		var err error
		out, err = s.statusTx(ctx, tx, ch)
		return err
	})
	return out, err
}

// statusTx derives one channel's observed state inside the caller's
// transaction. It performs no authorization: callers must have established the
// principal's access already. Keeping the derivation in one place is why the
// batch summary cannot drift from the single-channel status.
func (s *SourceService) statusTx(ctx context.Context, tx pgx.Tx, ch id.ID) (Status, error) {
	out := Status{ChannelID: ch}
	var mode, subPath string
	if err := tx.QueryRow(ctx, `SELECT c.current_revision_id,c.desired_revision_id,c.storage_pool_id,c.enabled,c.version,p.mode,coalesce(r.sub_path,'') FROM channels c JOIN recording_policies p ON p.channel_id=c.id LEFT JOIN source_revisions r ON r.id=c.current_revision_id WHERE c.id=$1`, ch).Scan(&out.CurrentRevisionID, &out.DesiredRevisionID, &out.StoragePoolID, &out.Enabled, &out.Version, &mode, &subPath); err != nil {
		return out, err
	}
	if err := tx.QueryRow(ctx, "SELECT $2::uuid IS NULL AND NOT EXISTS(SELECT 1 FROM source_switches WHERE channel_id=$1 AND kind='apply' AND state='succeeded')", ch, out.CurrentRevisionID).Scan(&out.RequiresInitialRecordingMode); err != nil {
		return out, err
	}
	out.Main = ObservedStatus{State: "unknown", Reason: "observation_missing"}
	out.Sub = out.Main
	out.Recording = out.Main
	// One pass for all three kinds. The index is (channel_id,kind,observed_at
	// DESC,id DESC), so the first row seen per kind is the latest one, which is
	// exactly what the per-kind queries used to select.
	rows, err := tx.Query(ctx, `SELECT o.kind,o.state,o.reason_code,o.observed_at,o.expires_at FROM source_observations o LEFT JOIN stream_sessions ss ON ss.id=o.session_id WHERE o.channel_id=$1 AND o.source_revision_id IS NOT DISTINCT FROM $2 AND (o.session_id IS NULL OR ss.purpose=o.kind OR o.kind='recording') ORDER BY o.kind,o.observed_at DESC,o.id DESC`, ch, out.CurrentRevisionID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	now := time.Now().UTC()
	seen := map[string]bool{}
	for rows.Next() {
		var kind string
		var status ObservedStatus
		if err := rows.Scan(&kind, &status.State, &status.Reason, &status.ObservedAt, &status.ExpiresAt); err != nil {
			return out, err
		}
		if seen[kind] {
			continue
		}
		seen[kind] = true
		if !status.ExpiresAt.After(now) {
			status.State = "unknown"
			status.Reason = "observation_stale"
		}
		switch kind {
		case "main":
			out.Main = status
		case "sub":
			out.Sub = status
		case "recording":
			out.Recording = status
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if out.CurrentRevisionID == nil {
		out.Main = ObservedStatus{State: "not_configured", Reason: "source_not_configured"}
		out.Sub = out.Main
		out.Recording = ObservedStatus{State: "disabled", Reason: "source_not_configured"}
	}
	if subPath == "" {
		out.Sub = ObservedStatus{State: "not_configured", Reason: "sub_not_configured"}
	}
	if mode == "none" {
		out.Recording = ObservedStatus{State: "disabled", Reason: "recording_disabled"}
	}
	if !out.Enabled {
		out.Main = ObservedStatus{State: "disabled", Reason: "channel_disabled"}
		out.Sub = out.Main
		out.Recording = out.Main
	}
	return out, nil
}

// Summaries returns every channel the principal may see together with the
// status the list view renders. It replaces one status request per row: the
// derivation is the same statusTx, and authorization stays per channel via the
// same grant predicate the list uses.
func (s *SourceService) Summaries(ctx context.Context, p auth.Principal) (SummaryPage, error) {
	out := SummaryPage{Items: make([]SummaryItem, 0, 32)}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		u, err := s.Auth.CurrentUser(ctx, p)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT c.id,c.channel_no,c.channel_name,c.channel_group,c.enabled,c.version,g.live,g.playback,g.export,g.configure FROM channels c JOIN channel_grants g ON g.channel_id=c.id WHERE g.user_id=$1 AND (g.live OR g.playback OR g.export OR (g.configure AND $2<>'viewer')) ORDER BY c.channel_no`, p.UserID, u.Role)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item SummaryItem
			var live, playback, export, configure bool
			if err := rows.Scan(&item.ChannelID, &item.ChannelNo, &item.ChannelName, &item.ChannelGroup, &item.Enabled, &item.Version, &live, &playback, &export, &configure); err != nil {
				rows.Close()
				return err
			}
			item.Permissions = permissions(u.Role, live, playback, export, configure)
			out.Items = append(out.Items, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(out.Items) == 0 {
			return nil
		}
		index := make(map[id.ID]int, len(out.Items))
		for i, item := range out.Items {
			index[item.ChannelID] = i
		}
		for i := range out.Items {
			status, err := s.statusTx(ctx, tx, out.Items[i].ChannelID)
			if err != nil {
				return err
			}
			out.Items[i].CurrentRevisionID = status.CurrentRevisionID
			out.Items[i].Main = status.Main
			out.Items[i].Sub = status.Sub
			out.Items[i].Recording = status.Recording
			out.Items[i].LastError, out.Items[i].UpdatedAt = worstOf(status)
		}
		// Both enrichment passes join the grant table instead of taking an id
		// array, so no array encoding is involved and a channel outside the
		// caller's grants can never contribute a row.
		source, err := tx.Query(ctx, `SELECT c.id,host(r.ip),r.main_path,r.sub_path,r.onvif_port FROM channels c JOIN channel_grants g ON g.channel_id=c.id AND g.user_id=$1 JOIN source_revisions r ON r.id=c.current_revision_id`, p.UserID)
		if err != nil {
			return err
		}
		for source.Next() {
			var ch id.ID
			var ip, main, sub string
			var onvif *int
			if err := source.Scan(&ch, &ip, &main, &sub, &onvif); err != nil {
				source.Close()
				return err
			}
			if at, ok := index[ch]; ok {
				out.Items[at].SourceIP = ip
				out.Items[at].MainPath = main
				out.Items[at].SubPath = sub
				out.Items[at].OnvifPort = onvif
			}
		}
		if err := source.Err(); err != nil {
			source.Close()
			return err
		}
		source.Close()
		bitrate, err := tx.Query(ctx, `SELECT DISTINCT ON (b.channel_id) b.channel_id,b.bytes_per_second FROM recording_bitrate_samples b JOIN channel_grants g ON g.channel_id=b.channel_id AND g.user_id=$1 WHERE b.valid ORDER BY b.channel_id,b.observed_at DESC,b.id DESC`, p.UserID)
		if err != nil {
			return err
		}
		defer bitrate.Close()
		for bitrate.Next() {
			var ch id.ID
			var bytes int64
			if err := bitrate.Scan(&ch, &bytes); err != nil {
				return err
			}
			if at, ok := index[ch]; ok {
				kbps := bytes * 8 / 1000
				out.Items[at].BitrateKbps = &kbps
			}
		}
		if err := bitrate.Err(); err != nil {
			return err
		}
		// Media parameters describe the applied source, so only a successful
		// test of the applied revision qualifies. An older revision's
		// observation is never substituted, and a channel without such a test
		// reports nothing.
		media, err := tx.Query(ctx, `SELECT DISTINCT ON (t.channel_id) t.channel_id,t.result,t.observed_at FROM source_tests t JOIN channels c ON c.id=t.channel_id AND c.current_revision_id=t.revision_id JOIN channel_grants g ON g.channel_id=t.channel_id AND g.user_id=$1 WHERE t.purpose='source' AND t.state='succeeded' ORDER BY t.channel_id,t.created_at DESC,t.id DESC`, p.UserID)
		if err != nil {
			return err
		}
		defer media.Close()
		for media.Next() {
			var ch id.ID
			var raw []byte
			var observed *time.Time
			if err := media.Scan(&ch, &raw, &observed); err != nil {
				return err
			}
			at, ok := index[ch]
			if !ok {
				continue
			}
			var streams struct{ Main, Sub StreamTest }
			if err := json.Unmarshal(raw, &streams); err != nil {
				// A stored result that cannot be parsed contributes nothing;
				// it must not fail the whole list.
				continue
			}
			out.Items[at].MainMedia = mediaParams(streams.Main, observed)
			out.Items[at].SubMedia = mediaParams(streams.Sub, observed)
		}
		return media.Err()
	})
	return out, err
}

// mediaParams turns one stored stream result into the list's media view. A
// result without video parameters — for example an unavailable sub stream —
// contributes nil rather than an empty object.
func mediaParams(t StreamTest, observed *time.Time) *MediaParams {
	if t.Codec == "" && t.Width == 0 {
		return nil
	}
	return &MediaParams{Codec: t.Codec, Width: t.Width, Height: t.Height, FPS: t.FPS, AudioCodec: t.AudioCodec, ObservedAt: observed}
}

// worstOf reports the first real fault across the three kinds plus the newest
// observation time. States the operator chose — disabled, not_configured — are
// outcomes, not errors, and must not surface as one.
func worstOf(s Status) (string, *time.Time) {
	kinds := []ObservedStatus{s.Main, s.Sub, s.Recording}
	var latest *time.Time
	for _, o := range kinds {
		if o.ObservedAt != nil && (latest == nil || o.ObservedAt.After(*latest)) {
			latest = o.ObservedAt
		}
	}
	for _, o := range kinds {
		if o.State == "unavailable" || o.State == "degraded" {
			return o.Reason, latest
		}
	}
	return "", latest
}

type RecordingPolicy struct {
	ChannelID             id.ID  `json:"channel_id"`
	Mode                  string `json:"mode"`
	EventRecordingEnabled bool   `json:"event_recording_enabled"`
	Version               int64  `json:"version"`
}

func (s *SourceService) GetPolicy(ctx context.Context, p auth.Principal, ch id.ID) (RecordingPolicy, error) {
	out := RecordingPolicy{ChannelID: ch}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, ch, auth.Configure, tx); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT p.mode,c.version FROM recording_policies p JOIN channels c ON c.id=p.channel_id WHERE c.id=$1", ch).Scan(&out.Mode, &out.Version); err != nil {
			return err
		}
		if out.Mode != "none" && out.Mode != "continuous" {
			return auth.ErrConflict
		}
		return nil
	})
	return out, err
}
