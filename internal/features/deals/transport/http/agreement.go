package dealshttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Adopten123/digital-ruble-deals/internal/core/documentfile"
	httpapi "github.com/Adopten123/digital-ruble-deals/internal/core/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
)

func decodePatch(w http.ResponseWriter, r *http.Request, out *service.PatchInput) bool {
	var raw struct {
		ExpectedVersion int64           `json:"expected_version"`
		SupplierPartyID json.RawMessage `json:"supplier_party_id"`
		Title           json.RawMessage `json:"title"`
		Description     json.RawMessage `json:"description"`
		AmountKopecks   json.RawMessage `json:"amount_kopecks"`
		Currency        json.RawMessage `json:"currency"`
		DeadlineAt      json.RawMessage `json:"deadline_at"`
	}
	if !httpapi.Decode(w, r, &raw) {
		return false
	}
	out.ExpectedVersion = raw.ExpectedVersion
	for _, field := range []struct {
		raw    json.RawMessage
		target **string
	}{{raw.SupplierPartyID, &out.SupplierPartyID}, {raw.Title, &out.Title}, {raw.Description, &out.Description}, {raw.AmountKopecks, &out.AmountKopecks}, {raw.Currency, &out.Currency}, {raw.DeadlineAt, &out.DeadlineAt}} {
		if field.raw == nil {
			continue
		}
		var value string
		if bytes.Equal(bytes.TrimSpace(field.raw), []byte("null")) || json.Unmarshal(field.raw, &value) != nil {
			httpapi.Error(w, r, 400, "INVALID_REQUEST", "Изменяемые поля должны быть строками")
			return false
		}
		*field.target = &value
	}
	return true
}

