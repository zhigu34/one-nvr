package auth

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/fault"
)

type entrySchemeKey struct{}

// WithEntryScheme is only called with the server's validated deployment scheme.
func WithEntryScheme(ctx context.Context, scheme string) context.Context {
	return context.WithValue(ctx, entrySchemeKey{}, scheme)
}
func (s *Service) CheckEntryProtocol(ctx context.Context, scheme string) error {
	var active string
	err := s.DB.Pool.QueryRow(ctx, "SELECT scheme FROM auth_entry_protocol WHERE singleton").Scan(&active)
	if err != nil {
		return err
	}
	if active != scheme {
		return fault.New(503, "entry_protocol_changed", "访问协议配置已变更，请重新启动入口服务")
	}
	return nil
}
func (s *Service) checkContextProtocol(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) error {
	scheme, _ := ctx.Value(entrySchemeKey{}).(string)
	if scheme == "" {
		return nil
	}
	var active string
	if err := q.QueryRow(ctx, "SELECT scheme FROM auth_entry_protocol WHERE singleton").Scan(&active); err != nil {
		return err
	}
	if active != scheme {
		return fault.New(503, "entry_protocol_changed", "访问协议配置已变更，请重新启动入口服务")
	}
	return nil
}

// ApplyEntryProtocol runs during API startup, before accepting authentication.
// The handler can retry initialization after a transient startup DB failure.
func (s *Service) ApplyEntryProtocol(ctx context.Context, scheme string) error {
	if scheme != "http" && scheme != "https" {
		return ErrInvalid
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := LockAuthorization(ctx, tx); err != nil {
			return err
		}
		var previous string
		err := tx.QueryRow(ctx, "SELECT scheme FROM auth_entry_protocol WHERE singleton").Scan(&previous)
		if err == nil && previous == scheme {
			return nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE sessions SET revoked_at=clock_timestamp() WHERE revoked_at IS NULL"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO auth_entry_protocol(singleton,scheme) VALUES(true,$1) ON CONFLICT(singleton) DO UPDATE SET scheme=$1,changed_at=clock_timestamp()", scheme); err != nil {
			return err
		}
		details, _ := json.Marshal(map[string]string{"scheme": scheme})
		return audit.Append(ctx, tx, audit.Entry{Action: "auth.entry_protocol_changed", Details: details})
	})
}
