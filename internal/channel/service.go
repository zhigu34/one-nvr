// Package channel manages permanent numbered slots independently of sources.
package channel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
)

type Channel struct {
	ID           id.ID         `json:"id"`
	ChannelNo    int           `json:"channel_no"`
	ChannelName  string        `json:"channel_name"`
	ChannelGroup string        `json:"channel_group"`
	Enabled      bool          `json:"enabled"`
	Version      int64         `json:"version"`
	Permissions  []auth.Action `json:"permissions"`
	// RecordingQuotaBytes 0 means no per-channel quota.
	RecordingQuotaBytes int64 `json:"recording_quota_bytes"`
}
type Page struct {
	Items      []Channel `json:"items"`
	NextCursor *id.ID    `json:"next_cursor"`
}

// UpdateInput carries only the business attributes of a channel. Pointers
// separate "leave unchanged" from "set to the zero value", so disabling a
// channel or clearing its group is expressible without a second endpoint.
// Media identity (source revisions, pool, policy) is never touched here:
// renaming or regrouping must not rebuild the stream (PRD CH-01/CH-03).
type UpdateInput struct {
	ChannelName  *string `json:"channel_name,omitempty"`
	ChannelGroup *string `json:"channel_group,omitempty"`
	Enabled      *bool   `json:"enabled,omitempty"`
	// RecordingQuotaBytes caps this channel's indexed recordings. Zero means no
	// quota. It is a housekeeping limit, not a filesystem quota: pressure
	// cleanup on the whole volume is a separate policy (STO-02 vs STO-03).
	RecordingQuotaBytes *int64 `json:"recording_quota_bytes,omitempty"`
}

// Slot contains only grant-management metadata, never video permissions or URLs.
type Slot struct {
	ID          id.ID  `json:"id"`
	ChannelNo   int    `json:"channel_no"`
	ChannelName string `json:"channel_name"`
}

func (s *Service) Slots(ctx context.Context, p auth.Principal) ([]Slot, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return nil, err
	}
	rows, err := s.DB.Pool.Query(ctx, "SELECT c.id,c.channel_no,c.channel_name FROM channels c JOIN sites s ON s.id=c.site_id WHERE s.singleton ORDER BY c.channel_no")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Slot, 0, 32)
	for rows.Next() {
		var slot Slot
		if err = rows.Scan(&slot.ID, &slot.ChannelNo, &slot.ChannelName); err != nil {
			return nil, err
		}
		out = append(out, slot)
	}
	return out, rows.Err()
}

type Service struct {
	DB   *database.DB
	Auth *auth.Service
}

func permissions(role string, live, playback, export, configure, archive bool) []auth.Action {
	out := make([]auth.Action, 0, 5)
	for _, p := range []struct {
		a  auth.Action
		ok bool
	}{{auth.Live, live}, {auth.Playback, playback}, {auth.Export, export}, {auth.Configure, configure && role != "viewer"}, {auth.Archive, archive && role != "viewer"}} {
		if p.ok {
			out = append(out, p.a)
		}
	}
	return out
}