func (h *Handler) command(w http.ResponseWriter, r *http.Request) (service.Actor, string, bool) {
	a := actor(r)
	if a.OrganizationID == "" {
		writeError(w, r, service.ErrNotFound)
		return a, "", false
	}
	if !h.csrf.Check(w, r) {
		return a, "", false
	}
	key := r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) != 1 || !service.ValidKey(key) {
		httpapi.Error(w, r, 400, "IDEMPOTENCY_KEY_REQUIRED", "Нужен Idempotency-Key длиной от 16 до 128 ASCII-символов без пробелов")
		return a, "", false
	}
	return a, key, true
}
func result(w http.ResponseWriter, r *http.Request, out service.Result, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(out.Status)
	_, _ = w.Write(out.Body)
}
func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
	a, key, ok := h.command(w, r)
	if !ok {
		return
	}
	// Pointers distinguish absence from an explicit null; null fields are rejected.
	var in service.PatchInput
	if !decodePatch(w, r, &in) {
		return
	}
	out, e := h.service.Patch(r.Context(), a, r.PathValue("id"), key, in)
	result(w, r, out, e)
}
func (h *Handler) agree(w http.ResponseWriter, r *http.Request) {
	a, key, ok := h.command(w, r)
	if !ok {
		return
	}
	var in service.AgreeInput
	if !httpapi.Decode(w, r, &in) {
		return
	}
	out, e := h.service.Agree(r.Context(), a, r.PathValue("id"), key, in)
	result(w, r, out, e)
}
func (h *Handler) rework(w http.ResponseWriter, r *http.Request) {
	a, key, ok := h.command(w, r)
	if !ok {
		return
	}
	var in service.ReworkInput
	if !httpapi.Decode(w, r, &in) {
		return
	}
	out, e := h.service.Rework(r.Context(), a, r.PathValue("id"), key, in)
	result(w, r, out, e)
}
func (h *Handler) generate(w http.ResponseWriter, r *http.Request) {
	a, key, ok := h.command(w, r)
	if !ok {
		return
	}
	var in service.GenerateInput
	if !httpapi.Decode(w, r, &in) {
		return
	}
	out, e := h.service.Generate(r.Context(), a, r.PathValue("id"), key, in)
	result(w, r, out, e)
}
func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	a, key, ok := h.command(w, r)
	if !ok {
		return
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "multipart/form-data" {
		httpapi.Error(w, r, 415, "UNSUPPORTED_MEDIA_TYPE", "Ожидается multipart/form-data")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, documentfile.MaxSize+(64<<10))
	reader, e := r.MultipartReader()
	if e != nil {
		httpapi.Error(w, r, 400, "INVALID_REQUEST", "Некорректный multipart запрос")
		return
	}
	var data []byte
	var version int64
	name := ""
	seen := map[string]bool{}
	fail := func(err error) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpapi.Error(w, r, 413, "PAYLOAD_TOO_LARGE", "PDF не должен превышать 5 MiB")
		} else {
			httpapi.Error(w, r, 400, "INVALID_REQUEST", "Ожидаются единственные поля file и expected_deal_version")
		}
	}
	for {
		part, e := reader.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			fail(e)
			return
		}
		field := part.FormName()
		if seen[field] || (field != "file" && field != "expected_deal_version") {
			part.Close()
			fail(errors.New("unknown or repeated part"))
			return
		}
		seen[field] = true
		if field == "file" {
			name = part.FileName()
			m, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
			if err != nil || m != "application/pdf" || strings.ToLower(filepath.Ext(name)) != ".pdf" {
				part.Close()
				httpapi.Error(w, r, 422, "INVALID_DOCUMENT", "Нужен файл .pdf с типом application/pdf")
				return
			}
			data, e = io.ReadAll(io.LimitReader(part, documentfile.MaxSize+1))
			if e != nil {
				fail(e)
				return
			}
			if len(data) > documentfile.MaxSize {
				httpapi.Error(w, r, 413, "PAYLOAD_TOO_LARGE", "PDF не должен превышать 5 MiB")
				return
			}
		} else {
			b, err := io.ReadAll(io.LimitReader(part, 32))
			if err != nil {
				fail(err)
				return
			}
			version, e = strconv.ParseInt(string(b), 10, 64)
			if e != nil || version < 1 || strconv.FormatInt(version, 10) != string(b) || part.FileName() != "" {
				part.Close()
				fail(errors.New("invalid version"))
				return
			}
		}
		part.Close()
	}
	if !seen["file"] || !seen["expected_deal_version"] {
		fail(errors.New("missing part"))
		return
	}
	out, e := h.service.Upload(r.Context(), a, r.PathValue("id"), key, version, name, data)
	result(w, r, out, e)
}
func documentVersion(r *http.Request, required bool) (*int64, error) {
	values, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return nil, e
	}
	for key, v := range values {
		if key != "version" || len(v) != 1 {
			return nil, service.ErrInvalidDocumentQuery
		}
	}
	raw, exists := values["version"]
	if !exists {
		if required {
			return nil, service.ErrInvalidDocumentQuery
		}
		return nil, nil
	}
	v, e := strconv.ParseInt(raw[0], 10, 64)
	if e != nil || v < 1 || strconv.FormatInt(v, 10) != raw[0] {
		return nil, service.ErrInvalidDocumentQuery
	}
	return &v, nil
}
func (h *Handler) document(w http.ResponseWriter, r *http.Request) {
	v, e := documentVersion(r, false)
	if e != nil {
		httpapi.Error(w, r, 400, "INVALID_REQUEST", "Некорректная версия документа")
		return
	}
	d, e := h.service.Document(r.Context(), actor(r), r.PathValue("id"), v)
	if e != nil {
		writeError(w, r, e)
		return
	}
	httpapi.JSON(w, 200, d)
}
func (h *Handler) view(w http.ResponseWriter, r *http.Request)     { h.pdf(w, r, false) }
func (h *Handler) download(w http.ResponseWriter, r *http.Request) { h.pdf(w, r, true) }
func (h *Handler) pdf(w http.ResponseWriter, r *http.Request, attachment bool) {
	v, e := documentVersion(r, true)
	if e != nil {
		httpapi.Error(w, r, 400, "INVALID_REQUEST", "Для получения PDF нужна положительная версия")
		return
	}
	d, b, e := h.service.DocumentBytes(r.Context(), actor(r), r.PathValue("id"), *v)
	if e != nil {
		writeError(w, r, e)
		return
	}
	disposition := "inline"
	if attachment {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": d.FileName}))
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; frame-ancestors 'self'")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.WriteHeader(200)
	_, _ = w.Write(b)
}
