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

package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// httpx_test.go — regression pins for the M16 security review: the
// security headers every surface must carry, and the trusted-proxy
// client-IP resolution (spoof-proof by default, opt-in by config).

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, frag := range []string{"default-src 'none'", "style-src 'unsafe-inline'", "frame-ancestors 'none'", "script-src"} {
		if frag == "script-src" {
			// scripts must be implicitly banned by default-src 'none'
			if strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "script-src *") {
				t.Errorf("CSP must not allow scripts: %q", csp)
			}
			continue
		}
		if !strings.Contains(csp, frag) {
			t.Errorf("CSP missing %q: %q", frag, csp)
		}
	}
	// Headers land even on error statuses (418 above proves it).
}

func TestResolveClientIPDefaultIgnoresXFF(t *testing.T) {
	h := ResolveClientIP(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.RemoteAddr; got != "203.0.113.9" {
			t.Errorf("RemoteAddr = %q, want the peer address", got)
		}
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:44321"
	req.Header.Set("X-Forwarded-For", "8.8.8.8") // spoofed
	h.ServeHTTP(httptest.NewRecorder(), req)
}

func TestResolveClientIPTrustedProxyHonored(t *testing.T) {
	trusted, err := ParseTrustedProxies([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	h := ResolveClientIP(trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.RemoteAddr
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.1.2.3:5555" // the proxy
	req.Header.Set("X-Forwarded-For", "198.51.100.7, 203.0.113.9")
	h.ServeHTTP(httptest.NewRecorder(), req)
	// The RIGHT-MOST hop is the one the trusted proxy appended.
	if got != "203.0.113.9" {
		t.Errorf("client ip = %q, want the last XFF hop", got)
	}
}

func TestResolveClientIPUntrustedPeerXFFIgnored(t *testing.T) {
	trusted, _ := ParseTrustedProxies([]string{"10.0.0.0/8"})
	var got string
	h := ResolveClientIP(trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.RemoteAddr
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.1:40000" // NOT in the trusted range
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got != "198.51.100.1" {
		t.Errorf("untrusted XFF must be ignored, got %q", got)
	}
}

func TestResolveClientIPMalformedXFFKeepsPeer(t *testing.T) {
	trusted, _ := ParseTrustedProxies([]string{"127.0.0.1"})
	var got string
	h := ResolveClientIP(trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.RemoteAddr
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("X-Forwarded-For", "not-an-ip, ,,  300.300.300.300")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got != "127.0.0.1" {
		t.Errorf("malformed XFF must fall back to the peer, got %q", got)
	}
}

func TestParseTrustedProxiesValidation(t *testing.T) {
	if p, err := ParseTrustedProxies(nil); err != nil || p != nil {
		t.Errorf("empty spec must yield nil,nil; got %v %v", p, err)
	}
	p, err := ParseTrustedProxies([]string{"10.0.0.1", "192.168.0.0/16"})
	if err != nil || len(p) != 2 || p[1].Bits() != 16 {
		t.Errorf("valid spec parse failed: %v %v", p, err)
	}
	if _, err := ParseTrustedProxies([]string{"not-a-cidr"}); err == nil {
		t.Error("invalid entry must be a hard config error (fail-closed)")
	}
	if _, err := ParseTrustedProxies([]string{"203.0.113.0/33"}); err == nil {
		t.Error("impossible prefix length must be rejected")
	}
}
