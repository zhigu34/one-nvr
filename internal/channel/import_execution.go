package channel

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/storage"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/secrets"
)

func importTerminal(state string) bool {
	return state == "succeeded" || state == "skipped" || state == "failed" || state == "cancelled" || state == "tested"
}
func importFailureCode(err error) string {
	switch {
	case errors.Is(err, auth.ErrConflict):
		return "channel_version_changed"
	case errors.Is(err, auth.ErrForbidden), errors.Is(err, auth.ErrNotFound), errors.Is(err, auth.ErrUnauthenticated):
		return "authorization_revoked"
	case errors.Is(err, secrets.ErrCredentialUnavailable):
		return "credential_unavailable"
	default:
		return "import_row_invalid"
	}
}

// AdvanceImport only orchestrates durable child jobs. It never holds a channel
// owner while waiting for source.test/source.apply, so the existing two-slot
// test limit and per-channel media fencing also apply to imports.
func (s *SourceService) AdvanceImport(ctx context.Context, lease jobs.Lease) (bool, error) {
	var task importTask
	if lease.Kind != "source.import" || json.Unmarshal(lease.Payload, &task) != nil {
		return false, auth.ErrInvalid
	}
	done := false
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := auth.LockAuthorization(ctx, tx); err != nil {
			return err
		}
		var batch id.ID
		fence := func() error {
			err := tx.QueryRow(ctx, `SELECT object_id FROM jobs WHERE id=$1 AND kind='source.import' AND state='running' AND fencing_token=$2 AND attempt=$3 AND lease_expires_at>clock_timestamp() FOR UPDATE`, lease.ID, lease.FencingToken, lease.Attempt).Scan(&batch)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && batch != task.BatchID {
				return jobs.ErrLeaseLost
			}
			return err
		}
		if err := fence(); err != nil {
			return err
		}
		b, err := s.loadImportBatch(ctx, tx, task.BatchID)
		if err != nil {
			return err
		}
		if b.Job == nil || *b.Job != lease.ID {
			return jobs.ErrLeaseLost
		}
		rows, err := loadImportRows(ctx, tx, b.ID)
		if err != nil {
			return err
		}
		p := auth.Principal{UserID: task.ActorID, AuthVersion: task.AuthVersion}
		for i := range rows {
			r := &rows[i]
			if importTerminal(r.Summary.State) || r.Summary.State == "draft" {
				continue
			}
			// Each row gets a savepoint: validation/version failure cannot undo another
			// row's accepted child intent. Infrastructure errors still retry the batch.
			save, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			err = s.advanceImportRow(ctx, save, p, b, task, *r, lease.ID)
			if err != nil {
				if e := save.Rollback(ctx); e != nil {
					return e
				}
				if errors.Is(err, jobs.ErrLeaseLost) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				// Database failures must not be converted into a permanent camera fault.
				if !isImportDomainError(err) {
					return err
				}
				if _, err := tx.Exec(ctx, "UPDATE source_import_items SET state='failed',error_code=$2,updated_at=clock_timestamp() WHERE id=$1", r.ID, importFailureCode(err)); err != nil {
					return err
				}
			} else if err = save.Commit(ctx); err != nil {
				return err
			}
		}
		var pending, success, failed, cancelled int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE state IN ('queued','testing','running')),count(*) FILTER(WHERE state IN ('succeeded','skipped','tested')),count(*) FILTER(WHERE state='failed'),count(*) FILTER(WHERE state='cancelled') FROM source_import_items WHERE batch_id=$1`, b.ID).Scan(&pending, &success, &failed, &cancelled); err != nil {
			return err
		}
		if pending == 0 {
			state := "succeeded"
			if task.Action == "test" && !b.Cancel {
				state = "preview"
			} else if failed > 0 {
				state = "failed"
				if success > 0 {
					state = "partial"
				}
			} else if cancelled > 0 {
				state = "cancelled"
				if success > 0 {
					state = "partial"
				}
			}
			if _, err := tx.Exec(ctx, "UPDATE source_import_batches SET state=$2 WHERE id=$1", b.ID, state); err != nil {
				return err
			}
			done = true
		} else if task.Action != "test" {
			if _, err := tx.Exec(ctx, "UPDATE source_import_batches SET state='running' WHERE id=$1", b.ID); err != nil {
				return err
			}
		}
		return fence()
	})
	return done, err
}

func isImportDomainError(err error) bool {
	var f *fault.Error
	return errors.As(err, &f) || errors.Is(err, auth.ErrInvalid) || errors.Is(err, auth.ErrConflict) || errors.Is(err, auth.ErrForbidden) || errors.Is(err, auth.ErrNotFound) || errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, secrets.ErrCredentialUnavailable) || errors.Is(err, storage.ErrMediaProof)
}
func (s *SourceService) advanceImportRow(ctx context.Context, tx pgx.Tx, p auth.Principal, b importBatch, task importTask, r importRow, parent id.ID) error {
	if r.Selection == nil || r.Summary.ChannelID == nil || r.Summary.ExpectedVersion == nil {
		return auth.ErrInvalid
	}
	sel := *r.Selection
	ch := *r.Summary.ChannelID
	setState := func(state, code string) error {
		_, err := tx.Exec(ctx, "UPDATE source_import_items SET state=$2,error_code=NULLIF($3,''),updated_at=clock_timestamp() WHERE id=$1", r.ID, state, code)
		return err
	}
	if r.Switch != nil {
		var state, code string
		if err := tx.QueryRow(ctx, "SELECT state,coalesce(error_code,'') FROM source_switches WHERE id=$1", r.Switch).Scan(&state, &code); err != nil {
			return err
		}
		if state == "queued" || state == "running" {
			return nil
		}
		if state != "succeeded" {
			if code == "" {
				code = "source_change_failed"
			}
			return setState("failed", code)
		}
		// The source already reached a safe terminal state. Revoked users may not
		// perform a delayed rename; source success must still remain visible.
		if sel.ImportName {
			in, err := s.importDraft(b.ID, r)
			if err != nil {
				return err
			}
			if err := s.Auth.RequireActorChannelTx(ctx, p.UserID, p.AuthVersion, ch, auth.Configure, tx); err == nil {
				tag, err := tx.Exec(ctx, "UPDATE channels SET channel_name=$2,version=version+1 WHERE id=$1 AND current_revision_id=$3 AND version=$4 AND channel_name<>$2", ch, in.ChannelName, r.Revision, *r.Summary.ExpectedVersion+2)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					var same bool
					if err := tx.QueryRow(ctx, "SELECT channel_name=$2 FROM channels WHERE id=$1", ch, in.ChannelName).Scan(&same); err != nil {
						return err
					}
					if !same {
						if _, err := tx.Exec(ctx, `UPDATE source_import_items SET summary=jsonb_set(summary,'{warnings}',coalesce(summary->'warnings','[]'::jsonb)||'["name_update_conflict"]'::jsonb) WHERE id=$1`, r.ID); err != nil {
							return err
						}
					}
				}
			} else if !errors.Is(err, auth.ErrForbidden) {
				return err
			}
		}
		return setState("succeeded", "")
	}
	if r.Test != nil {
		var state, code string
		if err := tx.QueryRow(ctx, `SELECT CASE WHEN j.state IN ('failed','cancelled') AND t.state IN ('queued','testing') THEN 'failed' ELSE t.state END,coalesce(t.error_code,j.error_code,'') FROM source_tests t JOIN jobs j ON j.id=t.job_id WHERE t.id=$1`, r.Test).Scan(&state, &code); err != nil {
			return err
		}
		if state == "queued" || state == "testing" {
			return nil
		}
		if state != "succeeded" {
			if code == "" {
				code = "source_test_failed"
			}
			return setState("failed", code)
		}
		if b.Cancel {
			return setState("cancelled", "cancelled")
		}
		if task.Action == "test" {
			return setState("tested", "")
		}
		if err := s.Auth.RequireActorChannelTx(ctx, p.UserID, p.AuthVersion, ch, auth.Configure, tx); err != nil {
			return err
		}
		change, err := s.requestChangeTx(tx, true, ctx, p, SourceTask{ChannelID: ch, RevisionID: *r.Revision, TestID: *r.Test, ActorID: p.UserID, AuthVersion: p.AuthVersion, ExpectedVersion: *r.Summary.ExpectedVersion, FirstRecordingMode: sel.FirstRecordingMode}, "apply", fmt.Sprintf("import:%s:%d:apply", parent, r.Summary.Row))
		if err != nil {
			return err
		}
		var sw id.ID
		if err := tx.QueryRow(ctx, "SELECT id FROM source_switches WHERE job_id=$1", change.JobID).Scan(&sw); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE source_import_items SET switch_id=$2,state='running',updated_at=clock_timestamp() WHERE id=$1", r.ID, sw)
		return err
	}
	if b.Cancel {
		return setState("cancelled", "cancelled")
	}
	if err := s.Auth.RequireActorChannelTx(ctx, p.UserID, p.AuthVersion, ch, auth.Configure, tx); err != nil {
		return err
	}
	var admin bool
	if err := tx.QueryRow(ctx, "SELECT role='admin' FROM users WHERE id=$1 AND enabled AND auth_version=$2", p.UserID, p.AuthVersion).Scan(&admin); err != nil {
		return err
	}
	if !admin {
		return auth.ErrForbidden
	}
	var current *id.ID
	var version int64
	var name string
	if err := tx.QueryRow(ctx, "SELECT current_revision_id,version,channel_name FROM channels WHERE id=$1 FOR UPDATE", ch).Scan(&current, &version, &name); err != nil {
		return err
	}
	if version != sel.ExpectedVersion {
		return auth.ErrConflict
	}
	var busy bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM source_switches WHERE channel_id=$1 AND state IN ('queued','running'))", ch).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return auth.ErrConflict
	}
	in, err := s.importDraft(b.ID, r)
	if err != nil {
		return err
	}
	if err = in.Config.Validate(s.network); err != nil {
		return err
	}
	if sel.ImportName && (in.ChannelName == "" || len(in.ChannelName) > 128 || strings.ContainsAny(in.ChannelName, "\x00\r\n")) {
		return auth.ErrInvalid
	}
	input := DraftInput{Config: in.Config, IdentityIntent: sel.IdentityIntent, HistorySourceID: sel.HistorySourceID, Credentials: CredentialInput{Username: in.Username, PasswordAction: sel.PasswordAction}}
	if sel.PasswordAction != "keep" {
		input.Credentials.Password = in.Password
	}
	if sel.PasswordAction == "clear" {
		empty := ""
		input.Credentials.Password = &empty
	}
	// A true no-op (or rename alone) keeps the physical source and its history.
	// Explicit replace/history still declares a different identity and must test.
	if current != nil && sel.IdentityIntent == "modify" {
		previous, err := loadRevision(ctx, tx, ch, *current)
		if err != nil {
			return err
		}
		credentials, err := s.decrypt(previous)
		if err != nil {
			return err
		}
		username, password := credentials.Username, credentials.Password
		if input.Credentials.Username != nil {
			username = *input.Credentials.Username
		}
		if sel.PasswordAction == "clear" {
			password = ""
		} else if sel.PasswordAction == "replace" {
			password = *input.Credentials.Password
		}
		desired, err := s.protectedDigest(struct {
			Config             SourceConfig
			Username, Password string
		}{normalizedConfig(input.Config), username, password})
		if err != nil {
			return err
		}
		existing, err := s.protectedDigest(struct {
			Config             SourceConfig
			Username, Password string
		}{previous.Revision.Config, credentials.Username, credentials.Password})
		if err != nil {
			return err
		}
		if hmac.Equal(desired, existing) && task.Action != "test" {
			if sel.ImportName && name != in.ChannelName {
				if _, err := tx.Exec(ctx, "UPDATE channels SET channel_name=$2,version=version+1 WHERE id=$1", ch, in.ChannelName); err != nil {
					return err
				}
				if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "channel.renamed", ObjectID: ch}); err != nil {
					return err
				}
			}
			return setState("skipped", "")
		}
	}
	revision, err := s.createDraftTx(tx, true, ctx, p, ch, version, input, fmt.Sprintf("import:%s:%d:draft", parent, r.Summary.Row))
	if err != nil {
		return err
	}
	change, err := s.requestTestTx(tx, true, ctx, p, ch, revision.ID, fmt.Sprintf("import:%s:%d:test", parent, r.Summary.Row))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE source_import_items SET revision_id=$2,test_id=$3,expected_version=$4,state='testing',updated_at=clock_timestamp() WHERE id=$1", r.ID, revision.ID, change.TestID, version+1)
	return err
}
