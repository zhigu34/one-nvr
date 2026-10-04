package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"io"
	"log/slog"
	"time"
)

func (s *Service) Sample(ctx context.Context, service string) error {
	pools, err := s.pools(ctx)
	if err != nil {
		return err
	}
	for _, pool := range pools {
		if err := s.SamplePool(ctx, pool, service); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) Monitor(ctx context.Context, service string) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		sample, cancel := context.WithTimeout(ctx, 8*time.Second)
		err := s.Sample(sample, service)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("storage checks unavailable", "service", service)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) HandleCheck(ctx context.Context, lease jobs.Lease) (jobs.Result, error) {
	var in struct {
		PoolID id.ID `json:"pool_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(lease.Payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		return jobs.Result{}, auth.ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return jobs.Result{}, auth.ErrInvalid
	}
	if _, err := id.Parse(string(in.PoolID)); err != nil {
		return jobs.Result{}, auth.ErrInvalid
	}
	pool, err := scanPool(s.DB.Pool.QueryRow(ctx, "SELECT "+poolColumns+" FROM storage_pools WHERE id=$1", in.PoolID))
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Result{Payload: []byte(`{"state":"pool_removed"}`)}, nil
	}
	if err != nil {
		return jobs.Result{}, err
	}
	if err := s.SamplePool(ctx, pool, "worker"); err != nil {
		return jobs.Result{}, err
	}
	if s.MediaCheck != nil {
		err := s.MediaCheck(ctx, in.PoolID)
		if err != nil {
			if ctx.Err() != nil {
				return jobs.Result{}, ctx.Err()
			}
			state, reason := "unavailable", "zlm_media_probe_failed"
			if errors.Is(err, zlm.ErrTestSourceRequired) {
				state, reason = "pending", "test_source_required"
			}
			now := time.Now().UTC()
			if _, writeErr := s.DB.Pool.Exec(ctx, `INSERT INTO storage_pool_checks(pool_id,service,state,reason_code,observed_at,expires_at,total_bytes,free_bytes) VALUES($1,'zlm',$2,$3,$4,$5,0,0) ON CONFLICT(pool_id,service) DO UPDATE SET state=EXCLUDED.state,reason_code=EXCLUDED.reason_code,observed_at=EXCLUDED.observed_at,expires_at=EXCLUDED.expires_at WHERE storage_pool_checks.observed_at<=EXCLUDED.observed_at`, in.PoolID, state, reason, now, now.Add(30*time.Second)); writeErr != nil {
				return jobs.Result{}, writeErr
			}
		}
	}
	// A completed check job means observation was stored, not that ZLM can record.
	return jobs.Result{Payload: []byte(`{"state":"checked","service":"worker"}`)}, nil
}

func (s *Service) RunJobs(ctx context.Context) {
	for ctx.Err() == nil {
		err := jobs.Run(ctx, jobs.Repository{DB: s.DB}, "storage.check", s.HandleCheck)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("storage check queue unavailable")
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
