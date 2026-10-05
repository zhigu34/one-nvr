package httpapi

import (
	"bytes"
	"io"
	"net/http"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
)

func (r *router) importPreview(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if err := r.d.Auth.RequireAdmin(q.Context(), p); err != nil {
		fail(w, q, err)
		return
	}
	q.Body = http.MaxBytesReader(w, q.Body, channel.MaxImportBytes)
	reader, err := q.MultipartReader()
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	format := ""
	var file []byte
	defer func() { clear(file) }()
	seen := map[string]bool{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(w, q, auth.ErrInvalid)
			return
		}
		name := part.FormName()
		if seen[name] || (name != "format" && name != "file") {
			part.Close()
			fail(w, q, auth.ErrInvalid)
			return
		}
		seen[name] = true
		limit := int64(channel.MaxImportBytes + 1)
		if name == "format" {
			limit = 16
		}
		data, err := io.ReadAll(io.LimitReader(part, limit))
		if err != nil {
			part.Close()
			fail(w, q, auth.ErrInvalid)
			return
		}
		if err = part.Close(); err != nil {
			clear(data)
			fail(w, q, auth.ErrInvalid)
			return
		}
		if name == "format" {
			format = string(data)
			clear(data)
		} else {
			file = data
		}
	}
	if !seen["file"] || (format != "csv" && format != "json") {
		fail(w, q, auth.ErrInvalid)
		return
	}
	out, err := r.d.Sources.PreviewImport(q.Context(), p, format, bytes.NewReader(file))
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func importBatchID(q *http.Request) (id.ID, error) {
	value, err := id.Parse(q.PathValue("batch_id"))
	if err != nil {
		return "", auth.ErrInvalid
	}
	return value, nil
}
func (r *router) importProgress(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	batch, err := importBatchID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Sources.GetImport(q.Context(), p, batch)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func (r *router) importSubmit(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	var in struct {
		BatchID id.ID                     `json:"batch_id"`
		Items   []channel.ImportSelection `json:"items"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	if _, err := id.Parse(string(in.BatchID)); err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	out, err := r.d.Sources.SubmitImport(q.Context(), p, in.BatchID, in.Items, q.Header.Get("Idempotency-Key"))
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 202, out)
}
func (r *router) importTest(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	batch, err := importBatchID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in struct {
		Rows []int `json:"rows"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Sources.TestImport(q.Context(), p, batch, in.Rows, q.Header.Get("Idempotency-Key"))
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 202, out)
}
func (r *router) importRetry(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	batch, err := importBatchID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in struct {
		Items []channel.ImportSelection `json:"items"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Sources.RetryImport(q.Context(), p, batch, in.Items, q.Header.Get("Idempotency-Key"))
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 202, out)
}
func (r *router) importCancel(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	batch, err := importBatchID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in struct{}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	if err := r.d.Sources.CancelImport(q.Context(), p, batch); err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		CancelRequested bool `json:"cancel_requested"`
	}{true})
}
