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

package searchlite_test

// service_integration_test.go — the personal-mode search integration:
// ingest through the storage layer, search through the GQL chain, and
// pin the behaviors the acceptance criteria require (GQL parity with
// the supported feature set, facets, keyset pagination, asset mode).

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/search"
	"github.com/Lutfifakee-Project/GSEER/internal/searchlite"
	"github.com/Lutfifakee-Project/GSEER/internal/storage"
	sqlstore "github.com/Lutfifakee-Project/GSEER/internal/storage/sqlite"
)

var obsSeq atomicCounter

type atomicCounter struct{ n int64 }

func (c *atomicCounter) next() int64 {
	c.n++
	return c.n
}

// seedIndex ingests the fixture-style corpus (assets A–D, services,
// plus a domain asset) into a fresh personal store.
func seedIndex(t *testing.T) (*sqlstore.Store, *searchlite.Service) {
	t.Helper()
	dir := t.TempDir()
	res, err := sqlstore.Open(filepath.Join(dir, "gseer.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = res.DB.Close() })
	if _, err := sqlstore.Migrate(context.Background(), res.DB); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store := sqlstore.NewStore(res.DB)
	src, err := store.EnsureSource(context.Background(), "fixture", "fixture", "", nil)
	if err != nil {
		t.Fatalf("fixture source: %v", err)
	}

	mk := func(kind, value string, port int, protocol, product, title string) sqlstore.Fact {
		var f sqlstore.Fact
		switch kind {
		case "ip":
			v := value
			f.Asset = storage.Asset{Kind: storage.AssetKindIP, Value: value, IP: &v}
		case "domain":
			v := value
			f.Asset = storage.Asset{Kind: storage.AssetKindDomain, Value: value, Domain: &v}
		default:
			v := value
			f.Asset = storage.Asset{Kind: storage.AssetKindHostname, Value: value, Hostname: &v}
		}
		obsID := obsSeq.next()
		f.Observation = storage.Observation{
			ID:           obsID,
			SourceID:     src,
			AssetID:      0, // resolved by ingest
			SourceCursor: fmt.Sprintf("obs-%s-%d", value, port),
			PayloadHash:  fmt.Sprintf("hash-%s-%d", value, port),
			Provenance:   storage.ProvenanceFixture,
			ObservedAt:   time.Now().UTC(),
		}
		f.Asset.GeoCountry = "US"
		if port != 0 {
			f.Service = &storage.Service{
				Port:      port,
				Transport: "tcp",
				Protocol:  protocol,
				Name:      protocol,
				Product:   product,
			}
			if protocol == "http" || protocol == "https" {
				f.Observation.Evidence = map[string]any{
					"http": map[string]any{"status": 200, "title": title},
				}
			}
		}
		return f
	}

	facts := []sqlstore.Fact{
		mk("ip", "203.0.113.10", 22, "ssh", "OpenSSH", ""),
		mk("ip", "203.0.113.10", 80, "http", "nginx", "Redirecting"),
		mk("ip", "203.0.113.10", 443, "https", "nginx", "Admin Login"),
		mk("ip", "203.0.113.11", 8443, "https", "nginx", "Admin Login"),
		mk("ip", "203.0.113.12", 443, "http", "apache", "Portal"),
		mk("ip", "203.0.113.12", 8443, "https", "apache", "Admin Login"),
		mk("domain", "portal.example.com", 0, "", "", ""),
	}
	st, err := store.IngestFacts(context.Background(), facts)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if st.ObservationsInserted != len(facts) {
		t.Fatalf("ingest stats = %+v", st)
	}
	return store, searchlite.New(res.DB)
}

func runSearch(t *testing.T, svc *searchlite.Service, q string, opts func(*search.Query)) *search.Result {
	t.Helper()
	req := search.Query{GQL: q, WithStats: false}
	if opts != nil {
		opts(&req)
	}
	res, err := svc.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("Search(%q): %v", q, err)
	}
	return res
}

// TestSearchParity — the acceptance battery: every supported feature
// answers correctly on the personal engine.
func TestSearchParity(t *testing.T) {
	_, svc := seedIndex(t)

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"eq quoted int", `port="443"`, 2},
		{"eq bare int", `port=443`, 2},
		{"and", `port="443" && protocol="https"`, 1},
		{"or", `port="22" || port="80"`, 2},
		{"not", `not (protocol="https")`, 3}, // NULL rows never match NOT(=)
		{"neq", `protocol!="https"`, 3},
		{"lt", `port<100`, 2},
		{"ge", `port>=443`, 4},
		{"in quoted ints", `port IN ("22", "80")`, 2},
		{"contains", `http.title contains "Portal"`, 1},
		{"fuzzy wildcard", `http.title*="Port?l"`, 1},
		{"fuzzy suffix", `asset_value*="203.0.113.1?"`, 6},
		{"free text", `"OpenSSH"`, 1},
		{"free text or", `"wordpress" || "portal"`, 2}, // title + asset_value
		{"empty value", `http.title=""`, 2},
		{"nonempty", `http.title!=""`, 5},
		{"derived is_ipv6", `is_ipv6="false"`, 6},
		{"in_cidr", `ip IN_CIDR "203.0.113.0/24"`, 6},
		{"in_cidr miss", `ip IN_CIDR "198.51.100.0/24"`, 0},
		{"in_domain", `domain IN_DOMAIN "example.com"`, 1},
		{"host alias ip", `host="203.0.113.10"`, 3},
		{"host alias fuzzy", `host*="203.0.113.1?"`, 6},
		{"alias status_code", `status_code="200"`, 5}, // 5 http(s) services
		{"alias base_protocol", `base_protocol="tcp"`, 6},
		{"after date", `after="2020-01-01"`, 7},
		{"before future", `before="2030-01-01"`, 7},
		{"quantified any host", `ANY(host){ http.status == 200 }`, 6},
		{"quantified any service", `ANY(service){ port == 443 }`, 2},
		{"paren mixed", `(title || product) && port="443"`, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runSearch(t, svc, tc.query, nil)
			if res.Query.RowCount != tc.want {
				t.Errorf("query %q: rows = %d, want %d", tc.query, res.Query.RowCount, tc.want)
			}
		})
	}
}

