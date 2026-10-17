package storage

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
)

type Pool struct {
	ID        id.ID   `json:"id"`
	SiteID    id.ID   `json:"-"`
	Name      string  `json:"name"`
	Path      string  `json:"path"`
	Enabled   bool    `json:"enabled"`
	IsDefault bool    `json:"is_default"`
	Version   int64   `json:"version"`
	Checks    []Check `json:"checks"`
	State     string  `json:"state"`
	// Verified indexed local usage; raw working/unpublished media usage is unknown.
	UsedBytes *int64 `json:"used_bytes"`
	// Watermark thresholds in percent. Warning only reports, pressure starts
	// deleting until the target is met, critical keeps deleting. They live on
	// the pool because each pool may sit on a different filesystem.
	WarningPercent  int `json:"warning_percent"`
	PressurePercent int `json:"pressure_percent"`
	TargetPercent   int `json:"pressure_target_percent"`
	CriticalPercent int `json:"critical_percent"`
	// Effective state of the pool against its thresholds: ok, warning,
	// pressure, critical or unknown. Empty until capacity has been measured.
	WatermarkState string `json:"watermark_state,omitempty"`
}
type Page struct {
	NextCursor *id.ID   `json:"next_cursor"`
	Items      []Pool   `json:"items"`
	Capacity   Capacity `json:"capacity"`
}
type RegisterInput struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type UpdateInput struct {
	Name      *string `json:"name"`
	Enabled   *bool   `json:"enabled"`
	IsDefault *bool   `json:"is_default"`
	// Watermark thresholds; each is optional so a rename does not have to
	// restate them.
	WarningPercent  *int `json:"warning_percent"`
	PressurePercent *int `json:"pressure_percent"`
	TargetPercent   *int `json:"pressure_target_percent"`
	CriticalPercent *int `json:"critical_percent"`
}
type Service struct {
	DB             *database.DB
	Auth           *auth.Service
	Roots          []string
	MediaInspector MediaInspector
	MediaCheck     func(context.Context, id.ID) error
}

func New(db *database.DB, a *auth.Service, roots []string) *Service {
	return &Service{DB: db, Auth: a, Roots: append([]string(nil), roots...)}
}

const poolColumns = "id,site_id,name,canonical_path,enabled,is_default,version,warning_percent,pressure_percent,pressure_target_percent,critical_percent"

func scanPool(row pgx.Row) (Pool, error) {
	var p Pool
	err := row.Scan(&p.ID, &p.SiteID, &p.Name, &p.Path, &p.Enabled, &p.IsDefault, &p.Version,
		&p.WarningPercent, &p.PressurePercent, &p.TargetPercent, &p.CriticalPercent)
	return p, err
}

// validWatermarks enforces the ordering the migration also enforces. The
// service checks it first so the caller gets a 400 rather than a constraint
// violation after a half-applied update.
func validWatermarks(warning, pressure, target, critical int) bool {
	return warning >= 1 && warning <= 99 &&
		pressure >= 1 && pressure <= 99 &&
		target >= 1 && target <= 99 &&
		critical >= 1 && critical <= 100 &&
		warning <= pressure && target <= pressure && pressure <= critical
}
func validName(name string) bool {
	return name != "" && len(name) <= 128 && strings.IndexFunc(name, unicode.IsControl) < 0
}
func lockPools(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(170020)")
	return err
}

func (s *Service) Register(ctx context.Context, p auth.Principal, in RegisterInput) (Pool, error) {
	var out Pool
	in.Name = strings.TrimSpace(in.Name)
	if !validName(in.Name) {
		return out, auth.ErrInvalid
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		if err := lockPools(ctx, tx); err != nil {
			return err
		}
		root, canonical, err := openPool(s.Roots, in.Path)
		if err != nil {
			return err
		}
		defer root.Close()
		rows, err := tx.Query(ctx, "SELECT canonical_path FROM storage_pools")
		if err != nil {
			return err
		}
		conflict := false
		for rows.Next() {
			var existing string
			if err := rows.Scan(&existing); err != nil {
				rows.Close()
				return err
			}
			if overlaps(existing, canonical) {
				conflict = true
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if conflict {
			return auth.ErrConflict
		}
		var siteID id.ID
		if err := tx.QueryRow(ctx, "SELECT id FROM sites WHERE singleton").Scan(&siteID); err != nil {
			return err
		}
		poolID, err := id.New()
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		m, err := publishMarker(root, Marker{Version: 1, SiteID: siteID, PoolID: poolID, Path: canonical})
		if err != nil {
			return err
		}
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM storage_pools WHERE id=$1)", m.PoolID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return auth.ErrConflict
		}
		out, err = scanPool(tx.QueryRow(ctx, "INSERT INTO storage_pools(id,site_id,name,canonical_path) VALUES($1,$2,$3,$4) RETURNING "+poolColumns, m.PoolID, siteID, in.Name, canonical))
		if err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "storage.registered", ObjectID: out.ID})
	})
	if err != nil {
		return Pool{}, err
	}
	// API evidence is measured by this process; it never writes Worker evidence.
	if err = s.SamplePool(ctx, out, "api"); err != nil {
		return out, err
	}
	return s.withChecks(ctx, out)
}

