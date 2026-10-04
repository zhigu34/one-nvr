package auth

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/id"
)

type Action string

const (
	Live      Action = "live"
	Playback  Action = "playback"
	Export    Action = "export"
	Configure Action = "configure"
)

type Grant struct {
	ChannelID id.ID    `json:"channel_id"`
	Actions   []Action `json:"actions"`
}

func validAction(a Action) bool { return a == Live || a == Playback || a == Export || a == Configure }
func (s *Service) RequireChannel(ctx context.Context, p Principal, channelID id.ID, action Action) error {
	return s.requireChannel(ctx, p, channelID, action, s.DB.Pool)
}

// RequireChannelTx serializes a channel mutation with session/grant revocation.
func (s *Service) RequireChannelTx(ctx context.Context, p Principal, channelID id.ID, action Action, tx pgx.Tx) error {
	if err := LockAuthorization(ctx, tx); err != nil {
		return err
	}
	return s.requireChannel(ctx, p, channelID, action, tx)
}

func (s *Service) requireChannel(ctx context.Context, p Principal, channelID id.ID, action Action, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) error {
	if !validAction(action) {
		return ErrInvalid
	}
	user, err := s.current(ctx, p, q)
	if err != nil {
		return err
	}
	var live, playback, export, configure bool
	err = q.QueryRow(ctx, "SELECT live,playback,export,configure FROM channel_grants WHERE user_id=$1 AND channel_id=$2", p.UserID, channelID).Scan(&live, &playback, &export, &configure)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !live && !playback && !export && !configure {
		return ErrNotFound
	}
	if !roleAllows(user.Role, action) || !map[Action]bool{Live: live, Playback: playback, Export: export, Configure: configure}[action] {
		return ErrForbidden
	}
	return nil
}
func (s *Service) GetGrants(ctx context.Context, p Principal, userID id.ID) ([]Grant, error) {
	if err := s.RequireAdmin(ctx, p); err != nil {
		return nil, err
	}
	var exists bool
	if err := s.DB.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)", userID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := s.DB.Pool.Query(ctx, "SELECT channel_id,live,playback,export,configure FROM channel_grants WHERE user_id=$1 ORDER BY channel_id", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grants := make([]Grant, 0)
	for rows.Next() {
		var g Grant
		var live, playback, export, configure bool
		if err := rows.Scan(&g.ChannelID, &live, &playback, &export, &configure); err != nil {
			return nil, err
		}
		g.Actions = make([]Action, 0)
		for _, pair := range []struct {
			a  Action
			ok bool
		}{{Live, live}, {Playback, playback}, {Export, export}, {Configure, configure}} {
			if pair.ok {
				g.Actions = append(g.Actions, pair.a)
			}
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}
func (s *Service) SetGrants(ctx context.Context, p Principal, userID id.ID, expected int64, grants []Grant) error {
	if len(grants) > 32 {
		return ErrInvalid
	}
	seen := map[id.ID]bool{}
	for _, g := range grants {
		if seen[g.ChannelID] || len(g.Actions) > 4 {
			return ErrInvalid
		}
		seen[g.ChannelID] = true
		actions := map[Action]bool{}
		for _, a := range g.Actions {
			if !validAction(a) || actions[a] {
				return ErrInvalid
			}
			actions[a] = true
		}
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(170018)"); err != nil {
			return err
		}
		if err := s.adminTx(ctx, p, tx); err != nil {
			return err
		}
		var version int64
		var role string
		if err := tx.QueryRow(ctx, "SELECT version,role FROM users WHERE id=$1 FOR UPDATE", userID).Scan(&version, &role); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if version != expected {
			return ErrConflict
		}
		for _, g := range grants {
			for _, a := range g.Actions {
				if !roleAllows(role, a) {
					return ErrInvalid
				}
			}
		}
		for _, g := range grants {
			var exists bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM channels WHERE id=$1)", g.ChannelID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrNotFound
			}
		}
		if _, err := tx.Exec(ctx, "DELETE FROM channel_grants WHERE user_id=$1", userID); err != nil {
			return err
		}
		for _, g := range grants {
			m := map[Action]bool{}
			for _, a := range g.Actions {
				m[a] = true
			}
			if _, err := tx.Exec(ctx, "INSERT INTO channel_grants(user_id,channel_id,live,playback,export,configure) VALUES($1,$2,$3,$4,$5,$6)", userID, g.ChannelID, m[Live], m[Playback], m[Export], m[Configure]); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE users SET version=version+1,auth_version=auth_version+1 WHERE id=$1", userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=$1 AND revoked_at IS NULL", userID); err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "user.grants_updated", ObjectID: userID})
	})
}

func roleAllows(role string, a Action) bool {
	return validRole(role) && validAction(a) && !(role == "viewer" && a == Configure)
}