// TestSearchPagination — keyset pagination walks the full result set
// without duplicates or gaps (the SQ-02 determinism contract).
func TestSearchPagination(t *testing.T) {
	_, svc := seedIndex(t)

	var seen []string
	cursor := ""
	for page := 0; page < 10; page++ {
		res, err := svc.Search(context.Background(), search.Query{
			GQL:    `port>0`,
			Limit:  3,
			Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		for _, r := range res.Rows {
			key := fmt.Sprintf("%d:%s", r.RowID, r.RowKind)
			seen = append(seen, key)
		}
		if !res.HasMore {
			break
		}
		cursor = res.NextCursor
		if cursor == "" {
			t.Fatalf("page %d: HasMore but empty cursor", page)
		}
	}
	if len(seen) != 6 {
		t.Errorf("paginated rows = %d, want 6 (no gaps, no duplicates)", len(seen))
	}
	for i, k := range seen {
		for j := i + 1; j < len(seen); j++ {
			if seen[j] == k {
				t.Errorf("duplicate row across pages: %s", k)
			}
		}
	}
}

// TestSearchAssetMode — one row per asset.
func TestSearchAssetMode(t *testing.T) {
	_, svc := seedIndex(t)
	res := runSearch(t, svc, `port>0`, func(q *search.Query) { q.Mode = "asset" })
	if res.Query.RowCount != 3 {
		t.Errorf("asset-mode rows = %d, want 3 assets", res.Query.RowCount)
	}
}

// TestSearchFacets — every facet dimension computes over the same
// filtered rows.
func TestSearchFacets(t *testing.T) {
	_, svc := seedIndex(t)
	res, err := svc.Facets(context.Background(), search.FacetsQuery{
		GQL: `port>0`,
	})
	if err != nil {
		t.Fatalf("facets: %v", err)
	}
	byName := map[string]map[string]int64{}
	for _, f := range res.Facets {
		m := map[string]int64{}
		for _, v := range f.Values {
			m[v.Value] = v.Count
		}
		byName[f.Name] = m
	}
	port := byName["port"]
	if port["443"] != 2 || port["8443"] != 2 || port["22"] != 1 || port["80"] != 1 {
		t.Errorf("port facet = %v", port)
	}
	proto := byName["protocol"]
	if proto["https"] != 3 || proto["http"] != 2 || proto["ssh"] != 1 {
		t.Errorf("protocol facet = %v", proto)
	}
	svcf := byName["service"]
	if svcf["https"] != 3 || svcf["http"] != 2 || svcf["ssh"] != 1 {
		t.Errorf("service facet = %v", svcf)
	}
	co := byName["country"]
	if co["US"] != 6 {
		t.Errorf("country facet = %v", co)
	}
	if f := byName["freshness"]; f["24h"] != 6 {
		t.Errorf("freshness facet = %v (want all six rows in 24h)", f)
	}
}

// TestSearchNotice — the fixture-only index carries the synthetic-data
// honesty notice.
func TestSearchNotice(t *testing.T) {
	_, svc := seedIndex(t)
	res := runSearch(t, svc, `port>0`, func(q *search.Query) { q.WithStats = true })
	if !strings.Contains(res.Notice, "SYNTHETIC") {
		t.Errorf("fixture-only index must carry the notice, got %q", res.Notice)
	}
}

// TestExplainAndValidate — the personal service exposes both surfaces.
func TestExplainAndValidate(t *testing.T) {
	_, svc := seedIndex(t)
	if err := svc.Validate(`port=443`); err != nil {
		t.Errorf("validate ok query: %v", err)
	}
	if err := svc.Validate(`port="abc"`); err == nil {
		t.Error("validate bad query must fail")
	}
	out, err := svc.Explain(`port=443`)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if !strings.Contains(out, "personal/SQLite dialect") || !strings.Contains(out, "LIMIT ?") {
		t.Errorf("explain output = %q", out)
	}
}

// TestSearchTimeout — a normal query runs under the smallest legal
// timeout without issue (the statement_timeout parity surface).
func TestSearchTimeout(t *testing.T) {
	_, svc := seedIndex(t)
	req := search.Query{GQL: `port>0`, Timeout: 100 * time.Millisecond}
	_, err := svc.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("fast query under tiny timeout must still succeed: %v", err)
	}
}

// TestInvalidQuerySurfaces — an unknown field surfaces the validator's
// error through the service (parity of the GQL chain).
func TestInvalidQuerySurfaces(t *testing.T) {
	_, svc := seedIndex(t)
	if _, err := svc.Search(context.Background(), search.Query{GQL: `nosuchfield="x"`}); err == nil {
		t.Fatal("unknown field must fail")
	}
}
