// GSEER — Internet Asset Intelligence & Search Engine.
// Copyright (C) 2026  The GSEER Authors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/Lutfifakee-Project/GSEER/internal/httpx"
)

// middleware.go — the API middlewares: client-IP resolution, the
// per-IP fixed-window rate limiter with X-RateLimit-* headers, and the
// request-meta carrier (request_id + start time for the envelope).

// realIP is retired in favor of httpx.ResolveClientIP (M16: opt-in
// trusted-proxy handling with the same peer-address-only default).  The
// wrapper keeps the historical name for tests; new code uses the httpx
// middleware directly.
func realIP(next http.Handler) http.Handler {
	return httpx.ResolveClientIP(nil)(next)
}

// requestMeta stamps the meta carrier (chi's RequestID middleware has
// already put the ID in the context under its own key).
func (s *Server) requestMeta(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chiRequestID(r)
		c := &metaCarrier{id: id, start: time.Now()}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), metaKey{}, c)))
	})
}

// chiRequestID reads chi's RequestID from the context (set by
// chimw.RequestID, which runs before this middleware).
func chiRequestID(r *http.Request) string {
	if id := chimw.GetReqID(r.Context()); id != "" {
		return id
	}
	return r.Header.Get("X-Request-Id")
}

// ---------------------------------------------------------------------------
// Rate limiting (fixed window, per client IP)
// ---------------------------------------------------------------------------

type window struct {
	count int
	reset time.Time
}

type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	period time.Duration
	byIP   map[string]*window
}

func newRateLimiter(limit int, period time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, period: period, byIP: map[string]*window{}}
}

// take consumes one request slot for ip.  Returns (allowed, remaining,
// resetAt).  Windows are lazy: an expired window resets on touch.  The
// map is pruned opportunistically every pruneEvery touches to keep the
// memory bounded on loopback-shaped deployments.
func (l *rateLimiter) take(ip string, now time.Time) (bool, int, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.byIP) > 4096 {
		for k, w := range l.byIP {
			if now.After(w.reset) {
				delete(l.byIP, k)
			}
		}
	}
	w, ok := l.byIP[ip]
	if !ok || now.After(w.reset) {
		w = &window{count: 0, reset: now.Add(l.period)}
		l.byIP[ip] = w
	}
	if w.count >= l.limit {
		return false, 0, w.reset
	}
	w.count++
	return true, l.limit - w.count, w.reset
}

// rateLimit applies the per-IP limiter and sets the standard headers.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, remaining, reset := s.limiter.take(r.RemoteAddr, time.Now())
		w.Header().Set("X-RateLimit-Limit", itoa(s.RateLimit))
		w.Header().Set("X-RateLimit-Remaining", itoa(remaining))
		w.Header().Set("X-RateLimit-Reset", itoa(int(time.Until(reset).Seconds())))
		if !allowed {
			w.Header().Set("Retry-After", itoa(int(time.Until(reset).Seconds())+1))
			writeError(w, r, http.StatusTooManyRequests, &apiError{
				Code:   "API_E_RATE_LIMITED",
				Detail: "too many requests from this address",
				Next:   "wait for the window to reset and retry",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
