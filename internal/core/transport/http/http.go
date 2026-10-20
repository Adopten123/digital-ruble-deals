package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
)

type requestIDKey struct{}

func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}
func JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}
func Error(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	JSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}, "request_id": RequestID(r.Context())})
}

func Decode(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		Error(w, r, 415, "UNSUPPORTED_MEDIA_TYPE", "Ожидается application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		decodeError(w, r, err)
		return false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' || !utf8.Valid(body) {
		Error(w, r, 400, "INVALID_REQUEST", "Тело должно быть JSON-объектом в UTF-8")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		decodeError(w, r, err)
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		decodeError(w, r, err)
		return false
	}
	return true
}
func Empty(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1)
	b, err := io.ReadAll(r.Body)
	if err != nil || len(b) != 0 {
		Error(w, r, 400, "INVALID_REQUEST", "Тело запроса должно быть пустым")
		return false
	}
	return true
}
func decodeError(w http.ResponseWriter, r *http.Request, err error) {
	var size *http.MaxBytesError
	if errors.As(err, &size) {
		Error(w, r, 413, "PAYLOAD_TOO_LARGE", "Превышен размер запроса")
		return
	}
	Error(w, r, 400, "INVALID_REQUEST", "Некорректный JSON или неизвестные поля")
}

func Middleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := security.RandomToken()
		if err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), requestIDKey{}, id), 15*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		started := time.Now()
		defer func() {
			if recover() != nil {
				logger.Error("request panic", "request_id", id)
				Error(w, r, 500, "INTERNAL_ERROR", "Внутренняя ошибка")
			}
			logger.Info("request", "request_id", id, "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds())
		}()
		next.ServeHTTP(w, r)
	})
}
