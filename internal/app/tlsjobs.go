package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/zhigu34/one-nvr/internal/gatewaycontrol"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/tlsmanager"
	"log/slog"
	"path/filepath"
	"time"
)

func tlsApplyHandler(s *tlsmanager.Service) jobs.Handler {
	return func(ctx context.Context, lease jobs.Lease) (jobs.Result, error) {
		in, err := s.PrepareApply(ctx, lease)
		if err != nil {
			return jobs.Result{}, err
		}
		if in.CommittedResult != nil {
			if in.CommittedResult.State == "failed" {
				return jobs.Result{}, &jobs.PermanentFailure{Code: in.CommittedResult.ErrorCode}
			}
			payload, err := json.Marshal(in.CommittedResult)
			return jobs.Result{Payload: payload}, err
		}
		request := gatewaycontrol.ApplyRequest{JobID: lease.ID, CertificateID: in.CertificateID, ExpectedActiveID: in.ExpectedActiveID, Operation: in.Operation}
		mailbox := gatewaycontrol.Mailbox{Directory: filepath.Join(s.DataDir, "gateway")}
		if err = mailbox.Submit(ctx, request); err != nil {
			return jobs.Result{}, err
		}
		// A durable authorized intent survives temporary gateway downtime; renewing
		// the lease avoids falsely declaring failure while the gateway can still act.
		result, err := mailbox.Wait(ctx, request)
		if err != nil {
			return jobs.Result{}, err
		}
		evidence := tlsmanager.GatewayResult{JobID: result.JobID, State: result.State, ActiveID: result.ActiveID, LeafSHA256: result.LeafSHA256, ChainSHA256: result.ChainSHA256, ErrorCode: result.ErrorCode}
		if err = s.CommitGatewayResult(ctx, lease, evidence); err != nil {
			return jobs.Result{}, err
		}
		if result.State == "failed" {
			return jobs.Result{}, &jobs.PermanentFailure{Code: result.ErrorCode}
		}
		payload, err := json.Marshal(evidence)
		return jobs.Result{Payload: payload}, err
	}
}
func runTLSApplyJobs(ctx context.Context, s *tlsmanager.Service) {
	for ctx.Err() == nil {
		err := jobs.Run(ctx, jobs.Repository{DB: s.DB}, "tls.apply", tlsApplyHandler(s))
		if ctx.Err() != nil {
			return
		}
		if err != nil && !errors.Is(err, jobs.ErrLeaseLost) {
			slog.Warn("TLS apply queue unavailable")
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
