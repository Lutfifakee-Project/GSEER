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

//go:build integration

package postgres

// suppression_integration_test.go — the M13 suppression-engine integration
// suite: the ingestion gate, the DB backstop trigger (bypass prevention),
// retroactive retraction and the fixture-purge isolation guarantee, all
// against the real migration set on live PostgreSQL.

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Lutfifakee-Project/GSEER/internal/storage"
	"github.com/Lutfifakee-Project/GSEER/internal/suppress"
)

// supTestDB migrates a fresh per-test database to head and opens a store
// over it (pattern shared with the M3/M4 suites).
func supTestDB(t *testing.T) (*Store, *pgxpool.Pool, string) {
	t.Helper()
	dsn, cleanup := testDB(t)
	t.Cleanup(cleanup)
	if _, _, err := RunMigrations(dsn, lockTimeoutForTest()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	pool, err := NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return NewStore(pool), pool, dsn
}

// supSource registers a data source and returns its id.
func supSource(t *testing.T, store *Store, name, connector string) int64 {
	t.Helper()
	var id int64
	err := store.pool.QueryRow(context.Background(),
		`INSERT INTO data_sources (name, connector, terms_ref, enabled) VALUES ($1, $2, 'test', false)
         ON CONFLICT (name) DO UPDATE SET connector = EXCLUDED.connector RETURNING id`,
		name, connector).Scan(&id)
	if err != nil {
		t.Fatalf("register source %s: %v", name, err)
	}
	return id
}

// supObservationID derives a stable caller-assigned observation id (the
// column has no default; same fnv bookkeeping scheme as the fixture
// corpus, namespaced by "sup:" so the two id spaces never collide).
func supObservationID(cursor string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("sup:" + cursor))
	return int64(h.Sum64())
}

// supFact builds an asset-level fact with exactly the kind's typed field
// set (mirroring what real collectors deliver).
func supFact(sourceID int64, cursor string, kind storage.AssetKind, value string) Fact {
	a := storage.Asset{Kind: kind, Value: value, Sources: []string{"suppression-test"}}
	switch kind {
	case storage.AssetKindIP:
		v := value
		a.IP = &v
	case storage.AssetKindDomain:
		v := value
		a.Domain = &v
	case storage.AssetKindHostname:
		v := value
		a.Hostname = &v
	}
	return Fact{
		Asset: a,
		Observation: storage.Observation{
			ID:       supObservationID(cursor),
			SourceID: sourceID, ObservedAt: time.Now().UTC().Truncate(time.Microsecond),
			PayloadHash: fmt.Sprintf("hash-%s", cursor), SourceCursor: cursor,
			Provenance: "observation", Evidence: map[string]any{"k": "v"},
		},
	}
}

