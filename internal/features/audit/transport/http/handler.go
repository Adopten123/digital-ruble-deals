package audithttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	httpapi "github.com/Adopten123/digital-ruble-deals/internal/core/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/features/audit/service"
	authhttp "github.com/Adopten123/digital-ruble-deals/internal/features/auth/transport/http"
)

type Service interface {
	List(context.Context, service.Actor, service.Filters, string) (service.Page, error)
	Export(context.Context, service.Actor, service.Filters) (service.Export, error)
}
type Handler struct{ service Service }

func New(s Service) *Handler { return &Handler{service: s} }
func (h *Handler) RegisterRoutes(mux *http.ServeMux, require func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/deals/{id}/audit", require(http.HandlerFunc(h.deal)))
	mux.Handle("GET /api/v1/deals/{id}/audit/export", require(http.HandlerFunc(h.export)))
	mux.Handle("GET /api/v1/demo/audit", require(http.HandlerFunc(h.demo)))
}
func actor(r *http.Request, operator bool) (service.Actor, error) {
	identity, ok := authhttp.CurrentIdentity(r.Context())
	if !ok {
		return service.Actor{}, service.ErrForbidden
	}
	a := service.Actor{UserID: identity.User.ID, Operator: operator}
	if operator {
		if identity.User.SystemRole != "DEMO_OPERATOR" {
			return service.Actor{}, service.ErrForbidden
		}
	} else {
		if identity.User.SystemRole == "DEMO_OPERATOR" {
			return service.Actor{}, service.ErrNotFound
		}
		if identity.User.SystemRole != "USER" || identity.Organization == nil || identity.Organization.Role != "ORG_OWNER" {
			return service.Actor{}, service.ErrForbidden
		}
		a.OrganizationID = identity.Organization.ID
	}
	return a, nil
}
func query(r *http.Request, operator, export bool) (service.Filters, string, string, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return service.Filters{}, "", "", service.ErrInvalidQuery
	}
	for key, values := range q {
		if len(values) != 1 || values[0] == "" {
			return service.Filters{}, "", "", service.ErrInvalidQuery
		}
		switch key {
		case "type", "from", "to":
		case "deal_id":
			if !operator {
				return service.Filters{}, "", "", service.ErrInvalidQuery
			}
		case "limit", "cursor":
			if export {
				return service.Filters{}, "", "", service.ErrInvalidQuery
			}
		case "format":
			if !export {
				return service.Filters{}, "", "", service.ErrInvalidQuery
			}
		default:
			return service.Filters{}, "", "", service.ErrInvalidQuery
		}
	}
	filters := service.Filters{DealID: q.Get("deal_id"), Type: q.Get("type"), Limit: 20}
	if !operator {
		filters.DealID = r.PathValue("id")
	}
	if raw, ok := q["limit"]; ok {
		filters.Limit, err = strconv.Atoi(raw[0])
		if err != nil || strconv.Itoa(filters.Limit) != raw[0] {
			return filters, "", "", service.ErrInvalidQuery
		}
	}
	for key, dest := range map[string]**time.Time{"from": &filters.From, "to": &filters.To} {
		if raw, ok := q[key]; ok {
			t, err := time.Parse(time.RFC3339Nano, raw[0])
			if err != nil || t.Year() < 1 || t.Year() > 9999 {
				return filters, "", "", service.ErrInvalidQuery
			}
			t = t.UTC()
			*dest = &t
		}
	}
	format := q.Get("format")
	if export && format != "csv" && format != "json" {
		return filters, "", "", service.ErrInvalidQuery
	}
	return filters, q.Get("cursor"), format, nil
}
func failure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrForbidden):
		httpapi.Error(w, r, 403, "FORBIDDEN", "Недостаточно прав для чтения истории")
	case errors.Is(err, service.ErrNotFound):
		httpapi.Error(w, r, 404, "RESOURCE_NOT_FOUND", "Сделка не найдена")
	case errors.Is(err, service.ErrInvalidQuery):
		httpapi.Error(w, r, 400, "INVALID_REQUEST", "Проверьте параметры истории и обновите список")
	case errors.Is(err, service.ErrExportTooLarge):
		httpapi.Error(w, r, 422, "EXPORT_TOO_LARGE", "В экспорте больше 5000 событий. Уточните период или тип события")
	default:
		httpapi.Error(w, r, 503, "TEMPORARILY_UNAVAILABLE", "История временно недоступна. Повторите чтение")
	}
}
func (h *Handler) deal(w http.ResponseWriter, r *http.Request) { h.list(w, r, false) }
func (h *Handler) demo(w http.ResponseWriter, r *http.Request) { h.list(w, r, true) }
func (h *Handler) list(w http.ResponseWriter, r *http.Request, operator bool) {
	a, err := actor(r, operator)
	if err != nil {
		failure(w, r, err)
		return
	}
	filters, cursor, _, err := query(r, operator, false)
	if err != nil {
		failure(w, r, err)
		return
	}
	out, err := h.service.List(r.Context(), a, filters, cursor)
	if err != nil {
		failure(w, r, err)
		return
	}
	httpapi.JSON(w, 200, out)
}
func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	a, err := actor(r, false)
	if err != nil {
		failure(w, r, err)
		return
	}
	filters, _, format, err := query(r, false, true)
	if err != nil {
		failure(w, r, err)
		return
	}
	out, err := h.service.Export(r.Context(), a, filters)
	if err != nil {
		failure(w, r, err)
		return
	}
	var body []byte
	if format == "csv" {
		body, err = service.CSV(out.Items)
	} else {
		body, err = json.Marshal(out)
	}
	if err != nil {
		failure(w, r, err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="poaktu-audit-`+filters.DealID+`.`+format+`"`)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	}
	w.WriteHeader(200)
	_, _ = w.Write(body)
}
