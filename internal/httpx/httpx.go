// GSEER — internet-asset search engine.
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

// Package httpx holds the HTTP hardening shared by GSEER's two web
// surfaces (REST API + dashboard): security response headers and
// trusted-proxy client-IP resolution (v0.0.2 M16 security review).
//
// Layering: both internal/api and internal/dashboard import httpx;
// httpx imports only the standard library.  internal/search never
// imports httpx (search stays transport-free).
package httpx

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// SecurityHeaders sets the conservative response headers every GSEER
// web surface carries (M16 regression-pinned):
//
//   - X-Content-Type-Options: nosniff — responses are never MIME-sniffed
//   - X-Frame-Options: DENY       — GSEER UI is never framed
//   - Referrer-Policy: no-referrer — queries (which can contain operator
//     research) never leak via Referer to other origins
//   - Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline';
//     img-src data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'
//     — the dashboard is server-rendered with inline CSS and zero scripts;
//     this denies script injection even if an escaping bug ever appears.
//
// The headers are set on every response (they are cheap and the value is
// failing closed).
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; img-src data:; "+
				"form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// ParseTrustedProxies converts config strings into prefixes, rejecting
// anything that is not a valid CIDR (or bare IP, treated as /32 or
// /128).  Invalid input is a configuration error, never silently
// ignored — a typo must not widen trust.
func ParseTrustedProxies(specs []string) ([]netip.Prefix, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make([]netip.Prefix, 0, len(specs))
	for _, s := range specs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			addr, err := netip.ParseAddr(s)
			if err != nil {
				return nil, &InvalidProxyError{Value: s, Err: err}
			}
			out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, &InvalidProxyError{Value: s, Err: err}
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// InvalidProxyError reports a malformed trusted-proxy specification.
type InvalidProxyError struct {
	Value string
	Err   error
}

func (e *InvalidProxyError) Error() string {
	return "invalid trusted proxy entry " + e.Value + ": " + e.Err.Error()
}

func (e *InvalidProxyError) Unwrap() error { return e.Err }

// ResolveClientIP returns the middleware that stamps the rate-limit and
// audit client IP into r.RemoteAddr (host-only, like the previous realIP
// middleware).
//
// Default (no trusted prefixes configured): the TCP peer address ONLY —
// X-Forwarded-For is ignored entirely, so a client can never spoof its
// identity past the rate limiter.
//
// With trusted prefixes: when the immediate peer falls inside one, the
// client IP is the LAST (right-most) X-Forwarded-For hop — the hop the
// trusted proxy itself appended; earlier entries remain client-controlled
// and are never trusted.  A malformed or empty XFF keeps the peer
// address.  Loopback proxies are NOT implicitly trusted — an operator
// must name them explicitly (fail-closed by configuration).
func ResolveClientIP(trusted []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer := peerIP(r.RemoteAddr)
			if peer != "" && isTrustedProxy(peer, trusted) {
				if ip := lastForwardedFor(r.Header.Values("X-Forwarded-For")); ip != "" {
					peer = ip
				}
			}
			if peer != "" {
				r.RemoteAddr = peer
			}
			next.ServeHTTP(w, r)
		})
	}
}

// peerIP extracts the host part of a host:port RemoteAddr.
func peerIP(remote string) string {
	if host, _, err := net.SplitHostPort(remote); err == nil {
		return host
	}
	return remote
}

// isTrustedProxy reports whether ip falls inside any trusted prefix.
func isTrustedProxy(ip string, trusted []netip.Prefix) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// lastForwardedFor returns the right-most entry across all XFF headers
// (the hop appended nearest the trusted proxy).
func lastForwardedFor(headers []string) string {
	var last string
	for _, h := range headers {
		for _, part := range strings.Split(h, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			// Reject entries with spaces or quoting (obfuscated syntax).
			if strings.ContainsAny(part, " \t\"'") {
				continue
			}
			if _, err := netip.ParseAddr(part); err != nil {
				continue
			}
			last = part
		}
	}
	return last
}