// supAssetCount returns how many assets currently match an exact value.
func supAssetCount(t *testing.T, store *Store, kind, value string) int {
	t.Helper()
	var n int
	err := store.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM assets WHERE kind = $1 AND value = $2`, kind, value).Scan(&n)
	if err != nil {
		t.Fatalf("asset count: %v", err)
	}
	return n
}

func TestSuppressionMigrationCreatesObjects(t *testing.T) {
	_, pool, dsn := supTestDB(t)
	for _, rel := range []string{"suppressions", "suppression_applications"} {
		if !relationExists(t, dsn, rel) {
			t.Errorf("relation %s missing after migrations", rel)
		}
	}
	var triggers int
	if err := pool.QueryRow(context.Background(), `
SELECT count(DISTINCT trigger_name) FROM information_schema.triggers
WHERE event_object_table = 'assets' AND trigger_name = 'trg_assets_suppression'`).Scan(&triggers); err != nil || triggers != 1 {
		t.Errorf("backstop trigger missing (count=%d, err=%v)", triggers, err)
	}
}

func TestSuppressionGateBlocksIngestFacts(t *testing.T) {
	store, _, _ := supTestDB(t)
	ctx := context.Background()
	src := supSource(t, store, "gate-src", "test")

	rule, _, err := store.AddSuppression(ctx, SuppressInput{Kind: suppress.KindIP, Value: "192.0.2.9", Reason: "test ip", CreatedBy: "t"})
	if err != nil {
		t.Fatalf("add rule: %v", err)
	}

	facts := []Fact{supFact(src, "c1", storage.AssetKindIP, "192.0.2.9"), supFact(src, "c2", storage.AssetKindIP, "192.0.2.10")}
	stats, err := store.IngestFacts(ctx, facts)
	if err != nil {
		t.Fatalf("IngestFacts: %v", err)
	}
	if stats.Suppressed != 1 {
		t.Errorf("Suppressed = %d, want 1 (stats: %+v)", stats.Suppressed, stats)
	}
	if stats.AssetsInserted != 1 {
		t.Errorf("AssetsInserted = %d, want 1", stats.AssetsInserted)
	}
	if got := supAssetCount(t, store, "ip", "192.0.2.9"); got != 0 {
		t.Errorf("suppressed asset was written (%d rows)", got)
	}
	if got := supAssetCount(t, store, "ip", "192.0.2.10"); got != 1 {
		t.Errorf("allowed asset missing (%d rows)", got)
	}
	// Audit row present with the full identity.
	var detail string
	var action string
	if err := store.pool.QueryRow(ctx,
		`SELECT action, detail::text FROM suppression_applications WHERE suppression_id = $1`, rule.ID).
		Scan(&action, &detail); err != nil {
		t.Fatalf("audit row missing: %v", err)
	}
	if action != "ingest-blocked" || !strings.Contains(detail, "192.0.2.9") {
		t.Errorf("audit row = %s / %s", action, detail)
	}
}

func TestSuppressionCIDRv4AndV6(t *testing.T) {
	store, _, _ := supTestDB(t)
	ctx := context.Background()
	src := supSource(t, store, "cidr-src", "test")
	if _, _, err := store.AddSuppression(ctx, SuppressInput{Kind: suppress.KindCIDR, Value: "203.0.113.0/24", Reason: "v4 range"}); err != nil {
		t.Fatalf("add v4 rule: %v", err)
	}
	if _, _, err := store.AddSuppression(ctx, SuppressInput{Kind: suppress.KindCIDR, Value: "2001:db8:abcd::/48", Reason: "v6 range"}); err != nil {
		t.Fatalf("add v6 rule: %v", err)
	}

	inside := []string{"203.0.113.1", "203.0.113.254", "2001:db8:abcd::1"}
	for i, v := range inside {
		res, err := store.IngestFact(ctx, func() *Fact { f := supFact(src, fmt.Sprintf("in-%d", i), storage.AssetKindIP, v); return &f }())
		if err != nil {
			t.Fatalf("IngestFact inside %s: %v", v, err)
		}
		if !res.Suppressed {
			t.Errorf("asset inside CIDR %s was not suppressed", v)
		}
	}
	outside := []string{"203.0.114.1", "2001:db8:abce::1"}
	for i, v := range outside {
		res, err := store.IngestFact(ctx, func() *Fact { f := supFact(src, fmt.Sprintf("out-%d", i), storage.AssetKindIP, v); return &f }())
		if err != nil {
			t.Fatalf("IngestFact outside %s: %v", v, err)
		}
		if res.Suppressed {
			t.Errorf("asset outside CIDR %s was suppressed", v)
		}
	}
}

func TestSuppressionDomainSuffixLabelBoundary(t *testing.T) {
	store, _, _ := supTestDB(t)
	ctx := context.Background()
	src := supSource(t, store, "suffix-src", "test")
	if _, _, err := store.AddSuppression(ctx, SuppressInput{Kind: suppress.KindDomainSuffix, Value: "Example.COM.", Reason: "boundary test"}); err != nil {
		t.Fatalf("add rule: %v", err)
	}

	blocked := []string{"example.com", "sub.example.com", "deep.a.b.example.com", "EXAMPLE.COM."}
	for i, v := range blocked {
		res, err := store.IngestFact(ctx, func() *Fact { f := supFact(src, fmt.Sprintf("b-%d", i), storage.AssetKindDomain, v); return &f }())
		if err != nil {
			t.Fatalf("IngestFact(%s): %v", v, err)
		}
		if !res.Suppressed {
			t.Errorf("%s not suppressed by suffix rule", v)
		}
	}
	allowed := []string{"notexample.com", "example.com.evil.net", "example.comx"}
	for i, v := range allowed {
		res, err := store.IngestFact(ctx, func() *Fact { f := supFact(src, fmt.Sprintf("a-%d", i), storage.AssetKindDomain, v); return &f }())
		if err != nil {
			t.Fatalf("IngestFact(%s): %v", v, err)
		}
		if res.Suppressed {
			t.Errorf("%s wrongly suppressed", v)
		}
	}
}

func TestSuppressionIDNARuleMatchesPunycodeInput(t *testing.T) {
	store, _, _ := supTestDB(t)
	ctx := context.Background()
	src := supSource(t, store, "idna-src", "test")
	if _, _, err := store.AddSuppression(ctx, SuppressInput{Kind: suppress.KindDomain, Value: "münchen.de", Reason: "idna test"}); err != nil {
		t.Fatalf("add rule: %v", err)
	}
	res, err := store.IngestFact(ctx, func() *Fact { f := supFact(src, "i1", storage.AssetKindDomain, "xn--mnchen-3ya.de"); return &f }())
	if err != nil {
		t.Fatalf("IngestFact: %v", err)
	}
	if !res.Suppressed {
		t.Error("punycode input must be suppressed by unicode rule")
	}
	res, err = store.IngestFact(ctx, func() *Fact { f := supFact(src, "i2", storage.AssetKindDomain, "münchen.de"); return &f }())
	if err != nil {
		t.Fatalf("IngestFact: %v", err)
	}
	if !res.Suppressed {
		t.Error("unicode input must be suppressed by unicode rule")
	}
	if got := supAssetCount(t, store, "domain", "xn--mnchen-3ya.de"); got != 0 {
		t.Errorf("suppressed IDNA asset written (%d rows)", got)
	}
}

func TestSuppressionSourceExclusion(t *testing.T) {
	store, _, _ := supTestDB(t)
	ctx := context.Background()
	blockedSrc := supSource(t, store, "blocked-feed", "test")
	allowedSrc := supSource(t, store, "allowed-feed", "test")

	if _, _, err := store.AddSuppression(ctx, SuppressInput{Kind: suppress.KindSource, Value: "blocked-feed", Reason: "excluded source"}); err != nil {
		t.Fatalf("add rule: %v", err)
	}
	res, err := store.IngestFact(ctx, func() *Fact { f := supFact(blockedSrc, "s1", storage.AssetKindIP, "198.51.100.1"); return &f }())
	if err != nil {
		t.Fatalf("IngestFact: %v", err)
	}
	if !res.Suppressed {
		t.Error("fact from excluded source must be suppressed")
	}
	res, err = store.IngestFact(ctx, func() *Fact { f := supFact(allowedSrc, "s2", storage.AssetKindIP, "198.51.100.1"); return &f }())
	if err != nil {
		t.Fatalf("IngestFact: %v", err)
	}
	if res.Suppressed {
		t.Error("fact from allowed source must not be suppressed")
	}
}

func TestSuppressionExpirationSkipsRule(t *testing.T) {
	store, _, _ := supTestDB(t)
	ctx := context.Background()
	src := supSource(t, store, "exp-src", "test")

	past := time.Now().Add(-time.Minute)
	if _, _, err := store.AddSuppression(ctx, SuppressInput{Kind: suppress.KindIP, Value: "198.51.100.5", Reason: "expired", ExpiresAt: &past}); err != nil {
		t.Fatalf("add expired rule: %v", err)
	}
	res, err := store.IngestFact(ctx, func() *Fact { f := supFact(src, "e1", storage.AssetKindIP, "198.51.100.5"); return &f }())
	if err != nil {
		t.Fatalf("IngestFact: %v", err)
	}
	if res.Suppressed {
		t.Error("expired rule must not suppress")
	}

	future := time.Now().Add(time.Hour)
	if _, _, err := store.AddSuppression(ctx, SuppressInput{Kind: suppress.KindIP, Value: "198.51.100.6", Reason: "live", ExpiresAt: &future}); err != nil {
		t.Fatalf("add live rule: %v", err)
	}
	res, err = store.IngestFact(ctx, func() *Fact { f := supFact(src, "e2", storage.AssetKindIP, "198.51.100.6"); return &f }())
	if err != nil {
		t.Fatalf("IngestFact: %v", err)
	}
	if !res.Suppressed {
		t.Error("unexpired rule must suppress")
	}
}

func TestSuppressionUpsertAssetGateError(t *testing.T) {
	store, _, _ := supTestDB(t)
	ctx := context.Background()
	if _, _, err := store.AddSuppression(ctx, SuppressInput{Kind: suppress.KindIP, Value: "198.51.100.77", Reason: "direct"}); err != nil {
		t.Fatalf("add rule: %v", err)
	}
	ip := "198.51.100.77"
	_, _, err := store.UpsertAsset(ctx, &storage.Asset{Kind: storage.AssetKindIP, Value: ip, IP: &ip})
	var dbErr *DBError
	if !errors.As(err, &dbErr) || dbErr.Code != CodeDBSuppressed {
		t.Fatalf("UpsertAsset error = %v, want %s", err, CodeDBSuppressed)
	}
	if got := supAssetCount(t, store, "ip", ip); got != 0 {
		t.Errorf("gated upsert wrote %d rows", got)
	}
	// An unsuppressed direct upsert still works.
	ok := "198.51.100.78"
	if _, _, err := store.UpsertAsset(ctx, &storage.Asset{Kind: storage.AssetKindIP, Value: ok, IP: &ok}); err != nil {
		t.Fatalf("UpsertAsset allowed: %v", err)
	}
}

// TestSuppressionBackstopTriggerBypass is the bypass-prevention proof:
// a raw-SQL INSERT (no Go gate) that matches an enabled rule is rejected
// by the database trigger with E_DB_SUPPRESSED.
func TestSuppressionBackstopTriggerBypass(t *testing.T) {
	store, pool, _ := supTestDB(t)
	ctx := context.Background()
	if _, _, err := store.AddSuppression(ctx, SuppressInput{Kind: suppress.KindDomainSuffix, Value: "evil.example", Reason: "backstop"}); err != nil {
		t.Fatalf("add rule: %v", err)
	}
	cases := []struct {
		kind, value, typed string
	}{
		{"domain", "sub.evil.example", "sub.evil.example"},
		{"domain", "evil.example", "evil.example"},
		{"hostname", "host.evil.example", "host.evil.example"},
	}
	for _, tc := range cases {
		_, err := pool.Exec(ctx, `INSERT INTO assets (kind, value, domain, hostname, sources)
VALUES ($1, $2, $3, $3, '{}')`, tc.kind, tc.value, tc.typed)
		var dbErr *DBError
		if !errors.As(mapSuppressionTrigger(err), &dbErr) || dbErr.Code != CodeDBSuppressed {
			t.Errorf("raw insert %s=%s bypassed the backstop: %v", tc.kind, tc.value, err)
		}
	}
	// A non-matching insert still succeeds (trigger selective).
	if _, err := pool.Exec(ctx, `INSERT INTO assets (kind, value, domain, sources)
VALUES ('domain', 'fine.example', 'fine.example', '{}')`); err != nil {
		t.Errorf("non-matching insert failed: %v", err)
	}
}

func TestSuppressionNormalizeOnWrite(t *testing.T) {
	store, pool, _ := supTestDB(t)
	ctx := context.Background()
	src := supSource(t, store, "norm-src", "test")
	res, err := store.IngestFact(ctx, func() *Fact { f := supFact(src, "n1", storage.AssetKindDomain, "WWW.Example.COM."); return &f }())
	if err != nil {
		t.Fatalf("IngestFact: %v", err)
	}
	if res.Suppressed {
		t.Fatal("normalization test fact wrongly suppressed")
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT value FROM assets WHERE domain = 'www.example.com'`).Scan(&stored); err != nil {
		t.Fatalf("canonical asset row missing: %v", err)
	}
	if stored != "www.example.com" {
		t.Errorf("stored value = %q, want www.example.com", stored)
	}
	// IPv6 canonicalization on write.
	v6 := "2001:0DB8:0000::1"
	if _, _, err := store.UpsertAsset(ctx, &storage.Asset{Kind: storage.AssetKindIP, Value: v6, IP: &v6}); err != nil {
		t.Fatalf("UpsertAsset v6: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT value FROM assets WHERE ip = '2001:db8::1'`).Scan(&stored); err != nil {
		t.Fatalf("canonical v6 row missing: %v", err)
	}
	if stored != "2001:db8::1" {
		t.Errorf("stored v6 = %q", stored)
	}
}

