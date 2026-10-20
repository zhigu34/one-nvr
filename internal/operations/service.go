// Package operations stores bounded, expiring observations and safe diagnostics.
package operations

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"sync"
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
	// OpenListUIPort is the host port the bundled OpenList console is published
	// on, zero when it is not published. The console is where its administrator
	// account is first created, so an operator needs the number to reach it and
	// there is no other place the deployment tells anyone.
	OpenListUIPort int
	// host is the sampler behind HostResources. It keeps the previous CPU
	// counters, so it must survive between requests rather than being created
	// per call — a fresh sampler has nothing to subtract from.
	host *hostSampler
	// EventCacheDir is the tmpfs mount point for the event ring buffer, empty
	// when the deployment did not reserve memory for it.
	EventCacheDir string
	once          sync.Once
}

// Resources reports what the machine is doing (DASH-02).
//
// Administrator-only: it describes the host one-nvr runs on, which is site
// infrastructure rather than business data.
func (s *Service) Resources(ctx context.Context, p auth.Principal) (HostResources, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return HostResources{}, err
	}
	s.once.Do(func() { s.host = newHostSampler(s.EventCacheDir) })
	return s.host.Sample(ctx), nil
}

func New(db *database.DB, a *auth.Service, frigate, openlist bool) *Service {
	return &Service{DB: db, Auth: a, FrigateEnabled: frigate, OpenListEnabled: openlist}
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

// BundledOpenList is what the archive page needs to offer a link into the
// container's own console.
//
// It carries the state as well as the port because the link is only worth
// showing when the console can actually answer, and only meaningful once the
// operator knows whether it is a first-run or an existing install.
type BundledOpenList struct {
	Enabled bool   `json:"enabled"`
	State   string `json:"state"`
	Reason  string `json:"reason"`
	// UIPort is the host port the console is published on; zero means it is not
	// published, and the page can only say so. It is not a URL: the console is
	// plain HTTP on a port separate from the gateway, and the host the operator
	// reached one-nvr on is known to the browser, not to the server.
	UIPort int `json:"ui_port"`
}

// BundledOpenList reports the bundled console's state and published port.
func (s *Service) BundledOpenList(ctx context.Context, p auth.Principal) (BundledOpenList, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return BundledOpenList{}, err
	}
	out := BundledOpenList{Enabled: s.OpenListEnabled, UIPort: s.OpenListUIPort, State: "disabled", Reason: "feature_disabled"}
	if !s.OpenListEnabled {
		return out, nil
	}
	out.State, out.Reason = "unknown", "not_observed"
	var expires time.Time
	err := s.DB.Pool.QueryRow(ctx, "SELECT state,reason_code,expires_at FROM component_observations WHERE name='openlist'").Scan(&out.State, &out.Reason, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return BundledOpenList{}, err
	}
	// An expired observation is not evidence, same rule as Snapshot.
	if !expires.After(time.Now()) {
		out.State, out.Reason = "unknown", "observation_expired"
	}
	return out, nil
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
	ID      id.ID  `json:"id"`
	ActorID *id.ID `json:"actor_id"`
	// ActorName is the username behind ActorID. The audit trail stores the
	// identity, not the display name, because a user can be renamed; the page
	// still has to show a person rather than a UUID, so the join happens here.
	// Empty means the action had no user behind it (system work) or the account
	// no longer exists — the two are told apart by ActorID being null.
	ActorName string `json:"actor_name"`
	Action    string `json:"action"`
	ObjectID  *id.ID `json:"object_id"`
	// ObjectType and ObjectLabel describe what ObjectID points at. Without them
	// the audit table showed bare UUIDs, which no operator can act on.
	ObjectType  string    `json:"object_type"`
	ObjectLabel string    `json:"object_label"`
	CreatedAt   time.Time `json:"created_at"`
}
type AuditPage struct {
	Items      []Audit `json:"items"`
	NextCursor *id.ID  `json:"next_cursor"`
}

