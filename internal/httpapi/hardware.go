package httpapi

import (
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/hardware"
	"net/http"
)

func (r *router) hardwareReport(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if e := r.d.Auth.RequireAdmin(q.Context(), p); e != nil {
		fail(w, q, e)
		return
	}
	if !r.d.FrigateEnabled {
		fail(w, q, fault.New(409, "feature_disabled", "功能未启用"))
		return
	}
	if r.d.TLS == nil {
		fail(w, q, fault.New(503, "hardware_not_checked", "硬件尚未检查"))
		return
	}
	report, e := hardware.ReadReport(r.d.TLS.DataDir)
	if e != nil {
		fail(w, q, fault.New(503, "hardware_not_checked", "硬件报告不可用，请重新部署检查"))
		return
	}
	respond(w, q, 200, report)
}