func TestSuppressionRetractionIPExact(t *testing.T) {
	store, pool, _ := supTestDB(t)
	ctx := context.Background()
	src := supSource(t, store, "retract-src", "test")
	// Two assets; one will be retracted.
	if _, err := store.IngestFacts(ctx, []Fact{
		supFact(src, "r1", storage.AssetKindIP, "198.51.100.10"),
		supFact(src, "r2", storage.AssetKindIP, "198.51.100.11"),
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	rule, _, err := store.AddSuppression(ctx, SuppressInput{Kind: suppress.KindIP, Value: "198.51.100.10", Reason: "retract me"})
	if err != nil {
		t.Fatalf("add rule: %v", err)
	}
	stats, err := store.RetractSuppressed(ctx, rule)
	if err != nil {
		t.Fatalf("RetractSuppressed: %v", err)
	}
	if stats.AssetsRetracted != 1 {
		t.Errorf("AssetsRetracted = %d, want 1 (%+v)", stats.AssetsRetracted, stats)
	}
	if stats.ObservationsRemoved != 1 {
		t.Errorf("ObservationsRemoved = %d, want 1", stats.ObservationsRemoved)
	}
	if got := supAssetCount(t, store, "ip", "198.51.100.10"); got != 0 {
		t.Errorf("retracted asset still present (%d)", got)
	}
	if got := supAssetCount(t, store, "ip", "198.51.100.11"); got != 1 {
		t.Errorf("sibling asset was deleted (%d)", got)
	}
	// Audit row survives the asset it recorded.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM suppression_applications
WHERE suppression_id = $1 AND action = 'retroactive-retract'`, rule.ID).Scan(&n); err != nil || n != 1 {
		t.Errorf("audit rows = %d, err %v; want 1", n, err)
	}
}

func TestSuppressionRetractionDomainSuffix(t *testing.T) {
	store, _, _ := supTestDB(t)
	ctx := context.Background()
	src := supSource(t, store, "retract-dns-src", "test")
	if _, err := store.IngestFacts(ctx, []Fact{
		supFact(src, "d1", storage.AssetKindHostname, "a.bad.example"),
		supFact(src, "d2", storage.AssetKindHostname, "b.bad.example"),
		supFact(src, "d3", storage.AssetKindDomain, "fine.example"),
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	rule, _, err := store.AddSuppression(ctx, SuppressInput{Kind: suppress.KindDomainSuffix, Value: "bad.example", Reason: "suffix retract"})
	if err != nil {
		t.Fatalf("add rule: %v", err)
	}
	stats, err := store.RetractSuppressed(ctx, rule)
	if err != nil {
		t.Fatalf("RetractSuppressed: %v", err)
	}
	if stats.AssetsRetracted != 2 {
		t.Errorf("AssetsRetracted = %d, want 2 (%+v)", stats.AssetsRetracted, stats)
	}
	if got := supAssetCount(t, store, "domain", "fine.example"); got != 1 {
		t.Errorf("unrelated asset deleted (%d)", got)
	}
	// search_index no longer exposes them.
	var visible int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM search_index WHERE asset_value LIKE '%.bad.example'`).Scan(&visible); err != nil {
		t.Fatalf("search_index count: %v", err)
	}
	if visible != 0 {
		t.Errorf("%d suppressed rows still visible in search_index", visible)
	}
}

func TestSuppressionRetractionSourceExclusionIsolation(t *testing.T) {
	store, pool, _ := supTestDB(t)
	ctx := context.Background()

	// A real-observation source + the labeled fixture source coexist.
	realSrc := supSource(t, store, "real-feed", "test")
	fixtureSrc := supSource(t, store, "co-fixture", "fixture")

	fixtureID, err := store.EnsureFixtureSource(ctx)
	_ = fixtureID
	if err != nil {
		t.Fatalf("EnsureFixtureSource: %v", err)
	}
	_ = fixtureSrc

	// supFactN: same fact but the asset's sources[] names the delivering
	// source (what real collectors do — and what source retraction strips).
	supFactN := func(sourceID int64, name, cursor string, kind storage.AssetKind, value string) Fact {
		f := supFact(sourceID, cursor, kind, value)
		f.Asset.Sources = []string{name}
		return f
	}

	if _, err := store.IngestFacts(ctx, []Fact{
		supFactN(realSrc, "real-feed", "real1", storage.AssetKindIP, "198.51.100.20"),
		supFactN(realSrc, "real-feed", "real2", storage.AssetKindIP, "198.51.100.21"),
	}); err != nil {
		t.Fatalf("ingest real: %v", err)
	}

	// Retract the real source entirely.
	rule, _, err := store.AddSuppression(ctx, SuppressInput{Kind: suppress.KindSource, Value: "real-feed", Reason: "source retract"})
	if err != nil {
		t.Fatalf("add rule: %v", err)
	}
	stats, err := store.RetractSuppressed(ctx, rule)
	if err != nil {
		t.Fatalf("RetractSuppressed: %v", err)
	}
	if stats.ObservationsRemoved != 2 || stats.AssetsRetracted != 2 {
		t.Errorf("retraction stats = %+v, want 2 obs / 2 assets", stats)
	}
	if got := supAssetCount(t, store, "ip", "198.51.100.20"); got != 0 {
		t.Errorf("retracted-source asset survived (%d)", got)
	}
	// The fixture source row is untouched by the source retraction.
	var fixtureRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM data_sources WHERE name = 'fixture'`).Scan(&fixtureRows); err != nil || fixtureRows != 1 {
		t.Errorf("fixture source rows = %d, err %v; want 1", fixtureRows, err)
	}
	// Audit history survives.
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM suppression_applications WHERE action = 'retract-source'`).Scan(&audits); err != nil || audits < 1 {
		t.Errorf("retract-source audit rows = %d, err %v", audits, err)
	}
}

