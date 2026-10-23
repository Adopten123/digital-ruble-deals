package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
)

const SessionCookie = "pa_session"
const csrfCookie = "pa_csrf"

type CSRF struct {
	key    []byte
	origin string
	secure bool
}

func NewCSRF(key []byte, origin string, secure bool) *CSRF {
	return &CSRF{key: append([]byte(nil), key...), origin: origin, secure: secure}
}
func SessionToken(r *http.Request) string {
	cookie, err := r.Cookie(SessionCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}
func (c *CSRF) signature(payload, session string) string {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte("poaktu:csrf:v1\x00" + payload + "\x00" + security.TokenHash(session)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
func (c *CSRF) valid(token, session string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || !security.ValidToken(parts[0]) {
		return false
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || expires <= time.Now().Unix() || expires > time.Now().Add(25*time.Hour).Unix() {
		return false
	}
	actual, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expected, _ := base64.RawURLEncoding.DecodeString(c.signature(parts[0]+"."+parts[1], session))
	return hmac.Equal(actual, expected)
}
func (c *CSRF) Issue(w http.ResponseWriter, r *http.Request, session string, force bool) (string, error) {
	if !force {
		if cookie, err := r.Cookie(csrfCookie); err == nil && c.valid(cookie.Value, session) {
			return cookie.Value, nil
		}
	}
	nonce, err := security.RandomToken()
	if err != nil {
		return "", err
	}
	payload := nonce + "." + strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10)
	token := payload + "." + c.signature(payload, session)
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: token, Path: "/", HttpOnly: true, Secure: c.secure, SameSite: http.SameSiteLaxMode, MaxAge: 86400})
	return token, nil
}
func (c *CSRF) Check(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Origin") != c.origin || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		Error(w, r, 403, "CSRF_INVALID", "Проверка источника запроса не пройдена")
		return false
	}
	cookie, err := r.Cookie(csrfCookie)
	header := r.Header.Get("X-CSRF-Token")
	if err != nil || len(header) > 256 || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) != 1 || !c.valid(header, SessionToken(r)) {
		Error(w, r, 403, "CSRF_INVALID", "Обновите защитный токен и повторите действие")
		return false
	}
	return true
}
func (c *CSRF) Get(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); (origin != "" && origin != c.origin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		Error(w, r, 403, "CSRF_INVALID", "Проверка источника запроса не пройдена")
		return
	}
	token, err := c.Issue(w, r, SessionToken(r), false)
	if err != nil {
		Error(w, r, 503, "TEMPORARILY_UNAVAILABLE", "Временно недоступно")
		return
	}
	JSON(w, 200, map[string]string{"csrf_token": token})
}
func (c *CSRF) SetSession(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: c.secure, SameSite: http.SameSiteLaxMode, MaxAge: 86400, Expires: expires})
}
func (c *CSRF) ClearSession(w http.ResponseWriter) {
	for _, name := range []string{SessionCookie, csrfCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: true, Secure: c.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
	}
}