func (s *Service) List(ctx context.Context, p auth.Principal, cursor id.ID, limit int) (Page, error) {
	out := Page{Items: make([]Channel, 0)}
	if limit < 1 || limit > 100 {
		return out, auth.ErrInvalid
	}
	u, err := s.Auth.CurrentUser(ctx, p)
	if err != nil {
		return out, err
	}
	from := 0
	if cursor != "" {
		err = s.DB.Pool.QueryRow(ctx, `SELECT c.channel_no FROM channels c JOIN channel_grants g ON g.channel_id=c.id WHERE c.id=$1 AND g.user_id=$2 AND (g.live OR g.playback OR g.export OR (g.configure AND $3<>'viewer'))`, cursor, p.UserID, u.Role).Scan(&from)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, auth.ErrNotFound
		}
		if err != nil {
			return out, err
		}
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT c.id,c.channel_no,c.channel_name,c.channel_group,c.enabled,c.version,c.recording_quota_bytes,g.live,g.playback,g.export,g.configure,g.archive FROM channels c JOIN channel_grants g ON g.channel_id=c.id WHERE g.user_id=$1 AND c.channel_no>$2 AND (g.live OR g.playback OR g.export OR g.archive OR (g.configure AND $3<>'viewer')) ORDER BY c.channel_no LIMIT $4`, p.UserID, from, u.Role, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Channel
		var live, playback, export, configure, archive bool
		if err := rows.Scan(&c.ID, &c.ChannelNo, &c.ChannelName, &c.ChannelGroup, &c.Enabled, &c.Version, &c.RecordingQuotaBytes, &live, &playback, &export, &configure, &archive); err != nil {
			return out, err
		}
		c.Permissions = permissions(u.Role, live, playback, export, configure, archive)
		out.Items = append(out.Items, c)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		last := out.Items[limit-1].ID
		out.NextCursor = &last
		out.Items = out.Items[:limit]
	}
	return out, nil
}

// Update applies the business attributes present in the input. Enabling and
// disabling a channel is deliberately independent of clearing its source and of
// turning recording off (PRD CH-04): this only flips a flag the recording
// scheduler, live authorization and status derivation already honour, so it
// stops fetching and recording without discarding the source or its history.
func (s *Service) Update(ctx context.Context, p auth.Principal, channelID id.ID, expected int64, in UpdateInput) (Channel, error) {
	var out Channel
	if expected < 1 || in.ChannelName == nil && in.ChannelGroup == nil && in.Enabled == nil && in.RecordingQuotaBytes == nil {
		return out, auth.ErrInvalid
	}
	if in.RecordingQuotaBytes != nil && (*in.RecordingQuotaBytes < 0 || *in.RecordingQuotaBytes > 1<<50) {
		// 1 PiB is far beyond any real deployment and keeps the value inside
		// what the byte arithmetic downstream can multiply without overflow.
		return out, auth.ErrInvalid
	}
	var name, group *string
	if in.ChannelName != nil {
		value := strings.TrimSpace(*in.ChannelName)
		if value == "" || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return out, auth.ErrInvalid
		}
		name = &value
	}
	if in.ChannelGroup != nil {
		value := strings.TrimSpace(*in.ChannelGroup)
		if len(value) > 64 || strings.ContainsAny(value, "\x00\r\n") {
			return out, auth.ErrInvalid
		}
		group = &value
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Configure, tx); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `UPDATE channels SET channel_name=coalesce($2::text,channel_name),channel_group=coalesce($3::text,channel_group),enabled=coalesce($4::boolean,enabled),recording_quota_bytes=coalesce($6::bigint,recording_quota_bytes),version=version+1 WHERE id=$1 AND version=$5 RETURNING id,channel_no,channel_name,channel_group,enabled,version,recording_quota_bytes`, channelID, name, group, in.Enabled, expected, in.RecordingQuotaBytes).Scan(&out.ID, &out.ChannelNo, &out.ChannelName, &out.ChannelGroup, &out.Enabled, &out.Version, &out.RecordingQuotaBytes)
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.ErrConflict
		}
		if err != nil {
			return err
		}
		var role string
		var live, playback, export, configure, archive bool
		if err := tx.QueryRow(ctx, `SELECT u.role,g.live,g.playback,g.export,g.configure,g.archive FROM channel_grants g JOIN users u ON u.id=g.user_id WHERE g.user_id=$1 AND g.channel_id=$2`, p.UserID, channelID).Scan(&role, &live, &playback, &export, &configure, &archive); err != nil {
			return err
		}
		out.Permissions = permissions(role, live, playback, export, configure, archive)
		// One entry per attribute actually changed, so the log shows what moved
		// rather than a single opaque "updated" row.
		if name != nil {
			if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "channel.renamed", ObjectID: channelID}); err != nil {
				return err
			}
		}
		if group != nil {
			details, err := json.Marshal(map[string]string{"channel_group": *group})
			if err != nil {
				return err
			}
			if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "channel.regrouped", ObjectID: channelID, Details: details}); err != nil {
				return err
			}
		}
		if in.Enabled != nil {
			action := "channel.disabled"
			if *in.Enabled {
				action = "channel.enabled"
			}
			if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: action, ObjectID: channelID}); err != nil {
				return err
			}
		}
		if in.RecordingQuotaBytes != nil {
			details, err := json.Marshal(map[string]int64{"recording_quota_bytes": *in.RecordingQuotaBytes})
			if err != nil {
				return err
			}
			if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "channel.quota_changed", ObjectID: channelID, Details: details}); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}
