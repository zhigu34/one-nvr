package channel

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
)

type TestWork struct {
	Task      SourceTask
	TestID    id.ID
	Config    SourceConfig
	Completed *SourceTestResult
}

func DecodeSourceTask(raw []byte) (SourceTask, error) {
	var task SourceTask
	if len(raw) > 2048 {
		return task, auth.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&task) != nil {
		return task, auth.ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return task, auth.ErrInvalid
	}
	for _, value := range []id.ID{task.ChannelID, task.ActorID} {
		if _, err := id.Parse(string(value)); err != nil {
			return task, auth.ErrInvalid
		}
	}
	for _, value := range []id.ID{task.RevisionID, task.TestID} {
		if value != "" {
			if _, err := id.Parse(string(value)); err != nil {
				return task, auth.ErrInvalid
			}
		}
	}
	if task.AuthVersion < 1 {
		return task, auth.ErrInvalid
	}
	return task, nil
}
func (s *SourceService) PrepareTest(ctx context.Context, e *Execution) (TestWork, error) {
	var out TestWork
	task, err := DecodeSourceTask(e.Lease.Payload)
	if err != nil {
		return out, err
	}
	out.Task = task
	if task.ChannelID != e.ChannelID || task.RevisionID == "" || e.Lease.Kind != "source.test" {
		return out, auth.ErrInvalid
	}
	err = e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var slot int
		if err := tx.QueryRow(ctx, "SELECT slot FROM source_test_slots WHERE job_id=$1 AND fencing_token=$2", e.Lease.ID, e.Lease.FencingToken).Scan(&slot); err != nil {
			return jobs.ErrLeaseLost
		}
		var state string
		var digest, stored, raw []byte
		var observed, expires *time.Time
		if err := tx.QueryRow(ctx, "SELECT id,state,configuration_digest,result,observed_at,expires_at FROM source_tests WHERE job_id=$1 AND channel_id=$2 AND revision_id=$3 FOR UPDATE", e.Lease.ID, task.ChannelID, task.RevisionID).Scan(&out.TestID, &state, &digest, &raw, &observed, &expires); err != nil {
			return err
		}
		if state == "succeeded" || state == "failed" || state == "cancelled" {
			result := SourceTestResult{ID: out.TestID, RevisionID: task.RevisionID, State: state, ObservedAt: observed, ExpiresAt: expires}
			var streams struct{ Main, Sub StreamTest }
			if err := json.Unmarshal(raw, &streams); err != nil {
				return err
			}
			result.Main = streams.Main
			result.Sub = streams.Sub
			out.Completed = &result
			return nil
		}
		if state == "queued" {
			if err := s.Auth.RequireActorChannelTx(ctx, task.ActorID, task.AuthVersion, task.ChannelID, auth.Configure, tx); err != nil {
				if !errors.Is(err, auth.ErrForbidden) {
					return err
				}
				if _, err := tx.Exec(ctx, "UPDATE source_tests SET state='cancelled',error_code='authorization_revoked' WHERE id=$1", out.TestID); err != nil {
					return err
				}
				out.Completed = &SourceTestResult{ID: out.TestID, RevisionID: task.RevisionID, State: "cancelled", Main: StreamTest{State: "pending"}, Sub: StreamTest{State: "pending"}}
				return nil
			}
		}
		if err := tx.QueryRow(ctx, "SELECT configuration_digest FROM source_revisions WHERE id=$1 AND channel_id=$2", task.RevisionID, task.ChannelID).Scan(&stored); err != nil {
			return err
		}
		if !hmac.Equal(digest, stored) {
			return auth.ErrConflict
		}
		revision, err := loadRevision(ctx, tx, task.ChannelID, task.RevisionID)
		if err != nil {
			return err
		}
		out.Config = revision.Revision.Config
		_, err = tx.Exec(ctx, "UPDATE source_tests SET state='testing',error_code=NULL WHERE id=$1", out.TestID)
		return err
	})
	return out, err
}
func (s *SourceService) FinishTest(ctx context.Context, e *Execution, work TestWork, main, sub StreamTest) (SourceTestResult, error) {
	observed := time.Now().UTC()
	expires := observed.Add(5 * time.Minute)
	state, code := "succeeded", ""
	if !main.FirstFrame || main.State != "healthy" {
		state, code = "failed", "main_source_unavailable"
	}
	out := SourceTestResult{ID: work.TestID, RevisionID: work.Task.RevisionID, State: state, Main: main, Sub: sub, ObservedAt: &observed, ExpiresAt: &expires}
	raw, _ := json.Marshal(struct {
		Main StreamTest `json:"main"`
		Sub  StreamTest `json:"sub"`
	}{main, sub})
	err := e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE source_tests SET state=$2,result=$3,observed_at=$4,expires_at=$5,error_code=NULLIF($6,'') WHERE id=$1 AND job_id=$7 AND state='testing'`, work.TestID, state, raw, observed, expires, code, e.Lease.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return jobs.ErrLeaseLost
		}
		return nil
	})
	return out, err
}
