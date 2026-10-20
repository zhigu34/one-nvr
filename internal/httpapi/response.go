package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/zhigu34/one-nvr/internal/archive"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"github.com/zhigu34/one-nvr/internal/storage"
	"io"
	"log/slog"
	"net/http"
	"strconv"
)

type requestKey struct{}

func respond(w http.ResponseWriter, r *http.Request, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Data      any   `json:"data"`
		RequestID id.ID `json:"request_id"`
	}{data, requestID(r)})
}
func requestID(r *http.Request) id.ID { v, _ := r.Context().Value(requestKey{}).(id.ID); return v }

// archiveFault turns an archive probe failure into a message that names the
// actual problem. The three verdicts have three different fixes, so collapsing
// them into one wording is what makes an operator re-type a correct password.
func archiveFault(err error) (error, bool) {
	return archiveFaultWithFields(err, nil)
}

// archiveFaultWithFields is the same mapping with evidence attached. The probe
// endpoint uses it so the failure's stage and HTTP status reach the page.
func archiveFaultWithFields(err error, fields map[string]string) (error, bool) {
	var mapped *fault.Error
	switch {
	case errors.Is(err, archive.ErrTargetDenied):
		mapped = fault.New(422, "archive_target_unauthorized",
			"WebDAV 服务拒绝了这组账号密码（HTTP 401）。请确认填的是这个 WebDAV 的账号。")
	case errors.Is(err, archive.ErrTargetReadOnly):
		// 403 is not a credential problem: the server accepted the credentials
		// and refused the write. Saying "password" here is what sent an operator
		// to re-type a password that demonstrably works.
		mapped = fault.New(422, "archive_target_read_only",
			"账号密码已通过（HTTP 403 而非 401），但服务端不允许往这个路径写入。常见原因：该挂载是只读的、后端存储不接受写入，或路径落在只读目录。")
	case errors.Is(err, archive.ErrRemotePathMissing):
		mapped = fault.New(422, "archive_remote_path_missing",
			"地址和账号都通了，但目标路径不存在。请检查「WebDAV URL」和「远端根目录」拼接后的位置在服务端是否真的存在。")
	case errors.Is(err, archive.ErrTargetUnavailable):
		mapped = fault.New(422, "archive_target_unreachable",
			"连不上 WebDAV 地址。请检查主机名与端口（容器内应填服务名，如 http://openlist:5244/dav，不能用 127.0.0.1）。")
	default:
		return nil, false
	}
	if len(fields) > 0 {
		mapped.Fields = fields
	}
	return mapped, true
}
func fail(w http.ResponseWriter, r *http.Request, err error) {
	var f *fault.Error
	// A request the caller abandoned is not a server fault. Browsers cancel
	// in-flight fetches on navigation and on query invalidation, and answering
	// that with "service temporarily unavailable" puts a red banner in front of
	// an operator for something that already stopped mattering. Nothing is
	// written: the connection is gone.
	if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
		return
	}
	if errors.Is(err, secrets.ErrCredentialUnavailable) {
		err = fault.New(503, "credential_unavailable", "摄像头凭据无法解密，请检查持久密钥")
	}
	// Missing pool write evidence is a failed precondition the operator can fix
	// by running a pool check, not a service outage. Report it as such instead
	// of falling through to the blanket 503 below.
	if errors.Is(err, storage.ErrMediaProof) {
		err = fault.New(409, "zlm_write_evidence_unavailable", "存储池检查未通过，请先运行存储池检查")
	}
	// Archive probes already say precisely what went wrong; turning them into a
	// generic 503 (or, worse, leaving the reason only in the log) means the
	// operator cannot tell a wrong address from a wrong password from a wrong
	// root path. Each becomes its own message with the stage it failed in.
	if mapped, ok := archiveFault(err); ok {
		err = mapped
	}
	if !errors.As(err, &f) {
		// An unrecognised error becomes a generic 503, which tells the operator
		// nothing and leaves no trace of the cause. Log it with the request path
		// so the next "503 with no explanation" can be diagnosed from the server
		// log instead of from a browser network panel.
		slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		f = fault.New(503, "dependency_unavailable", "服务暂时不可用，请稍后重试")
	}
	fields := f.Fields
	if fields == nil {
		fields = map[string]string{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(f.Status)
	_ = json.NewEncoder(w).Encode(struct {
		Error     any   `json:"error"`
		RequestID id.ID `json:"request_id"`
	}{struct {
		Code      string            `json:"code"`
		Message   string            `json:"message"`
		Fields    map[string]string `json:"fields"`
		Retryable bool              `json:"retryable"`
	}{f.Code, f.Message, fields, f.Retryable}, requestID(r)})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	return decodeLimit(w, r, v, 64<<10)
}
func decodeLimit(w http.ResponseWriter, r *http.Request, v any, maximum int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maximum)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		var syntax *json.SyntaxError
		if errors.As(err, &tooLarge) {
			return fault.New(413, "body_too_large", "请求内容超过大小限制")
		}
		if errors.As(err, &syntax) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fault.New(400, "invalid_json", "JSON 语法无效")
		}
		return auth.ErrInvalid
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return fault.New(413, "body_too_large", "请求内容超过大小限制")
		}
		return fault.New(400, "invalid_json", "只允许一个 JSON 文档")
	}
	return nil
}
func expectedVersion(r *http.Request) (int64, error) {
	s := r.Header.Get("If-Match")
	if len(s) < 3 || s[0] != '"' || s[len(s)-1] != '"' {
		return 0, auth.ErrInvalid
	}
	if s[1] < '1' || s[1] > '9' {
		return 0, auth.ErrInvalid
	}
	for _, c := range s[1 : len(s)-1] {
		if c < '0' || c > '9' {
			return 0, auth.ErrInvalid
		}
	}
	n, err := strconv.ParseInt(s[1:len(s)-1], 10, 64)
	if err != nil || n < 1 {
		return 0, auth.ErrInvalid
	}
	return n, nil
}
func pathID(r *http.Request) (id.ID, error) {
	i, err := id.Parse(r.PathValue("id"))
	if err != nil {
		return "", auth.ErrNotFound
	}
	return i, nil
}
func pagination(r *http.Request) (id.ID, int, error) {
	return paginationLimit(r, 50, 100)
}

// paginationLimit parses the shared cursor/limit convention with a
// caller-supplied default and ceiling. Recordings is the one endpoint with a
// higher ceiling (500): a 24-hour window holds ~1440 sixty-second segments, so
// the management view and the playback index page several times per search, and
// its contract declares the same maximum.
func paginationLimit(r *http.Request, fallback, max int) (id.ID, int, error) {
	limit := fallback
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > max {
			return "", 0, auth.ErrInvalid
		}
		limit = n
	}
	var cursor id.ID
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var err error
		cursor, err = id.Parse(raw)
		if err != nil {
			return "", 0, auth.ErrInvalid
		}
	}
	return cursor, limit, nil
}
