package recording

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
)

type physicalSession struct {
	ID, ChannelID, RevisionID id.ID
	Key                       zlm.StreamKey
	State                     string
}

const physicalColumns = `id,channel_id,source_revision_id,vhost,app,stream,state`

func scanSession(row pgx.Row) (physicalSession, error) {
	var ss physicalSession
	err := row.Scan(&ss.ID, &ss.ChannelID, &ss.RevisionID, &ss.Key.VHost, &ss.Key.App, &ss.Key.Stream, &ss.State)
	return ss, err
}
func (s *Service) testSession(ctx context.Context, e *channel.Execution, work channel.TestWork, kind string) (physicalSession, error) {
	var out physicalSession
	sessionID, err := id.New()
	if err != nil {
		return out, err
	}
	err = e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var generation int64
		if err := tx.QueryRow(ctx, `UPDATE channels SET source_generation=greatest(source_generation,(SELECT coalesce(max(generation),0) FROM stream_sessions WHERE channel_id=$1))+1 WHERE id=$1 RETURNING source_generation`, work.Task.ChannelID).Scan(&generation); err != nil {
			return err
		}
		key := zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(sessionID)}
		if _, err := tx.Exec(ctx, `INSERT INTO stream_sessions(id,channel_id,source_revision_id,generation,vhost,app,stream,purpose,source_test_id,operation_role,proxy_key) VALUES($1,$2,$3,$4,$5,$6,$7,'test',$8,$9,$10)`, sessionID, work.Task.ChannelID, work.Task.RevisionID, generation, key.VHost, key.App, key.Stream, work.TestID, "test_"+kind, key.VHost+"/"+key.App+"/"+key.Stream); err != nil {
			return err
		}
		out = physicalSession{ID: sessionID, ChannelID: work.Task.ChannelID, RevisionID: work.Task.RevisionID, Key: key, State: "starting"}
		return nil
	})
	return out, err
}
func (s *Service) closeTestSession(ctx context.Context, e *channel.Execution, ss physicalSession) error {
	if err := e.Check(ctx); err != nil {
		return err
	}
	ref := zlm.ProxyRef{Key: ss.Key, OpaqueKey: ss.Key.VHost + "/" + ss.Key.App + "/" + ss.Key.Stream}
	if err := s.Media.RemoveProxy(ctx, ref); err != nil && !errors.Is(err, zlm.ErrProxyAbsent) {
		return err
	}
	return e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE stream_sessions SET state='closed',closed_at=clock_timestamp() WHERE id=$1 AND channel_id=$2`, ss.ID, e.ChannelID)
		return err
	})
}
func (s *Service) cleanTestSessions(ctx context.Context, e *channel.Execution, testID id.ID) error {
	rows, err := s.DB.Pool.Query(ctx, "SELECT "+physicalColumns+" FROM stream_sessions WHERE source_test_id=$1 AND state NOT IN ('closed','failed') ORDER BY created_at", testID)
	if err != nil {
		return err
	}
	var sessions []physicalSession
	for rows.Next() {
		ss, err := scanSession(rows)
		if err != nil {
			rows.Close()
			return err
		}
		sessions = append(sessions, ss)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, ss := range sessions {
		if err := s.closeTestSession(ctx, e, ss); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) checkTestStream(ctx context.Context, e *channel.Execution, work channel.TestWork, kind string) (channel.StreamTest, error) {
	out := channel.StreamTest{State: "unavailable", Reason: "source_unavailable"}
	ss, err := s.testSession(ctx, e, work, kind)
	if err != nil {
		return out, err
	}
	// A private mapped session is durable before the first external operation.
	if err := e.Check(ctx); err != nil {
		return out, err
	}
	network, err := s.FreshNetwork(ctx)
	if err != nil {
		return out, err
	}
	input, err := s.Sources.PrivateConnection(ctx, work.Task.ChannelID, work.Task.RevisionID, kind, network)
	if err != nil {
		return out, err
	}
	input.Key = ss.Key
	if err := e.Check(ctx); err != nil {
		return out, err
	}
	_, addErr := s.Media.AddProxy(ctx, input)
	if addErr == nil {
		if err := e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "UPDATE stream_sessions SET state='active',started_at=clock_timestamp() WHERE id=$1", ss.ID)
			return err
		}); err != nil {
			return out, err
		}
		privateURL, err := probe.InternalURL(ss.Key, s.ProbeToken)
		if err != nil {
			return out, err
		}
		if err := e.Check(ctx); err != nil {
			return out, err
		}
		video, frameErr := s.Probe.FirstFrame(ctx, privateURL)
		if frameErr == nil && video.FirstFrame {
			if err := e.Check(ctx); err != nil {
				return out, err
			}
			snapshot, err := s.Media.Inspect(ctx, ss.Key)
			if err == nil && !snapshot.Recording {
				out = channel.StreamTest{State: "healthy", FirstFrame: true, Codec: video.Codec, Width: video.Width, Height: video.Height, FPS: video.FPS}
			}
		}
	}
	// A false source result is safe to commit only after its private proxy is
	// confirmed removed. A failed cleanup remains retryable and mapped.
	if err := s.closeTestSession(ctx, e, ss); err != nil {
		return out, err
	}
	return out, nil
}
func testJobResult(result channel.SourceTestResult) (jobs.Result, error) {
	if result.State != "succeeded" {
		code := "main_source_unavailable"
		if result.State == "cancelled" {
			code = "authorization_revoked"
		}
		return jobs.Result{}, &jobs.PermanentFailure{Code: code}
	}
	raw, err := json.Marshal(result)
	return jobs.Result{Payload: raw}, err
}
func (s *Service) ExecuteSourceTest(ctx context.Context, lease jobs.Lease) (jobs.Result, error) {
	if s.Sources == nil || s.Sources.Auth == nil || s.Media == nil || s.Probe == nil || s.FreshNetwork == nil {
		return jobs.Result{}, ErrPublicationUnavailable
	}
	task, err := channel.DecodeSourceTask(lease.Payload)
	if err != nil {
		return jobs.Result{}, err
	}
	execution, err := channel.NewExecution(ctx, s.DB, lease, task.ChannelID)
	if err != nil {
		return jobs.Result{}, err
	}
	defer execution.Close()
	ctx = execution.Context()
	work, err := s.Sources.PrepareTest(ctx, execution)
	if err != nil {
		return jobs.Result{}, err
	}
	if work.Completed != nil {
		return testJobResult(*work.Completed)
	}
	if err := s.cleanTestSessions(ctx, execution, work.TestID); err != nil {
		return jobs.Result{}, err
	}
	main, err := s.checkTestStream(ctx, execution, work, "main")
	if err != nil {
		return jobs.Result{}, err
	}
	sub := channel.StreamTest{State: "not_configured"}
	if work.Config.SubPath != "" {
		sub = channel.StreamTest{State: "pending", Reason: "main_unavailable"}
		if main.FirstFrame {
			sub, err = s.checkTestStream(ctx, execution, work, "sub")
			if err != nil {
				return jobs.Result{}, err
			}
		}
	}
	result, err := s.Sources.FinishTest(ctx, execution, work, main, sub)
	if err != nil {
		return jobs.Result{}, err
	}
	return testJobResult(result)
}
