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
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestAPINotUsingAdvisoryChiMiddleware pins the API's chi middleware
// surface to the advisory-free subset established by the v0.1.0 chi
// v5.2.1 → v5.5.0 upgrade (pre-release correction for the GO-2025-3770
// CI failure).
//
// Four security advisories against github.com/go-chi/chi/v5 name
// middleware symbols that GSEER deliberately does not mount — client-IP
// resolution is owned by internal/httpx (fail-closed, trusted-proxy
// opt-in), not by chi:
//
//	GO-2025-3770 / GHSA-vrw8-fxc6-2r93 — middleware.RedirectSlashes
//	  (host header injection → open redirect; fixed chi v5.2.2)
//	GHSA-3fxj-6jh8-hvhx               — middleware.RealIP
//	  (IP spoofing via X-Forwarded-For; fixed chi v5.3.0)
//	GHSA-j6qf-3m9c-vwj3               — middleware.ClientIPFromXFF /
//	  the ClientIP middleware family (spoofing; fixed chi v5.4.0)
//	GHSA-rqrp-8cg2-5958               — middleware.SuppressNotFound
//	  (mutable route state escape; fixed chi v5.5.0)
//
// Keeping the module on the patched chi release AND out of the affected
// symbols keeps govulncheck's reachable set empty even if a future
// advisory lands.  This test fails if any of those symbols is mounted
// again, so the reviewer is pointed at the advisories instead of
// silently re-opening them.
func TestAPINotUsingAdvisoryChiMiddleware(t *testing.T) {
	// Symbol reference requires the chimw alias (internal/api/middleware.go
	// and internal/api/server.go import middleware as chimw).
	affected := []string{
		"RedirectSlashes",
		"RealIP",
		"ClientIPFromXFF",
		"ClientIPFromRequest",
		"ClientIPAddr",
		"ClientIP(",
		"SuppressNotFound",
	}
	pattern := regexp.MustCompile(`\bchimw\.[A-Za-z_]+`)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read api package dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, hit := range pattern.FindAllString(string(src), -1) {
			for _, bad := range affected {
				if strings.Contains(hit, bad) {
					t.Errorf("%s uses %s — this chi middleware is named by a "+
						"published security advisory (see this test's comment); "+
						"client-IP handling belongs in internal/httpx",
						name, hit)
				}
			}
		}
	}
}
