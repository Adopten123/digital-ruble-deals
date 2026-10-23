package organizationshttp

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	httpapi "github.com/Adopten123/digital-ruble-deals/internal/core/transport/http"
	authhttp "github.com/Adopten123/digital-ruble-deals/internal/features/auth/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/features/organizations/service"
)

type Service interface {
	List(context.Context, string, string, int) (service.Catalog, error)
	Me(context.Context, string, string) (service.OwnOrganization, error)
}
type Handler struct{ service Service }

func New(s Service) *Handler { return &Handler{service: s} }
func (h *Handler) RegisterRoutes(mux *http.ServeMux, require func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/organisations", require(http.HandlerFunc(h.list)))
	mux.Handle("GET /api/v1/organisations/me", require(http.HandlerFunc(h.me)))
}
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			writeError(w, r, service.ErrInvalidQuery)
			return
		}
		limit = value
	}
	out, err := h.service.List(r.Context(), r.URL.Query().Get("q"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpapi.JSON(w, 200, out)
}
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	identity, ok := authhttp.CurrentIdentity(r.Context())
	if !ok || identity.Organization == nil {
		writeError(w, r, service.ErrForbidden)
		return
	}
	out, err := h.service.Me(r.Context(), identity.User.ID, identity.Organization.ID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpapi.JSON(w, 200, out)
}
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidQuery):
		httpapi.Error(w, r, 400, "INVALID_REQUEST", "Некорректные параметры списка")
	case errors.Is(err, service.ErrForbidden):
		httpapi.Error(w, r, 403, "FORBIDDEN", "Требуется членство в организации")
	default:
		httpapi.Error(w, r, 503, "TEMPORARILY_UNAVAILABLE", "Операция временно недоступна")
	}
}
