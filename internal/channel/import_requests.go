package channel

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/secrets"
)

// Only this private transactional entry point permits session-independent workers.
// Public requests always require a live session; workers check captured grants.
func (s *SourceService) authorizeSourceTx(ctx context.Context, p auth.Principal, ch id.ID, worker bool, tx pgx.Tx) error {
	if worker {
		return s.Auth.RequireActorChannelTx(ctx, p.UserID, p.AuthVersion, ch, auth.Configure, tx)
	}
	return s.Auth.RequireChannelTx(ctx, p, ch, auth.Configure, tx)
}

type importTask struct {
	BatchID         id.ID  `json:"batch_id"`
	ActorID         id.ID  `json:"actor_id"`
	AuthVersion     int64  `json:"auth_version"`
	Action          string `json:"action"`
	SelectionDigest string `json:"selection_digest"`
}
type importBatch struct {
	ID, Actor id.ID
	State     string
	Expires   time.Time
	Job       *id.ID
	Cancel    bool
}
type importRow struct {
	ID                     id.ID
	Summary                ImportItem
	Cipher                 secrets.EncryptedCredential
	Selection              *ImportSelection
	Revision, Test, Switch *id.ID
}

func (s *SourceService) loadImportBatch(ctx context.Context, tx pgx.Tx, batch id.ID) (importBatch, error) {
	var b importBatch
	b.ID = batch
	err := tx.QueryRow(ctx, "SELECT created_by,state,expires_at,job_id,cancel_requested FROM source_import_batches WHERE id=$1 AND site_id=$2 FOR UPDATE", batch, s.secret.SiteID).Scan(&b.Actor, &b.State, &b.Expires, &b.Job, &b.Cancel)
	if errors.Is(err, pgx.ErrNoRows) {
		err = auth.ErrNotFound
	}
	return b, err
}
func loadImportRows(ctx context.Context, tx pgx.Tx, batch id.ID) ([]importRow, error) {
	rows, err := tx.Query(ctx, `SELECT id,row_no,channel_id,expected_version,summary,state,draft_nonce,draft_ciphertext,coalesce(credential_key_id,''),selection,revision_id,test_id,switch_id,coalesce(error_code,'') FROM source_import_items WHERE batch_id=$1 ORDER BY row_no FOR UPDATE`, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []importRow{}
	for rows.Next() {
		var r importRow
		var summary, selection []byte
		var code string
		if err = rows.Scan(&r.ID, &r.Summary.Row, &r.Summary.ChannelID, &r.Summary.ExpectedVersion, &summary, &r.Summary.State, &r.Cipher.Nonce, &r.Cipher.Ciphertext, &r.Cipher.KeyID, &selection, &r.Revision, &r.Test, &r.Switch, &code); err != nil {
			return nil, err
		}
		var safe ImportItem
		if json.Unmarshal(summary, &safe) != nil {
			return nil, auth.ErrInvalid
		}
		r.Summary.Differences = safe.Differences
		r.Summary.Errors = safe.Errors
		r.Summary.Warnings = safe.Warnings
		r.Summary.PasswordAction = safe.PasswordAction
		if code != "" {
			r.Summary.Errors = []string{code}
		}
		if len(selection) > 0 {
			if json.Unmarshal(selection, &r.Selection) != nil {
				return nil, auth.ErrInvalid
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *SourceService) importDraft(b id.ID, r importRow) (ParsedItem, error) {
	raw, err := s.secret.OpenCredential(s.secret.SiteID, b, r.ID, "import_draft", r.Cipher)
	if err != nil {
		return ParsedItem{}, err
	}
	defer clear(raw)
	var in ParsedItem
	err = json.Unmarshal(raw, &in)
	return in, err
}
func (s *SourceService) GetImport(ctx context.Context, p auth.Principal, batch id.ID) (ImportProgress, error) {
	out := ImportProgress{BatchID: batch, Items: []ImportItem{}}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		b, err := s.loadImportBatch(ctx, tx, batch)
		if err != nil {
			return err
		}
		out.State = b.State
		out.ExpiresAt = b.Expires
		rows, err := loadImportRows(ctx, tx, batch)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Summary.ChannelID != nil {
				if err := s.Auth.RequireChannelTx(ctx, p, *r.Summary.ChannelID, auth.Configure, tx); err != nil {
					return err
				}
			}
			if r.Switch != nil {
				var job id.ID
				if err := tx.QueryRow(ctx, "SELECT job_id FROM source_switches WHERE id=$1", r.Switch).Scan(&job); err != nil {
					return err
				}
				r.Summary.JobID = &job
			} else if r.Test != nil {
				var job id.ID
				if err := tx.QueryRow(ctx, "SELECT job_id FROM source_tests WHERE id=$1", r.Test).Scan(&job); err != nil {
					return err
				}
				r.Summary.JobID = &job
			}
			if r.Summary.ChannelID != nil {
				var current int64
				if err := tx.QueryRow(ctx, "SELECT version FROM channels WHERE id=$1", r.Summary.ChannelID).Scan(&current); err != nil {
					return err
				}
				r.Summary.ExpectedVersion = &current
			}
			out.Items = append(out.Items, r.Summary)
		}
		return nil
	})
	if err != nil {
		return ImportProgress{}, err
	}
	return out, nil
}
func (s *SourceService) SubmitImport(ctx context.Context, p auth.Principal, batch id.ID, items []ImportSelection, key string) (Change, error) {
	return s.requestImport(ctx, p, batch, items, "apply", key)
}

// A retry explicitly acknowledges a current channel version and identity choice.
// Successful/skipped rows cannot be selected and never replay.
func (s *SourceService) RetryImport(ctx context.Context, p auth.Principal, batch id.ID, items []ImportSelection, key string) (Change, error) {
	return s.requestImport(ctx, p, batch, items, "retry", key)
}
func (s *SourceService) TestImport(ctx context.Context, p auth.Principal, batch id.ID, numbers []int, key string) (Change, error) {
	var selections []ImportSelection
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		if _, err := s.loadImportBatch(ctx, tx, batch); err != nil {
			return err
		}
		var expired bool
		if err := tx.QueryRow(ctx, "SELECT expires_at<=clock_timestamp() FROM source_import_batches WHERE id=$1", batch).Scan(&expired); err != nil {
			return err
		}
		if expired {
			return invalidSource("import_expired", "导入预览已过期，请重新上传")
		}

		rows, err := loadImportRows(ctx, tx, batch)
		if err != nil {
			return err
		}
		seen := map[int]bool{}
		for _, n := range numbers {
			if seen[n] {
				return auth.ErrInvalid
			}
			seen[n] = true
			found := false
			for _, r := range rows {
				if r.Summary.Row != n {
					continue
				}
				found = true
				if r.Summary.ChannelID == nil || r.Summary.ExpectedVersion == nil {
					return auth.ErrInvalid
				}
				in, err := s.importDraft(batch, r)
				if err != nil {
					return err
				}
				var current *id.ID
				if err := tx.QueryRow(ctx, "SELECT current_revision_id FROM channels WHERE id=$1", r.Summary.ChannelID).Scan(&current); err != nil {
					return err
				}
				intent := "modify"
				if current == nil {
					intent = "replace"
				}
				selections = append(selections, ImportSelection{Row: n, ChannelID: *r.Summary.ChannelID, ExpectedVersion: *r.Summary.ExpectedVersion, IdentityIntent: intent, PasswordAction: in.PasswordAction})
			}
			if !found {
				return auth.ErrInvalid
			}
		}
		return nil
	})
	if err != nil {
		return Change{}, err
	}
	return s.requestImport(ctx, p, batch, selections, "test", key)
}
func (s *SourceService) requestImport(ctx context.Context, p auth.Principal, batch id.ID, items []ImportSelection, action, key string) (Change, error) {
	var out Change
	if !validSourceJobKey(key) || len(items) < 1 || len(items) > MaxImportRows {
		return out, auth.ErrInvalid
	}
	selections := append([]ImportSelection(nil), items...)
	sort.Slice(selections, func(i, j int) bool { return selections[i].Row < selections[j].Row })
	var digestInput any = selections
	if action == "test" {
		numbers := []int{}
		for _, sel := range selections {
			numbers = append(numbers, sel.Row)
		}
		digestInput = numbers
	}
	digest, err := s.protectedDigest(digestInput)
	if err != nil {
		return out, err
	}
	raw, _ := json.Marshal(importTask{batch, p.UserID, p.AuthVersion, action, hex.EncodeToString(digest)})
	err = s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		b, err := s.loadImportBatch(ctx, tx, batch)
		if err != nil {
			return err
		}
		var previous bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM jobs WHERE kind='source.import' AND idempotency_key=$1)", key).Scan(&previous); err != nil {
			return err
		}
		job, err := jobs.Enqueue(ctx, tx, jobs.Input{Kind: "source.import", ObjectID: batch, IdempotencyKey: key, Payload: raw})
		if err != nil {
			return err
		}
		out.JobID = job
		if previous {
			return tx.QueryRow(ctx, "SELECT state FROM jobs WHERE id=$1", job).Scan(&out.State)
		}
		// Clock belongs to PostgreSQL, avoiding API/worker clock skew at expiry.
		var expired bool
		if err := tx.QueryRow(ctx, "SELECT expires_at<=clock_timestamp() FROM source_import_batches WHERE id=$1", batch).Scan(&expired); err != nil {
			return err
		}
		if expired {
			return invalidSource("import_expired", "导入预览已过期，请重新上传")
		}
		if b.Job != nil {
			var state string
			if err := tx.QueryRow(ctx, "SELECT state FROM jobs WHERE id=$1", b.Job).Scan(&state); err != nil {
				return err
			}
			if state == "queued" || state == "running" {
				return auth.ErrConflict
			}
		}
		if b.Cancel {
			return auth.ErrConflict
		}
		if action == "retry" {
			if b.State != "partial" && b.State != "failed" {
				return auth.ErrConflict
			}
		} else if b.State != "preview" {
			return auth.ErrConflict
		}
		rows, err := loadImportRows(ctx, tx, batch)
		if err != nil {
			return err
		}

		targetByRow := map[int]id.ID{}
		selectedRows := []int{}
		for _, sel := range selections {
			if _, duplicate := targetByRow[sel.Row]; duplicate {
				return auth.ErrInvalid
			}
			targetByRow[sel.Row] = sel.ChannelID
			selectedRows = append(selectedRows, sel.Row)
		}
		finalTargets := map[id.ID]bool{}
		for _, row := range rows {
			target, selected := targetByRow[row.Summary.Row]
			if !selected && row.Summary.ChannelID != nil {
				target = *row.Summary.ChannelID
			}
			if target == "" {
				continue
			}
			if finalTargets[target] {
				return invalidSource("duplicate_import_target", "导入存在重复目标通道")
			}
			finalTargets[target] = true
		}
		// Clear only selected draft mappings inside this transaction so a unique
		// target permutation does not transiently violate the uniqueness index.
		if _, err := tx.Exec(ctx, "UPDATE source_import_items SET channel_id=NULL WHERE batch_id=$1 AND row_no=ANY($2)", batch, selectedRows); err != nil {
			return err
		}
		targets := map[id.ID]bool{}
		numbers := map[int]bool{}
		for _, sel := range selections {
			if sel.Row < 1 || sel.Row > 32 || sel.ExpectedVersion < 1 || numbers[sel.Row] || targets[sel.ChannelID] {
				return auth.ErrInvalid
			}
			numbers[sel.Row] = true
			targets[sel.ChannelID] = true
			if err := s.Auth.RequireChannelTx(ctx, p, sel.ChannelID, auth.Configure, tx); err != nil {
				return err
			}
			if sel.IdentityIntent != "modify" && sel.IdentityIntent != "replace" && sel.IdentityIntent != "history" {
				return auth.ErrInvalid
			}
			if sel.IdentityIntent == "history" {
				if _, err := id.Parse(string(sel.HistorySourceID)); err != nil {
					return auth.ErrInvalid
				}
			} else if sel.HistorySourceID != "" {
				return auth.ErrInvalid
			}
			if sel.FirstRecordingMode != nil && *sel.FirstRecordingMode != "none" && *sel.FirstRecordingMode != "continuous" {
				return auth.ErrInvalid
			}
			if sel.PasswordAction != "keep" && sel.PasswordAction != "replace" && sel.PasswordAction != "clear" {
				return auth.ErrInvalid
			}
			found := false
			for _, row := range rows {
				if row.Summary.Row != sel.Row {
					continue
				}
				found = true
				if row.Summary.State == "skipped" || row.Summary.State == "succeeded" || row.Summary.State == "running" || row.Summary.State == "testing" {
					return auth.ErrConflict
				}
				if action == "retry" && row.Summary.State != "failed" {
					return auth.ErrConflict
				}
				// Same-site UUID exports cannot be redirected by a mapping confirmation.
				input, err := s.importDraft(batch, row)
				if err != nil {
					return err
				}
				if input.SiteID == s.secret.SiteID && (input.ChannelID == nil || *input.ChannelID != sel.ChannelID) {
					return auth.ErrConflict
				}
				if input.SourceState == "not_configured" {
					return auth.ErrInvalid
				}
				if sel.PasswordAction == "replace" && (input.Password == nil || *input.Password == "") {
					return auth.ErrInvalid
				}
				selected, _ := json.Marshal(sel)
				if _, err := tx.Exec(ctx, "UPDATE source_import_items SET selection=$2,channel_id=$3,expected_version=$4,state='queued',revision_id=NULL,test_id=NULL,switch_id=NULL,error_code=NULL,summary=jsonb_set(summary,'{errors}','[]'::jsonb),updated_at=clock_timestamp() WHERE id=$1", row.ID, selected, sel.ChannelID, sel.ExpectedVersion); err != nil {
					return err
				}
			}
			if !found {
				return auth.ErrInvalid
			}
		}
		state := "submitted"
		if action == "test" {
			state = "preview"
		}
		if _, err := tx.Exec(ctx, "UPDATE source_import_batches SET job_id=$2,state=$3,submitted_at=clock_timestamp(),cancel_requested=false WHERE id=$1", batch, job, state); err != nil {
			return err
		}
		if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "source.import_" + action + "_requested", ObjectID: batch}); err != nil {
			return err
		}
		out.State = "queued"
		return nil
	})
	return out, err
}
func (s *SourceService) CancelImport(ctx context.Context, p auth.Principal, batch id.ID) error {
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		b, err := s.loadImportBatch(ctx, tx, batch)
		if err != nil {
			return err
		}
		rows, err := loadImportRows(ctx, tx, batch)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Summary.ChannelID != nil {
				if err := s.Auth.RequireChannelTx(ctx, p, *r.Summary.ChannelID, auth.Configure, tx); err != nil {
					return err
				}
			}
		}
		if b.State == "succeeded" || b.State == "cancelled" {
			return nil
		}
		if _, err := tx.Exec(ctx, "UPDATE source_import_batches SET cancel_requested=true WHERE id=$1", batch); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE source_import_items SET state='cancelled',error_code='cancelled',updated_at=clock_timestamp() WHERE batch_id=$1 AND state IN ('draft','queued','tested')", batch); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE source_import_batches SET state='cancelled' WHERE id=$1 AND NOT EXISTS(SELECT 1 FROM source_import_items WHERE batch_id=$1 AND state IN ('testing','running'))", batch); err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "source.import_cancelled", ObjectID: batch})
	})
}
