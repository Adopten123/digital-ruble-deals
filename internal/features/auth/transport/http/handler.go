package authhttp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	httpapi "github.com/Adopten123/digital-ruble-deals/internal/core/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/features/auth/service"
)

type Service interface {
	Register(context.Context, string, string, string, string, string) (service.Identity, string, error)
	Login(context.Context, string, string, string) (service.Identity, string, error)
	Me(context.Context, string) (service.Identity, error)
	Logout(context.Context, string) error
}
type Handler struct {
	service                 Service
	csrf                    *httpapi.CSRF
	ipLimiter, emailLimiter *httpapi.Limiter
	slots                   chan struct{}
}

func New(s Service, csrf *httpapi.CSRF) *Handler {
	return &Handler{service: s, csrf: csrf, ipLimiter: httpapi.NewLimiter(30, time.Minute), emailLimiter: httpapi.NewLimiter(10, time.Minute), slots: make(chan struct{}, 4)}
}
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/auth/csrf", h.csrf.Get)
	mux.HandleFunc("POST /api/v1/auth/register", h.register)
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.Handle("POST /api/v1/auth/logout", h.Require(http.HandlerFunc(h.logout)))
	mux.Handle("GET /api/v1/auth/me", h.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, _ := CurrentIdentity(r.Context())
		httpapi.JSON(w, 200, identity)
	})))
}

type identityKey struct{}

func CurrentIdentity(ctx context.Context) (service.Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(service.Identity)
	return identity, ok
}
func (h *Handler) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, err := h.service.Me(r.Context(), httpapi.SessionToken(r))
		if err != nil {
			if errors.Is(err, service.ErrNoSession) {
				code := "AUTH_REQUIRED"
				if httpapi.SessionToken(r) != "" {
					code = "SESSION_EXPIRED"
				}
				httpapi.Error(w, r, 401, code, "Войдите в приложение")
				return
			}
			writeError(w, r, err)
			return
		}
		// A shared browser cookie can change while another tab still displays the old actor.
		// The header is only a precondition: identity still comes exclusively from the session.
		if expected := r.Header.Get("X-Expected-User-ID"); expected != "" && expected != identity.User.ID {
			httpapi.Error(w, r, http.StatusConflict, "SESSION_CHANGED", "Аккаунт изменился. Обновите кабинет")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, identity)))
	})
}
func (h *Handler) beginAuth(w http.ResponseWriter, r *http.Request) bool {
	if !h.csrf.Check(w, r) || !h.ipLimiter.Check(w, r) {
		return false
	}
	select {
	case h.slots <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "1")
		httpapi.Error(w, r, 429, "RATE_LIMITED", "Сервер занят проверкой паролей. Повторите позже")
		return false
	}
}
func (h *Handler) allowEmail(w http.ResponseWriter, r *http.Request, email string) bool {
	if h.emailLimiter.Allow(security.TokenHash(strings.ToLower(strings.TrimSpace(email)))) {
		return true
	}
	w.Header().Set("Retry-After", "60")
	httpapi.Error(w, r, 429, "RATE_LIMITED", "Слишком много попыток. Повторите через минуту")
	return false
}
func previousHash(r *http.Request) string {
	if token := httpapi.SessionToken(r); security.ValidToken(token) {
		return security.TokenHash(token)
	}
	return ""
}
func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	if !h.beginAuth(w, r) {
		return
	}
	defer func() { <-h.slots }()
	var in struct {
		Email        string `json:"email"`
		Password     string `json:"password"`
		Organization struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"organisation"`
	}
	if !httpapi.Decode(w, r, &in) || !h.allowEmail(w, r, in.Email) {
		return
	}
	identity, token, err := h.service.Register(r.Context(), in.Email, in.Password, in.Organization.Name, in.Organization.Kind, previousHash(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.authenticated(w, r, 201, identity, token)
}
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.beginAuth(w, r) {
		return
	}
	defer func() { <-h.slots }()
	var in struct {
		Email    *string `json:"email"`
		Password *string `json:"password"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if in.Email == nil || in.Password == nil {
		httpapi.Error(w, r, 400, "INVALID_REQUEST", "email и password должны быть строками")
		return
	}
	if !h.allowEmail(w, r, *in.Email) {
		return
	}
	identity, token, err := h.service.Login(r.Context(), *in.Email, *in.Password, previousHash(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.authenticated(w, r, 200, identity, token)
}
func (h *Handler) authenticated(w http.ResponseWriter, r *http.Request, status int, identity service.Identity, token string) {
	csrf, err := h.csrf.Issue(w, r, token, true)
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.csrf.SetSession(w, token, identity.SessionExpiresAt)
	httpapi.JSON(w, status, struct {
		service.Identity
		CSRFToken string `json:"csrf_token"`
	}{Identity: identity, CSRFToken: csrf})
}
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if !h.csrf.Check(w, r) || !httpapi.Empty(w, r) {
		return
	}
	if err := h.service.Logout(r.Context(), httpapi.SessionToken(r)); err != nil {
		writeError(w, r, err)
		return
	}
	h.csrf.ClearSession(w)
	w.WriteHeader(204)
}
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		httpapi.Error(w, r, 400, "INVALID_REQUEST", "Проверьте email, пароль от 12 до 128 символов и данные организации")
	case errors.Is(err, service.ErrCredentials):
		httpapi.Error(w, r, 401, "INVALID_CREDENTIALS", "Неверный email или пароль")
	case errors.Is(err, service.ErrEmailConflict):
		httpapi.Error(w, r, 409, "EMAIL_CONFLICT", "Email уже зарегистрирован")
	case errors.Is(err, service.ErrNoSession):
		httpapi.Error(w, r, 401, "AUTH_REQUIRED", "Войдите в приложение")
	default:
		httpapi.Error(w, r, 503, "TEMPORARILY_UNAVAILABLE", "Операция временно недоступна. Проверьте результат перед повтором")
	}
}
