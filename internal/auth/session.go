package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/id"
)

func digest(raw string) ([]byte, error) {
	b, err := hex.DecodeString(raw)
	if err != nil || len(b) != 32 {
		return nil, ErrUnauthenticated
	}
	d := sha256.Sum256([]byte(raw))
	return d[:], nil
}
func randomToken() (string, error) {
	var b [32]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}
func (s *Service) CSRF(rawSession string) string {
	h := hmac.New(sha256.New, s.csrfKey)
	h.Write([]byte("session:" + rawSession))
	return hex.EncodeToString(h.Sum(nil))
}
func (s *Service) VerifyCSRF(rawSession, provided string) bool {
	if provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(s.CSRF(rawSession)), []byte(provided)) == 1
}
func (s *Service) Login(ctx context.Context, name, password string) (out LoginResult, resultErr error) {
	defer func() {
		if errors.Is(resultErr, ErrUnauthenticated) {
			err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error { return audit.Append(ctx, tx, audit.Entry{Action: "auth.login_failed"}) })
			if err != nil {
				resultErr = err
			}
		}
	}()
	var result LoginResult
	if !ValidateUsername(name) || len(password) > 128 {
		return result, ErrUnauthenticated
	}
	var user User
	var encoded string
	var authVersion int64
	err := s.DB.Pool.QueryRow(ctx, "SELECT id,username,role,enabled,version,password_hash,auth_version FROM users WHERE lower(username)=lower($1)", name).Scan(&user.ID, &user.Username, &user.Role, &user.Enabled, &user.Version, &encoded, &authVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// Equal-cost derivation for unknown accounts, bounded by the same semaphore.
		_, _ = s.Passwords.derive(ctx, password, make([]byte, 16))
		return result, ErrUnauthenticated
	}
	if !s.Passwords.Verify(ctx, encoded, password) || !user.Enabled {
		return result, ErrUnauthenticated
	}
	raw, err := randomToken()
	if err != nil {
		return result, err
	}
	hash, _ := digest(raw)
	sessionID, err := id.New()
	if err != nil {
		return result, err
	}
	err = s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := LockAuthorization(ctx, tx); err != nil {
			return err
		}
		if err := s.checkContextProtocol(ctx, tx); err != nil {
			return err
		}
		var currentHash string
		var enabled bool
		var currentVersion int64
		if err := tx.QueryRow(ctx, "SELECT password_hash,enabled,auth_version FROM users WHERE id=$1 FOR UPDATE", user.ID).Scan(&currentHash, &enabled, &currentVersion); err != nil {
			return err
		}
		if !enabled || currentVersion != authVersion || currentHash != encoded {
			return ErrUnauthenticated
		}
		if _, err := tx.Exec(ctx, "INSERT INTO sessions(id,digest,user_id,auth_version,absolute_expires_at) VALUES($1,$2,$3,$4,clock_timestamp()+interval '12 hours')", sessionID, hash, user.ID, authVersion); err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: user.ID, Action: "auth.login", ObjectID: user.ID})
	})
	if err != nil {
		return result, err
	}
	return LoginResult{User: user, Principal: Principal{user.ID, sessionID, user.Role, authVersion}, RawSession: raw}, nil
}
func (s *Service) Authenticate(ctx context.Context, raw string) (Principal, error) {
	return s.authenticate(ctx, raw, true)
}

// CheckSession validates polling/read requests without extending idle expiry.
func (s *Service) CheckSession(ctx context.Context, raw string) (Principal, error) {
	return s.authenticate(ctx, raw, false)
}
func (s *Service) authenticate(ctx context.Context, raw string, renew bool) (Principal, error) {
	var p Principal
	hash, err := digest(raw)
	if err != nil {
		return p, err
	}
	const validity = `ss.digest=$1 AND ss.user_id=u.id AND u.enabled AND ss.auth_version=u.auth_version AND ss.revoked_at IS NULL
 AND ss.last_seen_at>clock_timestamp()-interval '30 minutes' AND ss.absolute_expires_at>clock_timestamp()`
	query := `SELECT u.id,ss.id,u.role,u.auth_version FROM sessions ss,users u WHERE ` + validity
	if renew {
		query = `UPDATE sessions ss SET last_seen_at=clock_timestamp() FROM users u WHERE ` + validity + ` RETURNING u.id,ss.id,u.role,u.auth_version`
	}
	err = s.DB.Pool.QueryRow(ctx, query, hash).Scan(&p.UserID, &p.SessionID, &p.Role, &p.AuthVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrUnauthenticated
	}
	return p, err
}
func (s *Service) Logout(ctx context.Context, p Principal) error {
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := LockAuthorization(ctx, tx); err != nil {
			return err
		}
		if _, err := s.current(ctx, p, tx); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1", p.SessionID); err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "auth.logout", ObjectID: p.UserID})
	})
}
func (s *Service) ChangePassword(ctx context.Context, p Principal, old, newPassword string) error {
	if ValidatePassword(newPassword) != nil {
		return ErrInvalid
	}
	if _, err := s.CurrentUser(ctx, p); err != nil {
		return err
	}
	var encoded string
	if err := s.DB.Pool.QueryRow(ctx, "SELECT password_hash FROM users WHERE id=$1", p.UserID).Scan(&encoded); err != nil {
		return err
	}
	if !s.Passwords.Verify(ctx, encoded, old) {
		return ErrUnauthenticated
	}
	next, err := s.Passwords.Hash(ctx, newPassword)
	if err != nil {
		return err
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := LockAuthorization(ctx, tx); err != nil {
			return err
		}
		if _, err := s.current(ctx, p, tx); err != nil {
			return err
		}
		var previous string
		if err := tx.QueryRow(ctx, "SELECT password_hash FROM users WHERE id=$1 FOR UPDATE", p.UserID).Scan(&previous); err != nil {
			return err
		}
		if previous != encoded {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, "UPDATE users SET password_hash=$2,auth_version=auth_version+1,version=version+1 WHERE id=$1", p.UserID, next); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=$1 AND revoked_at IS NULL", p.UserID); err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "auth.password_changed", ObjectID: p.UserID})
	})
}
