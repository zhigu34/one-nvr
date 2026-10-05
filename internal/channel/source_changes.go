package channel

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/storage"
)

type SwitchWork struct {
	ID                                         id.ID
	Task                                       SourceTask
	Kind, State, Phase                         string
	OldRevision, NewRevision, OldPool, NewPool *id.ID
	OldMode, DesiredMode                       string
	Deadline                                   *time.Time
	ErrorCode                                  string
}

func (s *SourceService) RequestApply(ctx context.Context, p auth.Principal, ch id.ID, in SourceApplyInput, key string) (Change, error) {
	if in.ExpectedVersion < 1 || in.RevisionID == "" || in.TestID == "" {
		return Change{}, auth.ErrInvalid
	}
	if in.FirstRecordingMode != nil && *in.FirstRecordingMode != "none" && *in.FirstRecordingMode != "continuous" {
		return Change{}, auth.ErrInvalid
	}
	task := SourceTask{ChannelID: ch, RevisionID: in.RevisionID, ActorID: p.UserID, AuthVersion: p.AuthVersion, TestID: in.TestID, ExpectedVersion: in.ExpectedVersion, FirstRecordingMode: in.FirstRecordingMode}
	return s.requestChange(ctx, p, task, "apply", key)
}
func (s *SourceService) RequestClear(ctx context.Context, p auth.Principal, ch id.ID, expected int64, key string) (Change, error) {
	if expected < 1 {
		return Change{}, auth.ErrInvalid
	}
	return s.requestChange(ctx, p, SourceTask{ChannelID: ch, ActorID: p.UserID, AuthVersion: p.AuthVersion, ExpectedVersion: expected}, "clear", key)
}