func (s *Service) Update(ctx context.Context, p auth.Principal, poolID id.ID, expected int64, in UpdateInput) (Pool, error) {
	var out Pool
	if expected < 1 || (in.Name == nil && in.Enabled == nil && in.IsDefault == nil &&
		in.WarningPercent == nil && in.PressurePercent == nil &&
		in.TargetPercent == nil && in.CriticalPercent == nil) {
		return out, auth.ErrInvalid
	}
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if !validName(*in.Name) {
			return out, auth.ErrInvalid
		}
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		if err := lockPools(ctx, tx); err != nil {
			return err
		}
		var err error
		out, err = scanPool(tx.QueryRow(ctx, "SELECT "+poolColumns+" FROM storage_pools WHERE id=$1 FOR UPDATE", poolID))
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.ErrNotFound
		}
		if err != nil {
			return err
		}
		if out.Version != expected {
			return auth.ErrConflict
		}
		if in.Name != nil {
			out.Name = *in.Name
		}
		if in.Enabled != nil {
			out.Enabled = *in.Enabled
		}
		if in.IsDefault != nil {
			out.IsDefault = *in.IsDefault
		}
		if in.WarningPercent != nil {
			out.WarningPercent = *in.WarningPercent
		}
		if in.PressurePercent != nil {
			out.PressurePercent = *in.PressurePercent
		}
		if in.TargetPercent != nil {
			out.TargetPercent = *in.TargetPercent
		}
		if in.CriticalPercent != nil {
			out.CriticalPercent = *in.CriticalPercent
		}
		// Validate the merged result, not just the fields that were sent: a
		// request that moves only the pressure line can still invert the order
		// against an unchanged neighbour.
		if !validWatermarks(out.WarningPercent, out.PressurePercent, out.TargetPercent, out.CriticalPercent) {
			return auth.ErrInvalid
		}
		if !out.Enabled {
			if in.IsDefault != nil && *in.IsDefault {
				return auth.ErrInvalid
			}
			out.IsDefault = false
		}
		if out.IsDefault {
			if _, err = tx.Exec(ctx, "UPDATE storage_pools SET is_default=false,version=version+1 WHERE site_id=$1 AND is_default AND id<>$2", out.SiteID, out.ID); err != nil {
				return err
			}
		}
		out, err = scanPool(tx.QueryRow(ctx, "UPDATE storage_pools SET name=$2,enabled=$3,is_default=$4,warning_percent=$5,pressure_percent=$6,pressure_target_percent=$7,critical_percent=$8,version=version+1 WHERE id=$1 RETURNING "+poolColumns, out.ID, out.Name, out.Enabled, out.IsDefault, out.WarningPercent, out.PressurePercent, out.TargetPercent, out.CriticalPercent))
		if err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "storage.updated", ObjectID: out.ID})
	})
	if err != nil {
		return Pool{}, err
	}
	return s.withChecks(ctx, out)
}

