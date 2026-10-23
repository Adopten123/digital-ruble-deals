package settlementhttp

import (
	"context"
	"errors"
	httpapi "github.com/Adopten123/digital-ruble-deals/internal/core/transport/http"
	authhttp "github.com/Adopten123/digital-ruble-deals/internal/features/auth/transport/http"
	deals "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	"github.com/Adopten123/digital-ruble-deals/internal/features/settlement/service"
	"net/http"
	"net/url"
	"strconv"
)

type Service interface {
	List(context.Context, service.Actor, string, string, int) (service.Page, error)
	Move(context.Context, service.Actor, string, string, string, service.Input) (deals.Result, error)
}
type Handler struct {
	service Service
	csrf    *httpapi.CSRF
}

func New(s Service, c *httpapi.CSRF) *Handler { return &Handler{service: s, csrf: c} }
func (h *Handler) RegisterRoutes(mux *http.ServeMux, require func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/demo/accounts", require(http.HandlerFunc(h.list)))
	mux.Handle("POST /api/v1/demo/accounts/{id}/credits", require(http.HandlerFunc(h.credit)))
	mux.Handle("POST /api/v1/demo/accounts/{id}/debits", require(http.HandlerFunc(h.debit)))
}
func actor(r *http.Request) service.Actor {
	identity, ok := authhttp.CurrentIdentity(r.Context())
	if !ok || identity.User.SystemRole != "DEMO_OPERATOR" {
		return service.Actor{}
	}
	return service.Actor{UserID: identity.User.ID, RequestID: httpapi.RequestID(r.Context())}
}
func failure(w http.ResponseWriter, r *http.Request, e error) {
	switch {
	case errors.Is(e, service.ErrForbidden):
		httpapi.Error(w, r, 403, "FORBIDDEN", "Нужен оператор тестового контура")
	case errors.Is(e, service.ErrNotFound):
		httpapi.Error(w, r, 404, "RESOURCE_NOT_FOUND", "Счёт не найден")
	case errors.Is(e, service.ErrInvalidQuery):
		httpapi.Error(w, r, 400, "INVALID_REQUEST", "Некорректные параметры счетов")
	default:
		httpapi.Error(w, r, 503, "TEMPORARILY_UNAVAILABLE", "Повторите исходную команду с тем же ключом и проверьте результат")
	}
}
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if a.UserID == "" {
		failure(w, r, service.ErrForbidden)
		return
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		failure(w, r, service.ErrInvalidQuery)
		return
	}
	for k, v := range q {
		if len(v) != 1 || (k != "q" && k != "limit" && k != "cursor") {
			failure(w, r, service.ErrInvalidQuery)
			return
		}
	}
	limit := 20
	if raw, ok := q["limit"]; ok {
		limit, e = strconv.Atoi(raw[0])
		if e != nil {
			failure(w, r, service.ErrInvalidQuery)
			return
		}
	}
	out, e := h.service.List(r.Context(), a, q.Get("q"), q.Get("cursor"), limit)
	if e != nil {
		failure(w, r, e)
		return
	}
	httpapi.JSON(w, 200, out)
}
func (h *Handler) credit(w http.ResponseWriter, r *http.Request) { h.move(w, r, "DEMO_CREDIT") }
func (h *Handler) debit(w http.ResponseWriter, r *http.Request)  { h.move(w, r, "DEMO_DEBIT") }
func (h *Handler) move(w http.ResponseWriter, r *http.Request, op string) {
	a := actor(r)
	if a.UserID == "" {
		failure(w, r, service.ErrForbidden)
		return
	}
	if !h.csrf.Check(w, r) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) != 1 || !deals.ValidKey(key) {
		httpapi.Error(w, r, 400, "IDEMPOTENCY_KEY_REQUIRED", "Нужен корректный Idempotency-Key")
		return
	}
	var in struct {
		Amount *string `json:"amount_kopecks"`
		Reason *string `json:"reason"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if in.Amount == nil || in.Reason == nil {
		httpapi.Error(w, r, 400, "INVALID_REQUEST", "Сумма и причина должны быть строками")
		return
	}
	out, e := h.service.Move(r.Context(), a, r.PathValue("id"), key, op, service.Input{AmountKopecks: *in.Amount, Reason: *in.Reason})
	if e != nil {
		failure(w, r, e)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(out.Status)
	_, _ = w.Write(out.Body)
}
