package httpapi

import (
	"encoding/json"
	"errors"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"io"
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
func fail(w http.ResponseWriter, r *http.Request, err error) {
	var f *fault.Error
	if errors.Is(err, secrets.ErrCredentialUnavailable) {
		err = fault.New(503, "credential_unavailable", "摄像头凭据无法解密，请检查持久密钥")
	}
	if !errors.As(err, &f) {
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
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
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
