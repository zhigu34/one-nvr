package tlsmanager

import (
	"context"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"log/slog"
	"time"
)

func (s *Service) Monitor(ctx context.Context) {
	if s.InputDir == "" {
		return
	}
	delay := time.Duration(0)
	for ctx.Err() == nil {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		check, cancel := context.WithTimeout(ctx, 8*time.Second)
		_, err := s.CheckDirectory(check)
		cancel()
		delay = 10 * time.Second
		if err != nil {
			delay = 30 * time.Second
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("TLS directory check unavailable")
		}
	}
}
func (s *Service) HandleDirectoryCheck(ctx context.Context, _ jobs.Lease) (jobs.Result, error) {
	result, err := s.CheckDirectory(ctx)
	if err != nil {
		return jobs.Result{}, err
	}
	if result.State == "stabilizing" {
		return jobs.Result{}, validationError("tls_input_stabilizing")
	}
	// Unavailable input is a completed diagnostic, not a successful application.
	return jobs.Result{Payload: []byte(`{"state":"` + result.State + `","reason":"` + result.Reason + `"}`)}, nil
}
func (s *Service) RunCheckJobs(ctx context.Context) {
	if s.InputDir == "" {
		return
	}
	for ctx.Err() == nil {
		err := jobs.Run(ctx, jobs.Repository{DB: s.DB}, "tls.check", s.HandleDirectoryCheck)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("TLS check queue unavailable")
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
