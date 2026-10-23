package dealshttp

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	httpapi "github.com/Adopten123/digital-ruble-deals/internal/core/transport/http"
	authhttp "github.com/Adopten123/digital-ruble-deals/internal/features/auth/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
)

type Service interface {
	Create(context.Context, service.Actor, string, service.Input) (service.Result, error)
	List(context.Context, service.Actor, service.Filters, string) (service.Page, error)
	Get(context.Context, service.Actor, string) (service.Deal, error)
	Patch(context.Context, service.Actor, string, string, service.PatchInput) (service.Result, error)
	Agree(context.Context, service.Actor, string, string, service.AgreeInput) (service.Result, error)
	Rework(context.Context, service.Actor, string, string, service.ReworkInput) (service.Result, error)
	Generate(context.Context, service.Actor, string, string, service.GenerateInput) (service.Result, error)
	Upload(context.Context, service.Actor, string, string, int64, string, []byte) (service.Result, error)
	Document(context.Context, service.Actor, string, *int64) (service.Document, error)
	DocumentBytes(context.Context, service.Actor, string, int64) (service.Document, []byte, error)
	Reserve(context.Context, service.Actor, string, string, service.ReserveInput) (service.Result, error)
	Sign(context.Context, service.Actor, string, string, service.SignInput) (service.Result, error)
}
type Handler struct {
	service Service
	csrf    *httpapi.CSRF
}

func New(s Service, csrf *httpapi.CSRF) *Handler { return &Handler{service: s, csrf: csrf} }
func (h *Handler) RegisterRoutes(mux *http.ServeMux, require func(http.Handler) http.Handler) {
	mux.Handle("POST /api/v1/deals", require(http.HandlerFunc(h.create)))
	mux.Handle("GET /api/v1/deals", require(http.HandlerFunc(h.list)))
	mux.Handle("GET /api/v1/deals/{id}", require(http.HandlerFunc(h.get)))
	mux.Handle("PATCH /api/v1/deals/{id}", require(http.HandlerFunc(h.patch)))
	mux.Handle("POST /api/v1/deals/{id}/agree", require(http.HandlerFunc(h.agree)))
	mux.Handle("POST /api/v1/deals/{id}/rework", require(http.HandlerFunc(h.rework)))
	mux.Handle("POST /api/v1/deals/{id}/reserve", require(http.HandlerFunc(h.reserve)))
	mux.Handle("POST /api/v1/deals/{id}/documents/generate", require(http.HandlerFunc(h.generate)))
	mux.Handle("POST /api/v1/deals/{id}/documents", require(http.HandlerFunc(h.upload)))
	mux.Handle("GET /api/v1/documents/{id}", require(http.HandlerFunc(h.document)))
	mux.Handle("POST /api/v1/documents/{id}/sign", require(http.HandlerFunc(h.sign)))
	mux.Handle("GET /api/v1/documents/{id}/view", require(http.HandlerFunc(h.view)))
	mux.Handle("GET /api/v1/documents/{id}/download", require(http.HandlerFunc(h.download)))
}
func actor(r *http.Request) service.Actor {
	identity, ok := authhttp.CurrentIdentity(r.Context())
	if !ok || identity.Organization == nil || identity.Organization.Role != "ORG_OWNER" || identity.User.SystemRole != "USER" {
		return service.Actor{}
	}
	return service.Actor{UserID: identity.User.ID, OrganizationID: identity.Organization.ID, RequestID: httpapi.RequestID(r.Context())}
}
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if a.OrganizationID == "" {
		writeError(w, r, service.ErrForbidden)
		return
	}
	if !h.csrf.Check(w, r) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) != 1 || !service.ValidKey(key) {
		httpapi.Error(w, r, 400, "IDEMPOTENCY_KEY_REQUIRED", "Нужен Idempotency-Key длиной от 16 до 128 ASCII-символов без пробелов")
		return
	}
	var in struct {
		SupplierPartyID *string `json:"supplier_party_id"`
		Title           *string `json:"title"`
		Description     *string `json:"description"`
		AmountKopecks   *string `json:"amount_kopecks"`
		Currency        *string `json:"currency"`
		DeadlineAt      *string `json:"deadline_at"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if in.SupplierPartyID == nil || in.Title == nil || in.Description == nil || in.AmountKopecks == nil || in.Currency == nil || in.DeadlineAt == nil {
		httpapi.Error(w, r, 400, "INVALID_REQUEST", "Все поля создания должны быть строками")
		return
	}
	result, err := h.service.Create(r.Context(), a, key, service.Input{SupplierPartyID: *in.SupplierPartyID, Title: *in.Title, Description: *in.Description, AmountKopecks: *in.AmountKopecks, Currency: *in.Currency, DeadlineAt: *in.DeadlineAt})
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(result.Status)
	_, _ = w.Write(result.Body)
}
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	out, err := h.service.Get(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpapi.JSON(w, 200, out)
}
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if a.OrganizationID == "" {
		writeError(w, r, service.ErrForbidden)
		return
	}
	params, err := httpQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out, err := h.service.List(r.Context(), a, params, r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpapi.JSON(w, 200, out)
}
func httpQuery(r *http.Request) (service.Filters, error) {
	values, err := r.URL.Query(), error(nil)
	// ParseQuery reports malformed escaping; URL.Query alone silently discards it.
	if err = r.ParseForm(); err != nil {
		return service.Filters{}, service.ErrInvalidQuery
	}
	for key, v := range values {
		if len(v) != 1 {
			return service.Filters{}, service.ErrInvalidQuery
		}
		switch key {
		case "q", "status", "role", "created_from", "created_to", "limit", "cursor":
		default:
			return service.Filters{}, service.ErrInvalidQuery
		}
	}
	f := service.Filters{Search: values.Get("q"), Status: values.Get("status"), Role: values.Get("role"), Limit: 20}
	if raw, ok := values["limit"]; ok {
		f.Limit, err = strconv.Atoi(raw[0])
		if err != nil {
			return f, service.ErrInvalidQuery
		}
	}
	for key, dest := range map[string]**time.Time{"created_from": &f.CreatedFrom, "created_to": &f.CreatedTo} {
		if raw, ok := values[key]; ok {
			value, e := time.Parse(time.RFC3339Nano, raw[0])
			if e != nil || value.Year() < 1 || value.Year() > 9999 {
				return f, service.ErrInvalidQuery
			}
			value = value.UTC()
			*dest = &value
		}
	}
	return f, nil
}
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrForbidden):
		httpapi.Error(w, r, 403, "FORBIDDEN", "Требуется владелец организации")
	case errors.Is(err, service.ErrNotFound):
		httpapi.Error(w, r, 404, "RESOURCE_NOT_FOUND", "Сделка не найдена")
	case errors.Is(err, service.ErrInvalidQuery):
		httpapi.Error(w, r, 400, "INVALID_REQUEST", "Некорректные параметры списка сделок")
	default:
		httpapi.Error(w, r, 503, "TEMPORARILY_UNAVAILABLE", "Операция временно недоступна. Повторите исходный запрос с тем же ключом и проверьте результат")
	}
}
