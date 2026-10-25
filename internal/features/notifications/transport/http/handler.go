package notificationshttp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	httpapi "github.com/Adopten123/digital-ruble-deals/internal/core/transport/http"
	authhttp "github.com/Adopten123/digital-ruble-deals/internal/features/auth/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/features/notifications/service"
)

type Service interface {
	List(context.Context, service.Actor, service.Filters, string) (service.Page, error)
	Read(context.Context, service.Actor, string) error
}
type Handler struct {
	service Service
	csrf    *httpapi.CSRF
}

func New(service Service, csrf *httpapi.CSRF) *Handler { return &Handler{service: service, csrf: csrf} }
func (h *Handler) RegisterRoutes(mux *http.ServeMux, require func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/notifications", require(http.HandlerFunc(h.list)))
	mux.Handle("POST /api/v1/notifications/{id}/read", require(http.HandlerFunc(h.read)))
}
func actor(r *http.Request) service.Actor {
	identity, ok := authhttp.CurrentIdentity(r.Context())
	if !ok {
		return service.Actor{}
	}
	if identity.User.SystemRole == "DEMO_OPERATOR" {
		return service.Actor{UserID: identity.User.ID, Operator: true}
	}
	if identity.User.SystemRole != "USER" || identity.Organization == nil || identity.Organization.Role != "ORG_OWNER" {
		return service.Actor{}
	}
	return service.Actor{UserID: identity.User.ID, OrganizationID: identity.Organization.ID}
}
func failure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrForbidden):
		httpapi.Error(w, r, 403, "FORBIDDEN", "Требуется пользователь организации")
	case errors.Is(err, service.ErrNotFound):
		httpapi.Error(w, r, 404, "RESOURCE_NOT_FOUND", "Уведомление не найдено")
	case errors.Is(err, service.ErrInvalidQuery):
		httpapi.Error(w, r, 400, "INVALID_REQUEST", "Проверьте параметры уведомлений и обновите список")
	default:
		httpapi.Error(w, r, 503, "TEMPORARILY_UNAVAILABLE", "Уведомления временно недоступны. Повторите запрос")
	}
}
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if a.UserID == "" {
		failure(w, r, service.ErrForbidden)
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		failure(w, r, service.ErrInvalidQuery)
		return
	}
	for key, v := range values {
		if len(v) != 1 || v[0] == "" || (key != "unread" && key != "limit" && key != "cursor") {
			failure(w, r, service.ErrInvalidQuery)
			return
		}
	}
	filters := service.Filters{Limit: 20}
	if raw, ok := values["limit"]; ok {
		filters.Limit, err = strconv.Atoi(raw[0])
		if err != nil || strconv.Itoa(filters.Limit) != raw[0] {
			failure(w, r, service.ErrInvalidQuery)
			return
		}
	}
	if raw, ok := values["unread"]; ok {
		if raw[0] != "true" && raw[0] != "false" {
			failure(w, r, service.ErrInvalidQuery)
			return
		}
		unread := raw[0] == "true"
		filters.Unread = &unread
	}
	out, err := h.service.List(r.Context(), a, filters, values.Get("cursor"))
	if err != nil {
		failure(w, r, err)
		return
	}
	httpapi.JSON(w, 200, out)
}
func (h *Handler) read(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if a.UserID == "" {
		failure(w, r, service.ErrNotFound)
		return
	}
	if !h.csrf.Check(w, r) {
		return
	}
	if r.URL.RawQuery != "" {
		failure(w, r, service.ErrInvalidQuery)
		return
	}
	if !httpapi.Empty(w, r) {
		return
	}
	if err := h.service.Read(r.Context(), a, r.PathValue("id")); err != nil {
		failure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
