package recording

import (
	"context"
	"encoding/json"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"time"
)

func (s *Service) ExecuteImport(ctx context.Context, lease jobs.Lease) (jobs.Result, error) {
	if s.Sources == nil {
		return jobs.Result{}, ErrPublicationUnavailable
	}
	for {
		done, err := s.Sources.AdvanceImport(ctx, lease)
		if err != nil {
			return jobs.Result{}, err
		}
		if done {
			return jobs.Result{Payload: json.RawMessage(`{"state":"finished"}`)}, nil
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return jobs.Result{}, ctx.Err()
		case <-timer.C:
		}
	}
}
