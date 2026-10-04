// Package operations stores bounded, expiring observations and safe diagnostics.
package operations

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
)

type Service struct {
	DB                              *database.DB
	Auth                            *auth.Service
	FrigateEnabled, OpenListEnabled bool
}

func New(db *database.DB, a *auth.Service, frigate, openlist bool) *Service {
	return &Service{db, a, frigate, openlist}
}

type Component struct {
	Name       string     `json:"name"`
	Enabled    bool       `json:"enabled"`
	State      string     `json:"state"`
	Reason     string     `json:"reason"`
	ObservedAt *time.Time `json:"observed_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}
type Observation struct {
	Name, State, Reason string
	ObservedAt          time.Time
}

var reasonPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var names = []string{"gateway", "api", "worker", "postgres", "zlm", "frigate", "mqtt", "openlist"}

func (s *Service) Enabled(name string) bool {
	switch name {
	case "frigate", "mqtt":
		return s.FrigateEnabled
	case "openlist":
		return s.OpenListEnabled
	case "gateway", "api", "worker", "postgres", "zlm":
		return true
	default:
		return false
	}
}
func (s *Service) Observe(ctx context.Context, in Observation) error {
	if !s.Enabled(in.Name) || !reasonPattern.MatchString(in.Reason) || (in.State != "healthy" && in.State != "unavailable" && in.State != "unknown") || in.ObservedAt.IsZero() || in.ObservedAt.After(time.Now().Add(5*time.Second)) {
		return auth.ErrInvalid
	}
	at := in.ObservedAt.UTC()
	_, err := s.DB.Pool.Exec(ctx, `INSERT INTO component_observations(name,state,reason_code,observed_at,expires_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(name) DO UPDATE SET state=EXCLUDED.state,reason_code=EXCLUDED.reason_code,observed_at=EXCLUDED.observed_at,expires_at=EXCLUDED.expires_at WHERE component_observations.observed_at<=EXCLUDED.observed_at`, in.Name, in.State, in.Reason, at, at.Add(30*time.Second))
	return err
}
func (s *Service) Components(ctx context.Context, p auth.Principal) ([]Component, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return nil, err
	}
	return s.Snapshot(ctx)
}

// Snapshot is internal; API callers use Components for administrator checks.
func (s *Service) Snapshot(ctx context.Context) ([]Component, error) {
	out := make([]Component, 0, len(names))
	now := time.Now()
	for _, name := range names {
		c := Component{Name: name, Enabled: s.Enabled(name), State: "unknown", Reason: "not_observed"}
		if !c.Enabled {
			c.State = "disabled"
			c.Reason = "feature_disabled"
			out = append(out, c)
			continue
		}
		var at, expires time.Time
		err := s.DB.Pool.QueryRow(ctx, "SELECT state,reason_code,observed_at,expires_at FROM component_observations WHERE name=$1", name).Scan(&c.State, &c.Reason, &at, &expires)
		if errors.Is(err, pgx.ErrNoRows) {
			out = append(out, c)
			continue
		}
		if err != nil {
			return nil, err
		}
		at = at.UTC()
		expires = expires.UTC()
		c.ObservedAt = &at
		c.ExpiresAt = &expires
		if !expires.After(now) {
			c.State = "unknown"
			c.Reason = "observation_expired"
		}
		out = append(out, c)
	}
	return out, nil
}

type Job struct {
	ID        id.ID   `json:"id"`
	Kind      string  `json:"kind"`
	State     string  `json:"state"`
	Attempts  int     `json:"attempts"`
	ErrorCode *string `json:"error_code"`
}

func (s *Service) Job(ctx context.Context, p auth.Principal, jobID id.ID) (Job, error) {
	var out Job
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return out, err
	}
	err := s.DB.Pool.QueryRow(ctx, "SELECT id,kind,state,attempt,error_code FROM jobs WHERE id=$1", jobID).Scan(&out.ID, &out.Kind, &out.State, &out.Attempts, &out.ErrorCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, auth.ErrNotFound
	}
	return out, err
}

type Audit struct {
	ID        id.ID     `json:"id"`
	ActorID   *id.ID    `json:"actor_id"`
	Action    string    `json:"action"`
	ObjectID  *id.ID    `json:"object_id"`
	CreatedAt time.Time `json:"created_at"`
}
type AuditPage struct {
	Items      []Audit `json:"items"`
	NextCursor *id.ID  `json:"next_cursor"`
}

func (s *Service) Audits(ctx context.Context, p auth.Principal, cursor id.ID, limit int) (AuditPage, error) {
	out := AuditPage{Items: make([]Audit, 0)}
	if limit < 1 || limit > 100 {
		return out, auth.ErrInvalid
	}
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return out, err
	}
	var rows pgx.Rows
	var err error
	if cursor == "" {
		rows, err = s.DB.Pool.Query(ctx, "SELECT id,actor_id,action,object_id,created_at FROM audit_logs ORDER BY created_at DESC,id DESC LIMIT $1", limit+1)
	} else {
		var at time.Time
		err = s.DB.Pool.QueryRow(ctx, "SELECT created_at FROM audit_logs WHERE id=$1", cursor).Scan(&at)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, auth.ErrNotFound
		}
		if err != nil {
			return out, err
		}
		rows, err = s.DB.Pool.Query(ctx, "SELECT id,actor_id,action,object_id,created_at FROM audit_logs WHERE (created_at,id)<($1,$2) ORDER BY created_at DESC,id DESC LIMIT $3", at, cursor, limit+1)
	}
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Audit
		if err := rows.Scan(&a.ID, &a.ActorID, &a.Action, &a.ObjectID, &a.CreatedAt); err != nil {
			return out, err
		}
		a.CreatedAt = a.CreatedAt.UTC()
		out.Items = append(out.Items, a)
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
