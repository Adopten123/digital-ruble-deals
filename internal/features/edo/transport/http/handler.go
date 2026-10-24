package edohttp

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"

	httpapi "github.com/Adopten123/digital-ruble-deals/internal/core/transport/http"
	authhttp "github.com/Adopten123/digital-ruble-deals/internal/features/auth/transport/http"
	deals "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	"github.com/Adopten123/digital-ruble-deals/internal/features/edo/service"
)

type Service interface {
	Receive(context.Context, service.Headers, []byte, string) (deals.Result, error)
	Queue(context.Context, service.Actor, string, service.Input) (deals.Result, error)
	Get(context.Context, service.Actor, string) (service.Delivery, error)
}
type Handler struct {
	service Service
	csrf    *httpapi.CSRF
}

func New(s Service, c *httpapi.CSRF) *Handler { return &Handler{service: s, csrf: c} }
func (h *Handler) RegisterRoutes(mux *http.ServeMux, require func(http.Handler) http.Handler) {
	mux.HandleFunc("POST /internal/edo/events", h.receive)
	mux.Handle("POST /api/v1/demo/edo/events", require(http.HandlerFunc(h.queue)))
	mux.Handle("GET /api/v1/demo/edo/events/{event_id}", require(http.HandlerFunc(h.get)))
}
func failure(w http.ResponseWriter, r *http.Request, e error) {
	switch {
	case errors.Is(e, service.ErrForbidden):
		httpapi.Error(w, r, 403, "FORBIDDEN", "Нужен оператор тестового контура")
	case errors.Is(e, service.ErrNotFound):
		httpapi.Error(w, r, 404, "RESOURCE_NOT_FOUND", "Событие не найдено")
	default:
		httpapi.Error(w, r, 503, "TEMPORARILY_UNAVAILABLE", "Повторите исходное событие или команду с прежним ключом")
	}
}
func result(w http.ResponseWriter, r *http.Request, out deals.Result, err error) {
	if err != nil {
		failure(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(out.Status)
	_, _ = w.Write(out.Body)
}
func actor(r *http.Request) service.Actor {
	id, ok := authhttp.CurrentIdentity(r.Context())
	if !ok || id.User.SystemRole != "DEMO_OPERATOR" {
		return service.Actor{}
	}
	return service.Actor{UserID: id.User.ID, RequestID: httpapi.RequestID(r.Context())}
}
func (h *Handler) receive(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{"X-EDO-Key-ID", "X-EDO-Timestamp", "X-EDO-Signature"} {
		if len(r.Header.Values(name)) != 1 {
			httpapi.Error(w, r, 403, "EDO_SOURCE_INVALID", "Источник события ЭДО не подтверждён")
			return
		}
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		httpapi.Error(w, r, 415, "UNSUPPORTED_MEDIA_TYPE", "Ожидается application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, service.MaxBody)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var size *http.MaxBytesError
		if errors.As(err, &size) {
			httpapi.Error(w, r, 413, "PAYLOAD_TOO_LARGE", "Превышен размер события ЭДО")
		} else {
			httpapi.Error(w, r, 400, "INVALID_REQUEST", "Не удалось прочитать событие")
		}
		return
	}
	out, err := h.service.Receive(r.Context(), service.Headers{KeyID: r.Header.Get("X-EDO-Key-ID"), Timestamp: r.Header.Get("X-EDO-Timestamp"), Signature: r.Header.Get("X-EDO-Signature")}, raw, httpapi.RequestID(r.Context()))
	result(w, r, out, err)
}
func (h *Handler) queue(w http.ResponseWriter, r *http.Request) {
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
	var in service.Input
	if !httpapi.Decode(w, r, &in) {
		return
	}
	out, err := h.service.Queue(r.Context(), a, key, in)
	result(w, r, out, err)
}
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	a := actor(r)
	if a.UserID == "" {
		failure(w, r, service.ErrForbidden)
		return
	}
	if r.URL.RawQuery != "" {
		httpapi.Error(w, r, 400, "INVALID_REQUEST", "Параметры запроса не поддерживаются")
		return
	}
	out, err := h.service.Get(r.Context(), a, r.PathValue("event_id"))
	if err != nil {
		failure(w, r, err)
		return
	}
	httpapi.JSON(w, 200, out)
}