// auditPageQuery resolves everything the audit table shows in one pass.
//
// The object label is resolved with LEFT JOINs over the entities an audit row
// can point at. LEFT, not inner: an audit row survives the thing it describes
// (that is the point of an audit trail), and a deleted channel must still show
// its row. When no join matches, the label falls back to the action family so
// the operator sees "录像片段" rather than a UUID.
const auditPageQuery = `SELECT l.id,l.actor_id,COALESCE(u.username,''),l.action,l.object_id,
	CASE
		WHEN c.id IS NOT NULL THEN 'channel'
		WHEN u2.id IS NOT NULL THEN 'user'
		WHEN p.id IS NOT NULL THEN 'storage_pool'
		WHEN s.id IS NOT NULL THEN 'recording_segment'
		WHEN e.id IS NOT NULL THEN 'event'
		ELSE ''
	END,
	CASE
		WHEN c.id IS NOT NULL THEN 'CH'||lpad(c.channel_no::text,2,'0')||CASE WHEN c.channel_name<>'' THEN ' '||c.channel_name ELSE '' END
		WHEN u2.id IS NOT NULL THEN u2.username
		WHEN p.id IS NOT NULL THEN p.name
		WHEN s.id IS NOT NULL THEN to_char(s.start_at,'YYYY-MM-DD HH24:MI')||' 的录像'
		WHEN e.id IS NOT NULL THEN '事件 '||left(e.id::text,8)
		ELSE ''
	END,
	l.created_at
	FROM audit_logs l
	LEFT JOIN users u ON u.id=l.actor_id
	LEFT JOIN users u2 ON u2.id=l.object_id
	LEFT JOIN channels c ON c.id=l.object_id
	LEFT JOIN storage_pools p ON p.id=l.object_id
	LEFT JOIN recording_segments s ON s.id=l.object_id
	LEFT JOIN events e ON e.id=l.object_id
	WHERE ($1::text IS NULL OR l.action LIKE $1)`

// validAuditPrefix accepts only the action families this application writes.
// A free-form filter would let a caller drive a full table scan with a leading
// wildcard, and there is no operator need for anything outside this list.
func validAuditPrefix(prefix string) bool {
	switch prefix {
	case "", "auth", "channel", "recording", "source", "storage", "event", "detection", "archive", "user", "tls", "site", "onvif":
		return true
	}
	return false
}

func (s *Service) Audits(ctx context.Context, p auth.Principal, prefix string, cursor id.ID, limit int) (AuditPage, error) {
	out := AuditPage{Items: make([]Audit, 0)}
	if limit < 1 || limit > 500 || !validAuditPrefix(prefix) {
		return out, auth.ErrInvalid
	}
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return out, err
	}
	// The prefix filters by action family (auth, channel, recording, ...). It is
	// a whitelist rather than a free-form LIKE: an operator must not be able to
	// make the database scan with a wildcard, and the set of families is fixed
	// by the actions this application writes.
	var family any
	if prefix != "" {
		family = prefix + ".%"
	}
	var rows pgx.Rows
	var err error
	if cursor == "" {
		rows, err = s.DB.Pool.Query(ctx, auditPageQuery+" ORDER BY l.created_at DESC,l.id DESC LIMIT $2", family, limit+1)
	} else {
		var at time.Time
		err = s.DB.Pool.QueryRow(ctx, "SELECT created_at FROM audit_logs WHERE id=$1", cursor).Scan(&at)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, auth.ErrNotFound
		}
		if err != nil {
			return out, err
		}
		rows, err = s.DB.Pool.Query(ctx, auditPageQuery+" AND (l.created_at,l.id)<($2,$3) ORDER BY l.created_at DESC,l.id DESC LIMIT $4", family, at, cursor, limit+1)
	}
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Audit
		if err := rows.Scan(&a.ID, &a.ActorID, &a.ActorName, &a.Action, &a.ObjectID, &a.ObjectType, &a.ObjectLabel, &a.CreatedAt); err != nil {
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

// AuditRetention is how long an audit row is kept (PRD SYS-01: 180 days).
//
// The audit trail is the record of who changed what, so the window is a policy
// decision rather than a capacity one — six months is what the PRD names, and
// deleting older rows is what keeps the table from growing without bound on a
// site that logs every playback.
const AuditRetention = 180 * 24 * time.Hour

// auditSweepBatch bounds one delete pass. The first run after an upgrade may have
// years of backlog, and one enormous DELETE would hold locks and write-ahead log
// for an arbitrarily long time.
const auditSweepBatch = 500

// SweepAudits deletes audit rows past the retention window.
//
// Until this existed nothing ever removed an audit row: the table only grew.
// That is a real defect on a site whose log is dominated by playback entries —
// this deployment had 2028 rows in ten days, which is roughly 74k a year for a
// 16-channel site that nobody watches.
//
// Deleting is deliberately plain: no protection, no pinning, no exception for
// "important" actions. The retention window is the policy, and an audit row that
// outlives it is not more valuable than the window says — inventing exceptions
// here would make the window untrustworthy in exactly the direction that matters
// (an operator assuming old evidence is still there).
func (s *Service) SweepAudits(ctx context.Context) {
	if s.DB == nil {
		return
	}
	for {
		tag, err := s.DB.Pool.Exec(ctx, `DELETE FROM audit_logs WHERE id IN (
			SELECT id FROM audit_logs WHERE created_at < clock_timestamp() - $1::interval LIMIT $2)`,
			AuditRetention.String(), auditSweepBatch)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("audit retention unavailable", "error", err)
			}
			return
		}
		if tag.RowsAffected() < auditSweepBatch || ctx.Err() != nil {
			return
		}
	}
}