func (s *SourceService) SetPolicy(ctx context.Context, p auth.Principal, ch id.ID, expected int64, mode, key string) (Change, error) {
	if expected < 1 || mode != "none" && mode != "continuous" {
		return Change{}, auth.ErrInvalid
	}
	return s.requestChange(ctx, p, SourceTask{ChannelID: ch, ActorID: p.UserID, AuthVersion: p.AuthVersion, ExpectedVersion: expected, RecordingMode: &mode}, "policy_apply", key)
}
func (s *SourceService) BindPool(ctx context.Context, p auth.Principal, ch, pool id.ID, expected int64, key string) (Change, error) {
	if expected < 1 {
		return Change{}, auth.ErrInvalid
	}
	if _, err := id.Parse(string(pool)); err != nil {
		return Change{}, auth.ErrInvalid
	}
	return s.requestChange(ctx, p, SourceTask{ChannelID: ch, ActorID: p.UserID, AuthVersion: p.AuthVersion, ExpectedVersion: expected, PoolID: &pool}, "pool_switch", key)
}
func (s *SourceService) requestChange(ctx context.Context, p auth.Principal, task SourceTask, kind, key string) (Change, error) {
	var out Change
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = s.requestChangeTx(tx, false, ctx, p, task, kind, key)
		return err
	})
	return out, err
}
func (s *SourceService) requestChangeTx(tx pgx.Tx, worker bool, ctx context.Context, p auth.Principal, task SourceTask, kind, key string) (Change, error) {
	var out Change
	if !validSourceJobKey(key) {
		return out, auth.ErrInvalid
	}
	raw, _ := json.Marshal(task)
	err := func(tx pgx.Tx) error {
		if err := s.authorizeSourceTx(ctx, p, task.ChannelID, worker, tx); err != nil {
			return err
		}
		// Enqueue and the domain receipt share one transaction. A replay returns the
		// original receipt even after its proof expires or its channel version moves.
		job, err := jobs.Enqueue(ctx, tx, jobs.Input{Kind: "source." + kind, ObjectID: task.ChannelID, IdempotencyKey: key, Payload: raw})
		if err != nil {
			return err
		}
		out.JobID = job
		var existing id.ID
		err = tx.QueryRow(ctx, "SELECT id FROM source_switches WHERE job_id=$1", job).Scan(&existing)
		if err == nil {
			return tx.QueryRow(ctx, "SELECT state FROM jobs WHERE id=$1", job).Scan(&out.State)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var site id.ID
		var current, pool *id.ID
		var version int64
		var mode string
		var enabled bool
		if err := tx.QueryRow(ctx, `SELECT c.site_id,c.current_revision_id,c.storage_pool_id,c.version,c.enabled,p.mode FROM channels c JOIN recording_policies p ON p.channel_id=c.id WHERE c.id=$1 FOR UPDATE OF c,p`, task.ChannelID).Scan(&site, &current, &pool, &version, &enabled, &mode); err != nil {
			return err
		}
		if version != task.ExpectedVersion {
			return auth.ErrConflict
		}
		var busy bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM source_switches WHERE channel_id=$1 AND state IN ('queued','running'))", task.ChannelID).Scan(&busy); err != nil {
			return err
		}
		if busy {
			return auth.ErrConflict
		}
		desired := mode
		if kind == "clear" && mode == "planned" {
			desired = "none"
		} // clear preserves the stored policy; no planned activation is requested.
		var next *id.ID
		var test *id.ID
		if kind == "policy_apply" || kind == "pool_switch" {
			next = current
			if task.RecordingMode != nil {
				desired = *task.RecordingMode
			}
			if task.PoolID != nil {
				pool = task.PoolID
			}
			if desired == "continuous" && (current == nil || !enabled) {
				return auth.ErrConflict
			}
			if pool == nil && desired == "continuous" {
				var value id.ID
				if err := tx.QueryRow(ctx, "SELECT id FROM storage_pools WHERE site_id=$1 AND is_default AND enabled", site).Scan(&value); err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						return storage.ErrMediaProof
					}
					return err
				}
				pool = &value
			}
			if pool != nil && (desired == "continuous" || kind == "pool_switch") {
				var ready bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM storage_pools p WHERE p.id=$1 AND p.site_id=$2 AND p.enabled AND (SELECT count(*) FROM storage_pool_checks WHERE pool_id=p.id AND state='healthy' AND expires_at>clock_timestamp())=3)`, pool, site).Scan(&ready); err != nil {
					return err
				}
				if !ready {
					return storage.ErrMediaProof
				}
			}
		}
		if kind == "apply" {
			if !enabled {
				return auth.ErrConflict
			}
			var proven bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_tests t JOIN source_revisions r ON r.id=t.revision_id AND r.channel_id=t.channel_id WHERE t.id=$1 AND t.channel_id=$2 AND t.revision_id=$3 AND t.purpose='source' AND t.state='succeeded' AND t.observed_at<=clock_timestamp() AND t.expires_at>clock_timestamp() AND t.configuration_digest=r.configuration_digest AND t.result->'main'->>'state'='healthy' AND t.result->'main'->>'first_frame'='true')`, task.TestID, task.ChannelID, task.RevisionID).Scan(&proven); err != nil {
				return err
			}
			if !proven {
				return auth.ErrConflict
			}
			var previouslyConfigured bool
			if err := tx.QueryRow(ctx, "SELECT $2::uuid IS NOT NULL OR EXISTS(SELECT 1 FROM source_switches WHERE channel_id=$1 AND kind='apply' AND state='succeeded')", task.ChannelID, current).Scan(&previouslyConfigured); err != nil {
				return err
			}
			if !previouslyConfigured {
				if task.FirstRecordingMode == nil {
					return auth.ErrInvalid
				}
				desired = *task.FirstRecordingMode
			} else if task.FirstRecordingMode != nil {
				return auth.ErrInvalid
			}
			if desired != "none" && desired != "continuous" {
				return auth.ErrConflict
			}
			next = &task.RevisionID
			test = &task.TestID
			if desired == "continuous" {
				if pool == nil {
					var defaultPool id.ID
					if err := tx.QueryRow(ctx, "SELECT id FROM storage_pools WHERE site_id=$1 AND is_default AND enabled", site).Scan(&defaultPool); err != nil {
						if errors.Is(err, pgx.ErrNoRows) {
							return storage.ErrMediaProof
						}
						return err
					}
					pool = &defaultPool
				}
				var ready bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM storage_pools p WHERE p.id=$1 AND p.site_id=$2 AND p.enabled AND (SELECT count(*) FROM storage_pool_checks WHERE pool_id=p.id AND state='healthy' AND expires_at>clock_timestamp())=3)`, pool, site).Scan(&ready); err != nil {
					return err
				}
				if !ready {
					return storage.ErrMediaProof
				}
			}
		}
		sw, _ := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO source_switches(id,channel_id,job_id,kind,old_revision_id,new_revision_id,test_id,old_pool_id,new_pool_id,old_mode,desired_mode,expected_version,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,(SELECT storage_pool_id FROM channels WHERE id=$2),$8,$9,$10,$11,$12)`, sw, task.ChannelID, job, kind, current, next, test, pool, mode, desired, task.ExpectedVersion, p.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE channels SET desired_revision_id=$2,version=version+1 WHERE id=$1", task.ChannelID, next); err != nil {
			return err
		}
		if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "source." + kind + "_requested", ObjectID: task.ChannelID}); err != nil {
			return err
		}
		out.State = "queued"
		return nil
	}(tx)
	return out, err
}

