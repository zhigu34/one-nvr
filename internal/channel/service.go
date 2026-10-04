// Package channel manages permanent numbered slots independently of sources.
package channel

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
)

type Channel struct {
	ID          id.ID         `json:"id"`
	ChannelNo   int           `json:"channel_no"`
	ChannelName string        `json:"channel_name"`
	Version     int64         `json:"version"`
	Permissions []auth.Action `json:"permissions"`
}
type Page struct {
	Items      []Channel `json:"items"`
	NextCursor *id.ID    `json:"next_cursor"`
}
type UpdateInput struct {
	ChannelName string `json:"channel_name"`
}
type Service struct {
	DB   *database.DB
	Auth *auth.Service
}

func permissions(role string, live, playback, export, configure bool) []auth.Action {
	out := make([]auth.Action, 0, 4)
	for _, p := range []struct {
		a  auth.Action
		ok bool
	}{{auth.Live, live}, {auth.Playback, playback}, {auth.Export, export}, {auth.Configure, configure && role != "viewer"}} {
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
	rows, err := s.DB.Pool.Query(ctx, `SELECT c.id,c.channel_no,c.channel_name,c.version,g.live,g.playback,g.export,g.configure FROM channels c JOIN channel_grants g ON g.channel_id=c.id WHERE g.user_id=$1 AND c.channel_no>$2 AND (g.live OR g.playback OR g.export OR (g.configure AND $3<>'viewer')) ORDER BY c.channel_no LIMIT $4`, p.UserID, from, u.Role, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Channel
		var live, playback, export, configure bool
		if err := rows.Scan(&c.ID, &c.ChannelNo, &c.ChannelName, &c.Version, &live, &playback, &export, &configure); err != nil {
			return out, err
		}
		c.Permissions = permissions(u.Role, live, playback, export, configure)
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

func (s *Service) Update(ctx context.Context, p auth.Principal, channelID id.ID, expected int64, in UpdateInput) (Channel, error) {
	var out Channel
	name := strings.TrimSpace(in.ChannelName)
	if name == "" || len(name) > 128 || strings.ContainsAny(name, "\x00\r\n") || expected < 1 {
		return out, auth.ErrInvalid
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Configure, tx); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `UPDATE channels SET channel_name=$2,version=version+1 WHERE id=$1 AND version=$3 RETURNING id,channel_no,channel_name,version`, channelID, name, expected).Scan(&out.ID, &out.ChannelNo, &out.ChannelName, &out.Version)
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.ErrConflict
		}
		if err != nil {
			return err
		}
		var role string
		var live, playback, export, configure bool
		if err := tx.QueryRow(ctx, `SELECT u.role,g.live,g.playback,g.export,g.configure FROM channel_grants g JOIN users u ON u.id=g.user_id WHERE g.user_id=$1 AND g.channel_id=$2`, p.UserID, channelID).Scan(&role, &live, &playback, &export, &configure); err != nil {
			return err
		}
		out.Permissions = permissions(role, live, playback, export, configure)
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "channel.renamed", ObjectID: channelID})
	})
	return out, err
}