func (s *Service) List(ctx context.Context, p auth.Principal) (Page, error) {
	return s.ListPage(ctx, p, "", 100)
}
func (s *Service) ListPage(ctx context.Context, p auth.Principal, cursor id.ID, limit int) (Page, error) {
	out := Page{Items: []Pool{}}
	if limit < 1 || limit > 100 {
		return out, auth.ErrInvalid
	}
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return out, err
	}
	pools, err := s.pools(ctx)
	if err != nil {
		return out, err
	}
	usageContext, cancelUsage := context.WithTimeout(ctx, 3*time.Second)
	defer cancelUsage()
	start := 0
	found := cursor == ""
	for i, pool := range pools {
		pool, err = s.withChecks(ctx, pool, usageContext)
		if err != nil {
			return out, err
		}
		pools[i] = pool
		if pool.ID == cursor {
			start = i + 1
			found = true
		}
	}
	if !found {
		return out, auth.ErrNotFound
	}
	out.Capacity = AggregateCapacity(pools, time.Now())
	end := start + limit
	if end > len(pools) {
		end = len(pools)
	}
	out.Items = append(out.Items, pools[start:end]...)
	if end < len(pools) {
		next := pools[end-1].ID
		out.NextCursor = &next
	}
	return out, nil
}
func (s *Service) pools(ctx context.Context) ([]Pool, error) {
	rows, err := s.DB.Pool.Query(ctx, "SELECT "+poolColumns+" FROM storage_pools ORDER BY created_at,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Pool{}
	for rows.Next() {
		p, err := scanPool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Service) withChecks(ctx context.Context, p Pool, usageContexts ...context.Context) (Pool, error) {
	p.Checks = []Check{}
	now := time.Now()
	for _, name := range []string{"api", "worker", "zlm"} {
		c := Check{Service: name, State: "pending", Reason: "not_observed"}
		err := s.DB.Pool.QueryRow(ctx, "SELECT state,reason_code,observed_at,expires_at,coalesce(total_bytes,0),coalesce(free_bytes,0),coalesce(filesystem_id,'') FROM storage_pool_checks WHERE pool_id=$1 AND service=$2", p.ID, name).Scan(&c.State, &c.Reason, &c.ObservedAt, &c.ExpiresAt, &c.TotalBytes, &c.FreeBytes, &c.FilesystemID)
		if errors.Is(err, pgx.ErrNoRows) {
			if name == "zlm" {
				c.Reason = "test_source_required"
			}
		} else if err != nil {
			return Pool{}, err
		} else {
			c.ObservedAt = c.ObservedAt.UTC()
			c.ExpiresAt = c.ExpiresAt.UTC()
			if !c.ExpiresAt.After(now) {
				c.State = "expired"
				c.Reason = "observation_expired"
			}
		}
		p.Checks = append(p.Checks, c)
	}
	p.State = p.Readiness(now)
	p.WatermarkState = p.watermarkState(now)
	usageCtx := ctx
	if len(usageContexts) == 1 {
		usageCtx = usageContexts[0]
	}
	usage, err := s.verifiedUsage(usageCtx, p)
	if err != nil && usageCtx.Err() != nil && ctx.Err() == nil {
		err = nil
		usage = nil
	}
	if err != nil {
		return Pool{}, err
	}
	p.UsedBytes = usage
	return p, nil
}

func (s *Service) Delete(ctx context.Context, p auth.Principal, poolID id.ID, expected int64) error {
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		if err := lockPools(ctx, tx); err != nil {
			return err
		}
		pool, err := scanPool(tx.QueryRow(ctx, "SELECT "+poolColumns+" FROM storage_pools WHERE id=$1 FOR UPDATE", poolID))
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.ErrNotFound
		}
		if err != nil {
			return err
		}
		if pool.Version != expected {
			return auth.ErrConflict
		}
		var referenced bool
		// A location whose media was deliberately deleted no longer pins the
		// pool: the recording keeps its index row, but nothing on this disk
		// depends on it any more.
		//
		// This does NOT make a used pool deletable on its own: recording_runs
		// rows are permanent provenance (they are never deleted by design) and
		// their pool reference below still reserves the pool. Reclaiming a pool
		// that ever recorded needs a separate decision about run provenance,
		// which this batch does not make. The file-system walk further down
		// independently refuses to delete a pool that still holds any file.
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM channels WHERE storage_pool_id=$1) OR EXISTS(SELECT 1 FROM recording_runs WHERE pool_id=$1) OR EXISTS(SELECT 1 FROM recording_locations WHERE pool_id=$1 AND state<>'deleted') OR EXISTS(SELECT 1 FROM source_switches WHERE old_pool_id=$1 OR new_pool_id=$1) OR EXISTS(SELECT 1 FROM pool_probe_intents pi JOIN stream_sessions ss ON ss.id=pi.session_id WHERE pi.pool_id=$1 AND ss.state<>'closed')`, poolID).Scan(&referenced); err != nil {
			return err
		}
		if referenced {
			return auth.ErrConflict
		}
		var active bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM jobs WHERE object_id=$1 AND state IN ('queued','running'))", poolID).Scan(&active); err != nil {
			return err
		}
		if active {
			return auth.ErrConflict
		}
		root, canonical, err := openPool(s.Roots, pool.Path)
		if err != nil || canonical != pool.Path {
			return auth.ErrConflict
		}
		defer root.Close()
		if verifyMarker(root, pool) != nil {
			return auth.ErrConflict
		}
		// Empty directories are harmless; all files except the immutable identity
		// block removal, including unexpected user files and unfinished probes.
		err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return auth.ErrConflict
			}
			if entry.IsDir() {
				return nil
			}
			if path == markerName && entry.Type().IsRegular() {
				return nil
			}
			return auth.ErrConflict
		})
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "DELETE FROM pool_probe_intents WHERE pool_id=$1", poolID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "DELETE FROM storage_pool_checks WHERE pool_id=$1", poolID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "DELETE FROM storage_pools WHERE id=$1", poolID); err != nil {
			return err
		}
		// Keep identity and the mounted root intact. Re-registering the empty pool
		// retains the same pool UUID, including after a process crash.
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "storage.deleted", ObjectID: poolID})
	})
}

func (s *Service) RequestCheck(ctx context.Context, p auth.Principal, poolID id.ID) (id.ID, error) {
	var jobID id.ID
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		if err := lockPools(ctx, tx); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM storage_pools WHERE id=$1)", poolID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return auth.ErrNotFound
		}
		err := tx.QueryRow(ctx, "SELECT id FROM jobs WHERE kind='storage.check' AND object_id=$1 AND state IN ('queued','running') ORDER BY created_at LIMIT 1", poolID).Scan(&jobID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		key, err := id.New()
		if err != nil {
			return err
		}
		jobID, err = jobs.Enqueue(ctx, tx, jobs.Input{Kind: "storage.check", ObjectID: poolID, IdempotencyKey: string(key), Payload: []byte(`{"pool_id":"` + string(poolID) + `"}`)})
		if err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "storage.check_requested", ObjectID: poolID})
	})
	return jobID, err
}

// SamplePool stores only the calling process's evidence. It locks the row so
// deletion cannot race a check publication; probes never recreate pool identity.
func (s *Service) SamplePool(ctx context.Context, pool Pool, service string) error {
	if service != "api" && service != "worker" {
		return auth.ErrInvalid
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		current, err := scanPool(tx.QueryRow(ctx, "SELECT "+poolColumns+" FROM storage_pools WHERE id=$1 FOR KEY SHARE", pool.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		check, err := (&Probe{Roots: s.Roots, Service: service}).Check(ctx, current)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO storage_pool_checks(pool_id,service,state,reason_code,observed_at,expires_at,total_bytes,free_bytes,filesystem_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,'')) ON CONFLICT(pool_id,service) DO UPDATE SET state=EXCLUDED.state,reason_code=EXCLUDED.reason_code,observed_at=EXCLUDED.observed_at,expires_at=EXCLUDED.expires_at,total_bytes=EXCLUDED.total_bytes,free_bytes=EXCLUDED.free_bytes,filesystem_id=EXCLUDED.filesystem_id WHERE storage_pool_checks.observed_at<=EXCLUDED.observed_at`, current.ID, check.Service, check.State, check.Reason, check.ObservedAt, check.ExpiresAt, check.TotalBytes, check.FreeBytes, check.FilesystemID)
		return err
	})
}

