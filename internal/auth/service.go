package auth

import (
	"context"
	"encoding/hex"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/secrets"
)

var (
	ErrUnauthenticated = fault.New(401, "unauthenticated", "请登录或重新登录")
	ErrForbidden       = fault.New(403, "forbidden", "没有权限执行此操作")
	ErrNotFound        = fault.New(404, "not_found", "资源不存在")
	ErrLastAdmin       = fault.New(409, "last_admin", "不能停用或降级最后一个管理员")
	ErrConflict        = fault.New(409, "version_conflict", "配置已更新，请刷新后重试")
	ErrInvalid         = fault.New(422, "invalid_input", "参数无效")
)

type Principal struct {
	UserID, SessionID id.ID
	Role              string
	AuthVersion       int64
}
type User struct {
	ID       id.ID  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Enabled  bool   `json:"enabled"`
	Version  int64  `json:"version"`
}
type LoginResult struct {
	User       User
	Principal  Principal
	RawSession string
}
type Service struct {
	DB        *database.DB
	Passwords *PasswordHasher
	csrfKey   []byte
}

func NewService(db *database.DB, secret secrets.State, h *PasswordHasher) *Service {
	key, err := hex.DecodeString(secret.CSRFKey)
	if err != nil || len(key) != 32 {
		panic("invalid persistent CSRF key")
	}
	return &Service{DB: db, Passwords: h, csrfKey: key}
}

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)

func ValidateUsername(name string) bool { return usernamePattern.MatchString(name) }
func validRole(role string) bool        { return role == "admin" || role == "operator" || role == "viewer" }
func (s *Service) current(ctx context.Context, p Principal, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (User, error) {
	var user User
	if err := s.checkContextProtocol(ctx, q); err != nil {
		return user, err
	}
	err := q.QueryRow(ctx, `SELECT u.id,u.username,u.role,u.enabled,u.version FROM users u JOIN sessions ss ON ss.user_id=u.id
 WHERE u.id=$1 AND ss.id=$2 AND u.enabled AND u.auth_version=$3 AND ss.auth_version=u.auth_version AND ss.revoked_at IS NULL
 AND ss.last_seen_at>clock_timestamp()-interval '30 minutes' AND ss.absolute_expires_at>clock_timestamp()`, p.UserID, p.SessionID, p.AuthVersion).Scan(&user.ID, &user.Username, &user.Role, &user.Enabled, &user.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrUnauthenticated
	}
	return user, err
}
func (s *Service) CurrentUser(ctx context.Context, p Principal) (User, error) {
	return s.current(ctx, p, s.DB.Pool)
}
func (s *Service) RequireAdmin(ctx context.Context, p Principal) error {
	user, err := s.CurrentUser(ctx, p)
	if err != nil {
		return err
	}
	if user.Role != "admin" {
		return ErrForbidden
	}
	return nil
}
func LockAuthorization(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(170018)")
	return err
}
func (s *Service) RequireAdminTx(ctx context.Context, p Principal, tx pgx.Tx) error {
	return s.adminTx(ctx, p, tx)
}
func (s *Service) adminTx(ctx context.Context, p Principal, tx pgx.Tx) error {
	if err := LockAuthorization(ctx, tx); err != nil {
		return err
	}
	user, err := s.current(ctx, p, tx)
	if err != nil {
		return err
	}
	if user.Role != "admin" {
		return ErrForbidden
	}
	return nil
}
