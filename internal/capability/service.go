// Package capability separates deployment intent from delivered functionality.
package capability

import (
	"context"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/operations"
)

type Capability struct {
	Enabled     bool   `json:"enabled"`
	Implemented bool   `json:"implemented"`
	State       string `json:"state"`
	Reason      string `json:"reason"`
}
type Snapshot struct {
	Intelligence Capability `json:"frigate"`
	Archive      Capability `json:"cloud_archive"`
}
type Service struct {
	Operations                      *operations.Service
	FrigateEnabled, OpenListEnabled bool
}

func future(enabled bool) Capability {
	if !enabled {
		return Capability{State: "disabled", Reason: "feature_disabled"}
	}
	return Capability{Enabled: true, State: "not_available", Reason: "implementation_pending"}
}
func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	return Snapshot{Intelligence: future(s.FrigateEnabled), Archive: future(s.OpenListEnabled)}, nil
}
func (s *Service) Require(feature string) error {
	var enabled bool
	switch feature {
	case "intelligence":
		enabled = s.FrigateEnabled
	case "archive":
		enabled = s.OpenListEnabled
	default:
		return auth.ErrInvalid
	}
	if !enabled {
		return fault.New(409, "feature_disabled", "该功能未启用")
	}
	return fault.New(501, "not_implemented", "该功能尚未实现")
}
