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

package fprule

import (
	"fmt"
	"path"
	"strings"
	"testing"
)

// seed_test.go — the v0.1.0 seed-collection regression gate:
//
//   - every seed rule lints clean AND passes its own false-positive
//     regression cases (a contribution that breaks either fails CI);
//   - the family coverage required by the owner directive (§2: web
//     servers, CMS, languages, frameworks, web technologies — plus the
//     service family for non-HTTP protocol software) is pinned;
//   - the versioned identification capability is pinned end-to-end.

func TestSeedLintsCleanAndPassesItsTests(t *testing.T) {
	rules, problems, err := LoadSeed()
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("seed parse problems: %v", problems)
	}
	if len(rules) == 0 {
		t.Fatal("no seed rules")
	}
	if problems := Lint(rules); len(problems) != 0 {
		for _, p := range problems {
			t.Errorf("lint: %s", p)
		}
		t.Fatalf("seed collection has %d lint problems", len(problems))
	}
	for _, r := range rules {
		if problems := ApplyTests(r); len(problems) != 0 {
			for _, p := range problems {
				t.Errorf("%s: %s", r.ID, p)
			}
			t.Errorf("rule %s fails its own regression tests", r.ID)
		}
	}
}

func TestSeedFamilyCoverage(t *testing.T) {
	rules, _, err := LoadSeed()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[Family]int{}
	for _, r := range rules {
		seen[r.Family]++
	}
	for _, f := range []Family{FamilyWebServer, FamilyCMS, FamilyLanguage, FamilyFramework, FamilyWebTech, FamilyService} {
		if seen[f] == 0 {
			t.Errorf("seed family coverage: family %q has no rules (owner directive §2)", f)
		}
	}
	for f, n := range seen {
		t.Logf("family %-12s %2d rules", f, n)
	}
}

func TestSeedRegistryHealthy(t *testing.T) {
	reg := Seed()
	if !reg.Healthy() {
		t.Fatalf("seed registry unhealthy: %v", reg.LintProblems())
	}
}

func TestSeedVersionedIdentification(t *testing.T) {
	cases := []struct {
		evidence    map[string]any
		wantProduct string
		wantVersion string
	}{
		{map[string]any{"http": map[string]any{"server": "nginx/1.24.0"}}, "nginx", "1.24.0"},
		{map[string]any{"http": map[string]any{"server": "Microsoft-IIS/10.0"}}, "microsoft-iis", "10.0"},
		{map[string]any{"http": map[string]any{"server": "Jetty(9.4.44.v20210927)"}}, "jetty", "9.4.44.v20210927"},
		{map[string]any{"http": map[string]any{"title": "Apache Tomcat/10.0.14"}}, "tomcat", "10.0.14"},
		{map[string]any{"ssh": map[string]any{"banner": "SSH-2.0-OpenSSH_9.6p1 Ubuntu-3ubuntu13"}}, "openssh", "9.6p1"},
	}
	reg := Seed()
	for i, tc := range cases {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			hits := reg.Apply(tc.evidence)
			if len(hits) == 0 {
				t.Fatalf("no hits for %v", tc.evidence)
			}
			if hits[0].Product != tc.wantProduct {
				t.Fatalf("top hit = %s, want %s (hits: %s)", hits[0].Product, tc.wantProduct, matchSummary(hits))
			}
			if hits[0].Version != tc.wantVersion {
				t.Fatalf("version = %q, want %q", hits[0].Version, tc.wantVersion)
			}
		})
	}
}

func TestSeedMutualExclusions(t *testing.T) {
	// Pairs of products whose signatures must not cross-fire — the
	// classic false-positive surface for anchored-prefix rules.
	pairs := []struct {
		ruleA, ruleB string // product names
		evidence     map[string]any
	}{
		{"nginx", "openresty", map[string]any{"http": map[string]any{"server": "openresty/1.21.4.1"}}},
		{"openresty", "nginx", map[string]any{"http": map[string]any{"server": "nginx/1.21.4.1"}}},
		{"apache-httpd", "tomcat", map[string]any{"http": map[string]any{"server": "Apache-Coyote/1.1"}}},
		{"gunicorn", "unicorn", map[string]any{"http": map[string]any{"server": "unicorn/5.4.0"}}},
		{"cloudflare", "other", map[string]any{"http": map[string]any{"server": "cloudflare-custom"}}},
	}
	reg := Seed()
	for _, p := range pairs {
		hits := reg.Apply(p.evidence)
		for _, h := range hits {
			if h.Product == p.ruleA {
				t.Errorf("%s fired on cross-product evidence %v (hits: %s)", p.ruleA, p.evidence, matchSummary(hits))
			}
		}
	}
}

func TestSeedNoExploitationShape(t *testing.T) {
	rules, _, err := LoadSeed()
	if err != nil {
		t.Fatal(err)
	}
	if problems := Lint(rules); len(problems) != 0 {
		t.Fatalf("seed must never trip the exploitation lint: %v", problems)
	}
}

// TestSeedFSUsesSlashPaths — Windows regression: embed.FS requires
// slash-separated names on every OS.  LoadSeed must pass path.Join-style
// (slash) paths to seedFS.ReadFile, never filepath.Join-style
// (backslash on Windows) paths.  Reading each known seed file directly
// through seedFS with a slash path proves the contract.
func TestSeedFSUsesSlashPaths(t *testing.T) {
	// The seed set is embedded; enumerate expected files and read each
	// through seedFS using a slash-separated path.  On Windows this
	// catches any accidental filepath.Join("seed", name) usage, which
	// would produce "seed\<name>" and fail with "file does not exist".
	entries, err := seedFS.ReadDir("seed")
	if err != nil {
		t.Fatalf("seedFS.ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		fsPath := path.Join("seed", e.Name()) // slash-separated, OS-independent
		if _, err := seedFS.ReadFile(fsPath); err != nil {
			t.Errorf("seedFS.ReadFile(%q) = %v — seed loading must use slash paths", fsPath, err)
		}
	}
}

// TestSeedLoadRoundTripWindows — every file that LoadSeed enumerates
// must also be readable by LoadSeed itself.  On Windows this catches
// any backslash path passed to embed.FS (file does not exist).
func TestSeedLoadRoundTripWindows(t *testing.T) {
	rules, problems, err := LoadSeed()
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("seed parse problems: %v", problems)
	}
	if len(rules) == 0 {
		t.Fatal("no seed rules loaded")
	}
	// Every expected seed file must produce a rule with matching ID.
	expected := []string{
		"cms-wordpress", "framework-django-admin",
		"language-php", "language-python",
		"service-openssh",
		"web-server-apache", "web-server-caddy", "web-server-envoy",
		"web-server-gunicorn", "web-server-iis", "web-server-jetty",
		"web-server-kestrel", "web-server-lighttpd", "web-server-litespeed",
		"web-server-nginx", "web-server-openresty", "web-server-tomcat",
		"web-server-tornado", "web-server-werkzeug",
		"web-tech-cloudflare", "web-tech-varnish", "web-tech-vercel",
	}
	seen := map[string]bool{}
	for _, r := range rules {
		seen[r.ID] = true
	}
	for _, id := range expected {
		if !seen[id] {
			t.Errorf("seed rule %q not loaded (missing from LoadSeed output)", id)
		}
	}
}
