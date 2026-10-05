package channel

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

func (s *SourceService) GetStatus(ctx context.Context, p auth.Principal, ch id.ID) (Status, error) {
	var out Status
	out.ChannelID = ch
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAnyChannelTx(ctx, p, ch, tx); err != nil {
			return err
		}
		var mode, subPath string
		if err := tx.QueryRow(ctx, `SELECT c.current_revision_id,c.desired_revision_id,c.storage_pool_id,c.enabled,c.version,p.mode,coalesce(r.sub_path,'') FROM channels c JOIN recording_policies p ON p.channel_id=c.id LEFT JOIN source_revisions r ON r.id=c.current_revision_id WHERE c.id=$1`, ch).Scan(&out.CurrentRevisionID, &out.DesiredRevisionID, &out.StoragePoolID, &out.Enabled, &out.Version, &mode, &subPath); err != nil {
			return err
		}
		observed := func(kind string) (ObservedStatus, error) {
			status := ObservedStatus{State: "unknown", Reason: "observation_missing"}
			err := tx.QueryRow(ctx, `SELECT o.state,o.reason_code,o.observed_at,o.expires_at FROM source_observations o LEFT JOIN stream_sessions ss ON ss.id=o.session_id WHERE o.channel_id=$1 AND o.source_revision_id IS NOT DISTINCT FROM $2 AND o.kind=$3 AND (o.session_id IS NULL OR ss.purpose=$3 OR $3='recording') ORDER BY o.observed_at DESC,o.id DESC LIMIT 1`, ch, out.CurrentRevisionID, kind).Scan(&status.State, &status.Reason, &status.ObservedAt, &status.ExpiresAt)
			if errors.Is(err, pgx.ErrNoRows) {
				return status, nil
			}
			if err != nil {
				return status, err
			}
			if !status.ExpiresAt.After(time.Now().UTC()) {
				status.State = "unknown"
				status.Reason = "observation_stale"
			}
			return status, nil
		}
		var err error
		out.Main, err = observed("main")
		if err != nil {
			return err
		}
		out.Sub, err = observed("sub")
		if err != nil {
			return err
		}
		out.Recording, err = observed("recording")
		if err != nil {
			return err
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
		return nil
	})
	return out, err
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
