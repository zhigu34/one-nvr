package auth

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
)

type CreateUserInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}
type UpdateUserInput struct {
	Role    *string `json:"role,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
}

func (s *Service) CreateUser(ctx context.Context, p Principal, in CreateUserInput) (User, error) {
	var user User
	if err := s.RequireAdmin(ctx, p); err != nil {
		return user, err
	}
	if !ValidateUsername(in.Username) || !validRole(in.Role) || ValidatePassword(in.Password) != nil {
		return user, ErrInvalid
	}
	hash, err := s.Passwords.Hash(ctx, in.Password)
	if err != nil {
		return user, err
	}
	userID, err := id.New()
	if err != nil {
		return user, err
	}
	err = s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.adminTx(ctx, p, tx); err != nil {
			return err
		}
		var siteID id.ID
		if err := tx.QueryRow(ctx, "SELECT site_id FROM users WHERE id=$1", p.UserID).Scan(&siteID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO users(id,site_id,username,password_hash,role) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING RETURNING id,username,role,enabled,version`, userID, siteID, strings.TrimSpace(in.Username), hash, in.Role).Scan(&user.ID, &user.Username, &user.Role, &user.Enabled, &user.Version); errors.Is(err, pgx.ErrNoRows) {
			return fault.New(409, "username_exists", "账号已存在")
		} else if err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "user.created", ObjectID: user.ID})
	})
	return user, err
}
func (s *Service) UpdateUser(ctx context.Context, p Principal, userID id.ID, expected int64, in UpdateUserInput) (User, error) {
	var user User
	if in.Role == nil && in.Enabled == nil {
		return user, ErrInvalid
	}
	if in.Role != nil && !validRole(*in.Role) {
		return user, ErrInvalid
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(170018)"); err != nil {
			return err
		}
		if err := s.adminTx(ctx, p, tx); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, "SELECT id,username,role,enabled,version FROM users WHERE id=$1 FOR UPDATE", userID).Scan(&user.ID, &user.Username, &user.Role, &user.Enabled, &user.Version)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if user.Version != expected {
			return ErrConflict
		}
		nextRole, nextEnabled := user.Role, user.Enabled
		if in.Role != nil {
			nextRole = *in.Role
		}
		if in.Enabled != nil {
			nextEnabled = *in.Enabled
		}
		if user.Role == "admin" && user.Enabled && (nextRole != "admin" || !nextEnabled) {
			var n int
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM users WHERE role='admin' AND enabled").Scan(&n); err != nil {
				return err
			}
			if n <= 1 {
				return ErrLastAdmin
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE users SET role=$2,enabled=$3,version=version+1,auth_version=auth_version+1 WHERE id=$1", userID, nextRole, nextEnabled); err != nil {
			return err
		}
		if nextRole == "viewer" {
			if _, err := tx.Exec(ctx, "UPDATE channel_grants SET configure=false WHERE user_id=$1", userID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=$1 AND revoked_at IS NULL", userID); err != nil {
			return err
		}
		if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "user.updated", ObjectID: userID}); err != nil {
			return err
		}
		user.Role, user.Enabled, user.Version = nextRole, nextEnabled, user.Version+1
		return nil
	})
	return user, err
}
func (s *Service) ListUsers(ctx context.Context, p Principal, after id.ID, limit int) ([]User, error) {
	if err := s.RequireAdmin(ctx, p); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 101 {
		return nil, ErrInvalid
	}
	rows, err := s.DB.Pool.Query(ctx, "SELECT id,username,role,enabled,version FROM users WHERE ($1::uuid IS NULL OR id>$1) ORDER BY id LIMIT $2", nullable(after), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]User, 0)
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.Enabled, &u.Version); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}
func nullable(i id.ID) any {
	if i == "" {
		return nil
	}
	return i
}
