package site

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
)

type UpdateInput struct {
	Name     *string `json:"name"`
	Timezone *string `json:"timezone"`
	// RequireStorageWriteProof is a reliability preference: false accepts a
	// pool without a verified MP4 write and relies on the runtime gates.
	RequireStorageWriteProof *bool `json:"require_storage_write_proof"`
}

func (s *Service) Current(ctx context.Context, p auth.Principal) (Site, error) {
	var out Site
	if _, err := s.Auth.CurrentUser(ctx, p); err != nil {
		return out, err
	}
	err := s.DB.Pool.QueryRow(ctx, "SELECT id,name,timezone,channel_count,require_storage_write_proof,version FROM sites WHERE singleton").Scan(&out.ID, &out.Name, &out.Timezone, &out.ChannelCount, &out.RequireStorageWriteProof, &out.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, auth.ErrNotFound
	}
	return out, err
}

func (s *Service) Update(ctx context.Context, p auth.Principal, expected int64, in UpdateInput) (Site, error) {
	var out Site
	if expected < 1 ||
		(in.Name == nil && in.Timezone == nil && in.RequireStorageWriteProof == nil) {
		return out, auth.ErrInvalid
	}
	if in.Name != nil {
		v := strings.TrimSpace(*in.Name)
		if v == "" || len(v) > 128 || strings.ContainsAny(v, "\x00\r\n") {
			return out, auth.ErrInvalid
		}
		in.Name = &v
	}
	if in.Timezone != nil && !ValidateTimezone(*in.Timezone) {
		return out, auth.ErrInvalid
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, "UPDATE sites SET name=COALESCE($1,name),timezone=COALESCE($2,timezone),require_storage_write_proof=COALESCE($4,require_storage_write_proof),version=version+1 WHERE singleton AND version=$3 RETURNING id,name,timezone,channel_count,require_storage_write_proof,version", in.Name, in.Timezone, expected, in.RequireStorageWriteProof).Scan(&out.ID, &out.Name, &out.Timezone, &out.ChannelCount, &out.RequireStorageWriteProof, &out.Version)
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.ErrConflict
		}
		if err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "site.updated", ObjectID: out.ID})
	})
	return out, err
}

// Expand only appends slots 17–32; existing channel identities are immutable.
func (s *Service) Expand(ctx context.Context, p auth.Principal, expected int64) ([]channel.Channel, error) {
	out := make([]channel.Channel, 0, 16)
	if expected < 1 {
		return out, auth.ErrInvalid
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		var siteID id.ID
		var count int
		var version int64
		if err := tx.QueryRow(ctx, "SELECT id,channel_count,version FROM sites WHERE singleton FOR UPDATE").Scan(&siteID, &count, &version); errors.Is(err, pgx.ErrNoRows) {
			return auth.ErrNotFound
		} else if err != nil {
			return err
		}
		if count != 16 || version != expected {
			return auth.ErrConflict
		}
		for n := 17; n <= 32; n++ {
			cid, err := id.New()
			if err != nil {
				return err
			}
			name := fmt.Sprintf("通道 %02d", n)
			if _, err := tx.Exec(ctx, "INSERT INTO channels(id,site_id,channel_no,channel_name) VALUES($1,$2,$3,$4)", cid, siteID, n, name); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "INSERT INTO recording_policies(channel_id) VALUES($1)", cid); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "INSERT INTO channel_grants(user_id,channel_id,live,playback,export,configure) VALUES($1,$2,true,true,true,true)", p.UserID, cid); err != nil {
				return err
			}
			out = append(out, channel.Channel{ID: cid, ChannelNo: n, ChannelName: name, Version: 1, Permissions: []auth.Action{auth.Live, auth.Playback, auth.Export, auth.Configure}})
		}
		if _, err := tx.Exec(ctx, "UPDATE sites SET channel_count=32,version=version+1 WHERE id=$1", siteID); err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "site.expanded", ObjectID: siteID})
	})
	return out, err
}