// watermarkState classifies the pool's freshest capacity evidence against its
// own thresholds.
//
// This mirrors recording.watermarkPolicy.state on purpose rather than sharing
// code: the two processes answer different questions (this one renders, the
// worker deletes) and a shared implementation would have to be exported from a
// package that must not depend on the HTTP layer. The rules are pinned by tests
// in both packages, and the comparison uses raw bytes rather than the rounded
// display percentage so a pool at 79.9% does not read as 80%.
func (p Pool) watermarkState(now time.Time) string {
	checks := 0
	var total, free int64
	agreed := true
	var filesystem string
	for _, c := range p.Checks {
		if c.Service != "api" && c.Service != "worker" {
			continue
		}
		if c.State != "healthy" || !c.ExpiresAt.After(now) {
			continue
		}
		checks++
		if c.TotalBytes > 0 && (total == 0 || c.TotalBytes < total) {
			total = c.TotalBytes
		}
		if c.FreeBytes < free || free == 0 {
			free = c.FreeBytes
		}
		if filesystem == "" {
			filesystem = c.FilesystemID
		} else if c.FilesystemID != filesystem {
			agreed = false
		}
	}
	if checks != 2 || !agreed || filesystem == "" || total <= 0 {
		return "unknown"
	}
	used := total - free
	crosses := func(percent int) bool {
		return percent > 0 && used*100 >= int64(percent)*total
	}
	switch {
	case crosses(p.CriticalPercent):
		return "critical"
	case crosses(p.PressurePercent):
		return "pressure"
	case crosses(p.WarningPercent):
		return "warning"
	}
	return "ok"
}