func TestSuppressionFixturePurgeIsolation(t *testing.T) {
	store, pool, _ := supTestDB(t)
	ctx := context.Background()

	// The fixture source registry row + a fixture-provenance fact (QS-6
	// style: provenance=fixture, written through the normal ingestion
	// path — exactly what --load-fixtures does).
	if _, err := store.EnsureFixtureSource(ctx); err != nil {
		t.Fatalf("EnsureFixtureSource: %v", err)
	}
	var fixtureID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM data_sources WHERE name = 'fixture'`).Scan(&fixtureID); err != nil {
		t.Fatalf("fixture source id: %v", err)
	}
	realSrc := supSource(t, store, "isolation-real", "test")

	fixtureFact := supFact(fixtureID, "fx-iso-1", storage.AssetKindDomain, "portal.example.net")
	fixtureFact.Observation.Provenance = storage.ProvenanceFixture
	fixtureFact.Asset.Sources = []string{storage.FixtureSourceName}

	// Real observation first (so a buggy purge would delete it only via
	// suppression scope, never via provenance).
	if _, err := store.IngestFacts(ctx, []Fact{
		fixtureFact,
		supFact(realSrc, "iso1", storage.AssetKindIP, "198.51.100.30"),
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	// A suppression rule that matches the FIXTURE asset (example.net).
	rule, _, err := store.AddSuppression(ctx, SuppressInput{Kind: suppress.KindDomainSuffix, Value: "example.net", Reason: "fixture isolation test"})
	if err != nil {
		t.Fatalf("add rule: %v", err)
	}
	retract, err := store.RetractSuppressed(ctx, rule)
	if err != nil {
		t.Fatalf("RetractSuppressed: %v", err)
	}
	if retract.AssetsRetracted != 1 {
		t.Errorf("fixture asset not retracted: %+v", retract)
	}
	if got := supAssetCount(t, store, "domain", "portal.example.net"); got != 0 {
		t.Errorf("suppressed fixture asset survived retraction (%d)", got)
	}

	// Fixture purge afterwards: scoped to provenance=fixture only.
	stats, err := PurgeFixtureData(ctx, pool)
	if err != nil {
		t.Fatalf("PurgeFixtureData: %v", err)
	}
	_ = stats
	// The real observation survives the purge AND the suppression (the
	// rule never touched it).
	if got := supAssetCount(t, store, "ip", "198.51.100.30"); got != 1 {
		t.Errorf("real observation asset lost (%d rows)", got)
	}
	// Suppression audit rows are NOT part of the fixture corpus: the
	// retraction's audit row survives the purge.
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM suppression_applications
WHERE suppression_id = $1 AND action = 'retroactive-retract'`, rule.ID).Scan(&audits); err != nil || audits != 1 {
		t.Errorf("suppression audit rows = %d (err %v), want 1 — audit must survive the fixture purge", audits, err)
	}
}

