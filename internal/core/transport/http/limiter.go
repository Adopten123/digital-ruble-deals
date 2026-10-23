package httpapi

import (
	"net"
	"net/http"
	"sync"
	"time"
)

type rateWindow struct {
	started time.Time
	count   int
}

// Limiter is intentionally per-process for this single-instance local prototype.
// It ignores spoofable forwarding headers. At a proxy, the shared proxy address has one budget.
type Limiter struct {
	mu       sync.Mutex
	windows  map[string]rateWindow
	max      int
	duration time.Duration
}

func NewLimiter(max int, duration time.Duration) *Limiter {
	return &Limiter{windows: map[string]rateWindow{}, max: max, duration: duration}
}
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	window := l.windows[key]
	if now.Sub(window.started) >= l.duration {
		window = rateWindow{started: now}
	}
	if len(l.windows) >= 10000 {
		for k, v := range l.windows {
			if now.Sub(v.started) >= l.duration {
				delete(l.windows, k)
			}
		}
		if len(l.windows) >= 10000 && window.count == 0 {
			return false
		}
	}
	if window.count >= l.max {
		return false
	}
	window.count++
	l.windows[key] = window
	return true
}
func (l *Limiter) Check(w http.ResponseWriter, r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !l.Allow(host) {
		w.Header().Set("Retry-After", "60")
		Error(w, r, 429, "RATE_LIMITED", "Слишком много попыток. Повторите через минуту")
		return false
	}
	return true
}