func (s *SourceService) PrepareChange(ctx context.Context, e *Execution) (SwitchWork, error) {
	var out SwitchWork
	task, err := DecodeSourceTask(e.Lease.Payload)
	if err != nil {
		return out, err
	}
	out.Task = task
	if task.ChannelID != e.ChannelID || e.Lease.Kind != "source.apply" && e.Lease.Kind != "source.clear" && e.Lease.Kind != "source.policy_apply" && e.Lease.Kind != "source.pool_switch" {
		return out, auth.ErrInvalid
	}
	err = e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id,kind,state,phase,old_revision_id,new_revision_id,old_pool_id,new_pool_id,old_mode,desired_mode,phase_deadline,coalesce(error_code,'') FROM source_switches WHERE job_id=$1 AND channel_id=$2 FOR UPDATE`, e.Lease.ID, e.ChannelID).Scan(&out.ID, &out.Kind, &out.State, &out.Phase, &out.OldRevision, &out.NewRevision, &out.OldPool, &out.NewPool, &out.OldMode, &out.DesiredMode, &out.Deadline, &out.ErrorCode); err != nil {
			return err
		}
		if out.State != "queued" {
			return nil
		}
		reason := ""
		if err := s.Auth.RequireActorChannelTx(ctx, task.ActorID, task.AuthVersion, task.ChannelID, auth.Configure, tx); err != nil {
			if !errors.Is(err, auth.ErrForbidden) {
				return err
			}
			reason = "authorization_revoked"
		}
		var version int64
		if err := tx.QueryRow(ctx, "SELECT version FROM channels WHERE id=$1 FOR UPDATE", task.ChannelID).Scan(&version); err != nil {
			return err
		}
		if reason == "" && version != task.ExpectedVersion+1 {
			reason = "channel_version_changed"
		}
		if reason == "" && out.Kind == "apply" {
			var proven bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_tests t JOIN source_revisions r ON r.id=t.revision_id AND r.channel_id=t.channel_id WHERE t.id=$1 AND t.channel_id=$2 AND t.revision_id=$3 AND t.state='succeeded' AND t.purpose='source' AND t.expires_at>clock_timestamp() AND t.observed_at<=clock_timestamp() AND t.configuration_digest=r.configuration_digest AND t.result->'main'->>'state'='healthy' AND t.result->'main'->>'first_frame'='true')`, task.TestID, task.ChannelID, task.RevisionID).Scan(&proven); err != nil {
				return err
			}
			if !proven {
				reason = "source_test_expired"
			}
		}
		if reason != "" {
			if _, err := tx.Exec(ctx, "UPDATE source_switches SET state='cancelled',phase='cancelled',error_code=$2,finished_at=clock_timestamp() WHERE id=$1", out.ID, reason); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE channels SET desired_revision_id=current_revision_id,version=version+1 WHERE id=$1", task.ChannelID); err != nil {
				return err
			}
			out.State = "cancelled"
			out.Phase = "cancelled"
			out.ErrorCode = reason
			return nil
		}
		if _, err := tx.Exec(ctx, "UPDATE source_switches SET state='running',phase='testing',started_at=clock_timestamp(),fencing_token=$2 WHERE id=$1", out.ID, e.Lease.FencingToken); err != nil {
			return err
		}
		out.State = "running"
		out.Phase = "testing"
		return nil
	})
	return out, err
}
