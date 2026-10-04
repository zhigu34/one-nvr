package gatewaycontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"io"
)

type ApplyRequest struct {
	JobID            id.ID  `json:"job_id"`
	CertificateID    id.ID  `json:"certificate_id"`
	ExpectedActiveID *id.ID `json:"expected_active_id"`
	Operation        string `json:"operation"`
}
type Evidence struct {
	LeafSHA256  string `json:"leaf_sha256"`
	ChainSHA256 string `json:"chain_sha256"`
}
type ApplyResult struct {
	JobID    id.ID  `json:"job_id"`
	State    string `json:"state"`
	ActiveID *id.ID `json:"active_id"`
	Evidence
	ErrorCode string `json:"error_code"`
}
type Runtime interface {
	Prepare(context.Context, id.ID) (Evidence, error)
	Switch(context.Context, id.ID) error
	Reload(context.Context) error
	Probe(context.Context) (Evidence, error)
}

var ErrInvalid = fault.New(422, "gateway_request_invalid", "网关控制请求无效")
var ErrConflict = fault.New(409, "gateway_version_conflict", "网关当前证书已改变")
var ErrFailed = fault.New(503, "gateway_apply_failed", "证书应用或核验失败，查看应用结果")

func (r ApplyRequest) Validate() error {
	for _, value := range []id.ID{r.JobID, r.CertificateID} {
		if _, err := id.Parse(string(value)); err != nil {
			return ErrInvalid
		}
	}
	if r.ExpectedActiveID != nil {
		if _, err := id.Parse(string(*r.ExpectedActiveID)); err != nil {
			return ErrInvalid
		}
	}
	if r.Operation != "apply" && r.Operation != "rollback" {
		return ErrInvalid
	}
	return nil
}
func DecodeRequest(data []byte) (ApplyRequest, error) {
	var out ApplyRequest
	if len(data) > 16384 {
		return out, ErrInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&out) != nil {
		return out, ErrInvalid
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return out, ErrInvalid
	}
	return out, out.Validate()
}
func sameID(a, b *id.ID) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
func sameRequest(a, b ApplyRequest) bool {
	return a.JobID == b.JobID && a.CertificateID == b.CertificateID && a.Operation == b.Operation && sameID(a.ExpectedActiveID, b.ExpectedActiveID)
}
