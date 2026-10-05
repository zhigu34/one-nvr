package channel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
)

// SourceTask is a non-secret durable request. The captured authorization version
// is checked at execution start; closing the browser does not discard the job.
type SourceTask struct {
	ChannelID          id.ID   `json:"channel_id"`
	RevisionID         id.ID   `json:"revision_id,omitempty"`
	ActorID            id.ID   `json:"actor_id"`
	AuthVersion        int64   `json:"auth_version"`
	TestID             id.ID   `json:"test_id,omitempty"`
	ExpectedVersion    int64   `json:"expected_version,omitempty"`
	FirstRecordingMode *string `json:"first_recording_mode,omitempty"`
}

func (s *SourceService) RequestTest(ctx context.Context, p auth.Principal, channelID, revisionID id.ID, key string) (Change, error) {
	var out Change
	if !validSourceJobKey(key) {
		return out, auth.ErrInvalid
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Configure, tx); err != nil {
			return err
		}
		if _, err := loadRevision(ctx, tx, channelID, revisionID); err != nil {
			return err
		}
		payload, _ := json.Marshal(SourceTask{ChannelID: channelID, RevisionID: revisionID, ActorID: p.UserID, AuthVersion: p.AuthVersion})
		job, err := jobs.Enqueue(ctx, tx, jobs.Input{Kind: "source.test", ObjectID: channelID, IdempotencyKey: key, Payload: payload})
		if err != nil {
			return err
		}
		testID, err := id.New()
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO source_tests(id,channel_id,revision_id,job_id,configuration_digest,created_by) SELECT $1,channel_id,id,$2,configuration_digest,$3 FROM source_revisions WHERE id=$4 AND channel_id=$5 ON CONFLICT(job_id) DO NOTHING`, testID, job, p.UserID, revisionID, channelID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "source.test_requested", ObjectID: channelID}); err != nil {
				return err
			}
		}
		out.JobID = job
		var resultID id.ID
		if err := tx.QueryRow(ctx, "SELECT id FROM source_tests WHERE job_id=$1", job).Scan(&resultID); err != nil {
			return err
		}
		out.TestID = &resultID
		return tx.QueryRow(ctx, "SELECT state FROM jobs WHERE id=$1", job).Scan(&out.State)
	})
	return out, err
}
func validSourceJobKey(key string) bool {
	return len(key) > 0 && len(key) <= 128 && !strings.ContainsAny(key, "\x00\r\n")
}
func (s *SourceService) TestResult(ctx context.Context, p auth.Principal, channelID, testID id.ID) (SourceTestResult, error) {
	var out SourceTestResult
	var raw []byte
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Configure, tx); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, "SELECT id,revision_id,state,result,observed_at,expires_at FROM source_tests WHERE id=$1 AND channel_id=$2", testID, channelID).Scan(&out.ID, &out.RevisionID, &out.State, &raw, &out.ObservedAt, &out.ExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.ErrNotFound
		}
		return err
	})
	if err != nil {
		return SourceTestResult{}, err
	}
	out.Main.State = "pending"
	out.Sub.State = "pending"
	if len(raw) > 0 {
		var streams struct{ Main, Sub StreamTest }
		if err := json.Unmarshal(raw, &streams); err != nil {
			return SourceTestResult{}, err
		}
		if streams.Main.State != "" {
			out.Main = streams.Main
		}
		if streams.Sub.State != "" {
			out.Sub = streams.Sub
		}
	}
	return out, nil
}