func TestSuppressionAuditSurvivesRuleRemoval(t *testing.T) {
	store, pool, _ := supTestDB(t)
	ctx := context.Background()
	src := supSource(t, store, "rm-src", "test")
	rule, _, err := store.AddSuppression(ctx, SuppressInput{Kind: suppress.KindIP, Value: "198.51.100.40", Reason: "will be removed"})
	if err != nil {
		t.Fatalf("add rule: %v", err)
	}
	if _, err := store.IngestFact(ctx, func() *Fact { f := supFact(src, "rm1", storage.AssetKindIP, "198.51.100.40"); return &f }()); err != nil {
		t.Fatalf("IngestFact: %v", err)
	}
	ok, err := store.RemoveSuppression(ctx, rule.ID)
	if err != nil || !ok {
		t.Fatalf("RemoveSuppression = %v, %v", ok, err)
	}
	// The blocked fact ingests now.
	res, err := store.IngestFact(ctx, func() *Fact { f := supFact(src, "rm2", storage.AssetKindIP, "198.51.100.40"); return &f }())
	if err != nil {
		t.Fatalf("IngestFact after removal: %v", err)
	}
	if res.Suppressed {
		t.Error("fact suppressed by a REMOVED rule")
	}
	// The audit row survives with suppression_id NULLed.
	var action string
	var detail string
	err = pool.QueryRow(ctx, `SELECT action, detail::text FROM suppression_applications
WHERE detail->>'rule_value' = '198.51.100.40' AND suppression_id IS NULL`).Scan(&action, &detail)
	if err != nil {
		t.Fatalf("audit row did not survive rule removal: %v", err)
	}
	if action != "ingest-blocked" {
		t.Errorf("audit action = %s", action)
	}
}

func TestSuppressionInvalidRuleValueRejected(t *testing.T) {
	store, _, _ := supTestDB(t)
	ctx := context.Background()
	_, _, err := store.AddSuppression(ctx, SuppressInput{Kind: suppress.KindIP, Value: "999.1.2.3", Reason: "bad"})
	var dbErr *DBError
	if !errors.As(err, &dbErr) || dbErr.Code != CodeDBUsage {
		t.Fatalf("AddSuppression bad IP error = %v, want %s", err, CodeDBUsage)
	}
	_, _, err = store.AddSuppression(ctx, SuppressInput{Kind: suppress.KindDomain, Value: "bad..domain", Reason: "bad"})
	if !errors.As(err, &dbErr) || dbErr.Code != CodeDBUsage {
		t.Fatalf("AddSuppression bad domain error = %v, want %s", err, CodeDBUsage)
	}
}
