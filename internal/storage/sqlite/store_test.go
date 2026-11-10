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

package sqlite

// store_test.go — the SQ-01/SQ-02/SQ-06 core behavior tests against a
// real SQLite engine (file-backed, in the test's temp dir).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/storage"
	"github.com/Lutfifakee-Project/GSEER/internal/suppress"
)

// openTestStore opens a migrated personal store in a fresh temp dir.
func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gseer.db")
	res, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = res.DB.Close() })
	if _, err := Migrate(context.Background(), res.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return NewStore(res.DB), path
}

// assetFact builds one asset-level fact (no service).
func assetFact(sourceID int64, kind storage.AssetKind, value string) Fact {
	f := Fact{
		Observation: storage.Observation{
			ID:           nextObsID(),
			SourceID:     sourceID,
			SourceCursor: "cursor-" + value,
			PayloadHash:  "hash-" + value,
			Provenance:   storage.ProvenanceFixture,
			ObservedAt:   time.Now().UTC(),
		},
	}
	switch kind {
	case storage.AssetKindIP:
		ip := value
		f.Asset = storage.Asset{Kind: kind, Value: value, IP: &ip}
	case storage.AssetKindDomain:
		d := value
		f.Asset = storage.Asset{Kind: kind, Value: value, Domain: &d}
	default:
		h := value
		f.Asset = storage.Asset{Kind: kind, Value: value, Hostname: &h}
	}
	return f
}

var obsCounter atomic.Int64

func nextObsID() int64 {
	return obsCounter.Add(1)
}

// serviceFact builds one service fact on the given asset.
func serviceFact(sourceID int64, assetValue string, port int, protocol, product string) Fact {
	f := assetFact(sourceID, storage.AssetKindIP, assetValue)
	f.Observation.SourceCursor = "cursor-" + assetValue + "-svc-" + fmt.Sprint(port)
	f.Observation.PayloadHash = "hash-" + assetValue + "-svc-" + fmt.Sprint(port)
	f.Service = &storage.Service{
		Port:      port,
		Transport: "tcp",
		Protocol:  protocol,
		Product:   product,
	}
	return f
}

// ensureSource registers a test source.
func ensureSource(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	id, err := s.EnsureSource(context.Background(), name, "fixture", "", nil)
	if err != nil {
		t.Fatalf("EnsureSource(%q): %v", name, err)
	}
	return id
}

// ---------------------------------------------------------------------------
// SQ-01 — upserts return the STORED first_seen, never the input.
// ---------------------------------------------------------------------------

func TestUpsertAssetFirstSeenStoredWins(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	a := storage.Asset{Kind: storage.AssetKindIP, GeoCountry: "US", ASN: 13335}
	ip := "192.0.2.10"
	a.IP = &ip
	a.Value = ip
	id1, inserted1, err := s.UpsertAsset(ctx, &a)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if !inserted1 {
		t.Fatal("first upsert must insert")
	}
	original := a.FirstSeen
	if a.LastSeen.IsZero() || FormatTS(original) != FormatTS(a.LastSeen) {
		t.Fatalf("insert must set first=last (got %v/%v)", a.FirstSeen, a.LastSeen)
	}

	// Time passes; the second sighting claims a 1999 first_seen and a
	// newer last_seen plus fresh metadata.
	time.Sleep(15 * time.Millisecond)
	bogus := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Now().UTC().Add(5 * time.Second)
	a2 := storage.Asset{Kind: storage.AssetKindIP, GeoCountry: "DE"}
	ip2 := "192.0.2.10"
	a2.IP = &ip2
	a2.Value = ip2
	a2.FirstSeen = bogus
	a2.LastSeen = newer
	id2, inserted2, err := s.UpsertAsset(ctx, &a2)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if inserted2 {
		t.Fatal("second upsert must update")
	}
	if id2 != id1 {
		t.Fatalf("id drift: %d vs %d", id1, id2)
	}
	if FormatTS(a2.FirstSeen) != FormatTS(original) {
		t.Errorf("SQ-01 VIOLATION: returned first_seen %v, want stored %v (the 1999 input must never echo back)", a2.FirstSeen, original)
	}
	if !a2.LastSeen.After(original) {
		t.Errorf("last_seen must advance: %v vs %v", a2.LastSeen, original)
	}
	if a2.GeoCountry != "DE" {
		t.Errorf("non-empty incoming metadata must overwrite (got %q, want DE)", a2.GeoCountry)
	}
}

func TestUpsertAssetSourcesUnion(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	a := storage.Asset{Kind: storage.AssetKindDomain}
	d := "Example.com."
	a.Domain = &d
	a.Value = d
	a.Sources = []string{"src-a"}
	if _, _, err := s.UpsertAsset(ctx, &a); err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	// Re-sight from another source; also proves normalization (trailing
	// dot + case folded to the canonical value).
	a2 := storage.Asset{Kind: storage.AssetKindDomain}
	d2 := "example.com"
	a2.Domain = &d2
	a2.Value = d2
	a2.Sources = []string{"src-b"}
	if _, _, err := s.UpsertAsset(ctx, &a2); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	if a2.Value != "example.com" {
		t.Fatalf("value normalization: %q", a2.Value)
	}
	var raw string
	if err := s.DB.QueryRow(`SELECT sources FROM assets WHERE kind='domain' AND value='example.com'`).Scan(&raw); err != nil {
		t.Fatalf("read sources: %v", err)
	}
	got := jsonSources(raw)
	if len(got) != 2 || got[0] != "src-a" || got[1] != "src-b" {
		t.Errorf("sources = %v, want [src-a src-b]", got)
	}
}

func TestUpsertServiceFirstSeenStoredWins(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	src := ensureSource(t, s, "svc-test")

	f := serviceFact(src, "198.51.100.5", 443, "https", "nginx")
	if _, err := s.IngestFact(ctx, &f); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	original := f.Service.FirstSeen

	time.Sleep(15 * time.Millisecond)
	sv := storage.Service{AssetID: f.Asset.ID, Port: 443, Transport: "tcp", Protocol: "https", Version: "1.25"}
	bogus := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	sv.FirstSeen = bogus
	id, inserted, err := s.UpsertService(ctx, &sv)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if inserted || id != f.Service.ID {
		t.Fatalf("must update the same row (inserted=%v id=%d want %d)", inserted, id, f.Service.ID)
	}
	if FormatTS(sv.FirstSeen) != FormatTS(original) {
		t.Errorf("SQ-01 VIOLATION: service first_seen %v, want stored %v", sv.FirstSeen, original)
	}
	if sv.Version != "1.25" {
		t.Errorf("identity fields must be replaced on conflict (version=%q)", sv.Version)
	}
}

// ---------------------------------------------------------------------------
// SQ-02 — sub-millisecond timestamps: L2 dedup exact, order chronological.
// ---------------------------------------------------------------------------

func TestObservationSubMillisecondPrecision(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	src := ensureSource(t, s, "precision")

	base := time.Now().UTC().Truncate(time.Second)
	build := func(obs time.Time, cursor string) Fact {
		f := assetFact(src, storage.AssetKindIP, "198.51.100.20")
		f.Observation.ObservedAt = obs
		f.Observation.SourceCursor = cursor
		f.Observation.PayloadHash = "hash-precision"
		return f
	}

	t1 := base.Add(10 * time.Millisecond)
	t2 := base.Add(10*time.Millisecond + time.Microsecond) // 1µs apart

	res1, err := s.IngestFact(ctx, ptr(build(t1, "c1")))
	if err != nil {
		t.Fatalf("ingest t1: %v", err)
	}
	res2, err := s.IngestFact(ctx, ptr(build(t2, "c2")))
	if err != nil {
		t.Fatalf("ingest t2: %v", err)
	}
	if res1.ObservationID == res2.ObservationID {
		t.Fatal("observations 1µs apart must NOT dedupe (sub-ms precision lost)")
	}

	// Identical content at the identical microsecond dedupes.
	res3, err := s.IngestFact(ctx, ptr(build(t1, "c1")))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res3.ObservationID != res1.ObservationID {
		t.Fatalf("L1 replay must dedupe onto %d, got %d", res1.ObservationID, res3.ObservationID)
	}

	var n int
	if err := s.DB.QueryRow(`SELECT count(*) FROM observations WHERE payload_hash='hash-precision'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("observation rows = %d, want 2 (one per distinct microsecond)", n)
	}

	// Lexicographic == chronological for the stored format.
	var earlier, later string
	if err := s.DB.QueryRow(`SELECT observed_at FROM observations WHERE source_cursor='c1'`).Scan(&earlier); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT observed_at FROM observations WHERE source_cursor='c2'`).Scan(&later); err != nil {
		t.Fatal(err)
	}
	if !(earlier < later) {
		t.Errorf("fixed-width format must sort chronologically: %q vs %q", earlier, later)
	}
}

func ptr[T any](v T) *T { return &v }

// ---------------------------------------------------------------------------
// IngestFact atomicity + stats.
// ---------------------------------------------------------------------------

func TestIngestFactAtomicRollbackOnUnknownSource(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	f := assetFact(9999, storage.AssetKindIP, "198.51.100.30") // unregistered source
	if _, err := s.IngestFact(ctx, &f); err == nil {
		t.Fatal("ingest with an unregistered source must fail")
	}
	var n int
	if err := s.DB.QueryRow(`SELECT count(*) FROM assets WHERE value='198.51.100.30'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("a rolled-back fact must leave NO asset row (atomicity)")
	}
}

func TestIngestStatsAndIdempotentReplay(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	src := ensureSource(t, s, "stats")

	facts := []Fact{
		assetFact(src, storage.AssetKindIP, "198.51.100.1"),
		serviceFact(src, "198.51.100.2", 22, "ssh", "OpenSSH"),
		serviceFact(src, "198.51.100.2", 443, "https", "nginx"),
	}
	st, err := s.IngestFacts(ctx, facts)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	// Assets: .1 and .2 insert; the second service on .2 updates it.
	if st.AssetsInserted != 2 || st.AssetsUpdated != 1 || st.ServicesInserted != 2 || st.ObservationsInserted != 3 {
		t.Fatalf("stats = %+v", st)
	}
	st2, err := s.IngestFacts(ctx, facts)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if st2.ObservationsInserted != 0 || st2.ObservationsDeduped != 3 {
		t.Errorf("replay stats = %+v, want 0 inserted / 3 deduped", st2)
	}
	if st2.AssetsUpdated != 3 || st2.ServicesUpdated != 2 {
		t.Errorf("replay stats = %+v, want 3 asset updates / 2 service updates", st2)
	}
}

// ---------------------------------------------------------------------------
// SQ-06 — suppression gate, audit, expiry, deny-wins, backstop trigger.
// ---------------------------------------------------------------------------

func TestSuppressionGateSkipsAndAudits(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	src := ensureSource(t, s, "gate-src")

	_, _, err := s.AddSuppression(ctx, SuppressInput{
		Kind: suppress.KindIP, Value: "198.51.100.66", Reason: "owner request", CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("add rule: %v", err)
	}

	// Gated fact: skipped, nothing written, one audit row.
	f := assetFact(src, storage.AssetKindIP, "198.51.100.66")
	res, err := s.IngestFact(ctx, &f)
	if err != nil {
		t.Fatalf("gated ingest: %v", err)
	}
	if !res.Suppressed {
		t.Fatal("fact must be suppressed")
	}
	var assets, obs int
	_ = s.DB.QueryRow(`SELECT count(*) FROM assets WHERE value='198.51.100.66'`).Scan(&assets)
	_ = s.DB.QueryRow(`SELECT count(*) FROM observations WHERE asset_value_maybe`).Scan(&obs) //nolint
	_ = obs
	if assets != 0 {
		t.Fatal("a suppressed fact must write NOTHING (not even asset metadata)")
	}
	var audits int
	if err := s.DB.QueryRow(`SELECT count(*) FROM suppression_applications WHERE action='ingest-blocked'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Errorf("ingest-block audit rows = %d, want 1", audits)
	}

	// A DIRECT upsert of a suppressed asset is an error (single record).
	ip := "198.51.100.66"
	a := storage.Asset{Kind: storage.AssetKindIP, IP: &ip, Value: ip}
	if _, _, err := s.UpsertAsset(ctx, &a); !IsSuppressedError(err) {
		t.Fatalf("single-record upsert of a suppressed asset must be an error, got %v", err)
	}

	// The backstop trigger refuses RAW writes too — no bypass.
	_, err = s.DB.Exec(`INSERT INTO assets (kind, value, ip, first_seen, last_seen, sources)
                VALUES ('ip', '198.51.100.66', '198.51.100.66', strftime('%Y-%m-%dT%H:%M:%fZ','now'), strftime('%Y-%m-%dT%H:%M:%fZ','now'), '[]')`)
	if err == nil {
		t.Fatal("the GSUP1 backstop trigger must refuse raw SQL writes")
	}
	if !IsSuppressedError(err) {
		t.Errorf("raw write error must map to suppression refusal, got: %v", err)
	}
}

func TestSuppressionExpiryAndDenyWins(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	src := ensureSource(t, s, "expiry-src")

	past := time.Now().UTC().Add(-time.Hour)
	if _, _, err := s.AddSuppression(ctx, SuppressInput{
		Kind: suppress.KindIP, Value: "198.51.100.70", Reason: "expired", CreatedBy: "test", ExpiresAt: &past,
	}); err != nil {
		t.Fatalf("add expired rule: %v", err)
	}
	f := assetFact(src, storage.AssetKindIP, "198.51.100.70")
	res, err := s.IngestFact(ctx, &f)
	if err != nil {
		t.Fatalf("ingest with expired rule: %v", err)
	}
	if res.Suppressed {
		t.Fatal("an expired rule must NOT suppress (deny-wins over time)")
	}

	// A DISABLED rule must not suppress either.
	future := time.Now().UTC().Add(time.Hour)
	rule, _, err := s.AddSuppression(ctx, SuppressInput{
		Kind: suppress.KindIP, Value: "198.51.100.71", Reason: "disabled", CreatedBy: "test", ExpiresAt: &future,
	})
	if err != nil {
		t.Fatalf("add rule: %v", err)
	}
	if _, err := s.DB.Exec(`UPDATE suppressions SET enabled = 0 WHERE id = ?`, rule.ID); err != nil {
		t.Fatal(err)
	}
	f2 := assetFact(src, storage.AssetKindIP, "198.51.100.71")
	res2, err := s.IngestFact(ctx, &f2)
	if err != nil {
		t.Fatalf("ingest with disabled rule: %v", err)
	}
	if res2.Suppressed {
		t.Fatal("a disabled rule must NOT suppress")
	}
}

func TestSuppressionFailClosedOnBrokenRule(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	src := ensureSource(t, s, "broken-src")

	// A rule the matcher cannot compile (bad CIDR) lands in the table
	// only via a bypassed write path — simulate with direct SQL.
	if _, err := s.DB.Exec(`INSERT INTO suppressions (kind, value, reason, created_by, created_at, enabled)
                VALUES ('cidr', '999.999.0.0/16', 'broken', 'test', ?, 1)`, FormatTS(time.Now())); err != nil {
		t.Fatalf("seed broken rule: %v", err)
	}
	f := assetFact(src, storage.AssetKindIP, "198.51.100.90")
	if _, err := s.IngestFact(ctx, &f); err == nil {
		t.Fatal("a rule set that cannot compile must FAIL CLOSED (ingestion stops, never bypasses)")
	}
}

func TestRetractSuppressedRemovesIndexedRecords(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	src := ensureSource(t, s, "retract-src")

	// Index two services on one asset + a second, unrelated asset.
	facts := []Fact{
		serviceFact(src, "198.51.100.100", 22, "ssh", "OpenSSH"),
		serviceFact(src, "198.51.100.100", 443, "https", "nginx"),
		assetFact(src, storage.AssetKindIP, "198.51.100.200"),
	}
	if _, err := s.IngestFacts(ctx, facts); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	var before int
	_ = s.DB.QueryRow(`SELECT count(*) FROM assets`).Scan(&before)

	rule, _, err := s.AddSuppression(ctx, SuppressInput{
		Kind: suppress.KindIP, Value: "198.51.100.100", Reason: "retract", CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	st, err := s.RetractSuppressed(ctx, rule)
	if err != nil {
		t.Fatalf("retract: %v", err)
	}
	if st.AssetsRetracted != 1 || st.ServicesRemoved != 2 || st.AuditRows != 1 {
		t.Fatalf("retract stats = %+v", st)
	}
	var after int
	_ = s.DB.QueryRow(`SELECT count(*) FROM assets`).Scan(&after)
	if after != before-1 {
		t.Errorf("assets = %d, want %d (retracted asset removed)", after, before-1)
	}
	// The audit row survives the deletion it records.
	var audits int
	if err := s.DB.QueryRow(`SELECT count(*) FROM suppression_applications WHERE action='retroactive-retract'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Errorf("retroactive-retract audit rows = %d, want 1 (audit survives the delete)", audits)
	}

	// Re-ingesting the suppressed asset is now refused by the gate.
	f := assetFact(src, storage.AssetKindIP, "198.51.100.100")
	res, err := s.IngestFact(ctx, &f)
	if err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	if !res.Suppressed {
		t.Fatal("the rule must now gate new ingestion of the retracted asset")
	}
}

func TestRetractBySourceRemovesSourceData(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	srcA := ensureSource(t, s, "src-a")
	srcB := ensureSource(t, s, "src-b")

	// Asset A: seen by both sources; asset B: only src-a.
	fa1 := assetFact(srcA, storage.AssetKindIP, "198.51.100.101")
	fa2 := assetFact(srcB, storage.AssetKindIP, "198.51.100.101")
	fa2.Observation.SourceCursor = "cursor-b-101"
	fa2.Observation.PayloadHash = "hash-b-101"
	fb := assetFact(srcA, storage.AssetKindIP, "198.51.100.102")
	if _, err := s.IngestFacts(ctx, []Fact{fa1, fa2, fb}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	rule, _, err := s.AddSuppression(ctx, SuppressInput{
		Kind: suppress.KindSource, Value: "src-a", Reason: "bad source", CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	st, err := s.RetractSuppressed(ctx, rule)
	if err != nil {
		t.Fatalf("retract: %v", err)
	}
	if st.ObservationsRemoved != 2 || st.AssetsRetracted != 1 {
		t.Fatalf("source retraction stats = %+v (want 2 obs, 1 asset)", st)
	}

	// Asset A survives (still observed by src-b); asset B is gone.
	var n int
	_ = s.DB.QueryRow(`SELECT count(*) FROM assets WHERE value='198.51.100.102'`).Scan(&n)
	if n != 0 {
		t.Errorf("asset B must be retracted")
	}
	_ = s.DB.QueryRow(`SELECT count(*) FROM assets WHERE value='198.51.100.101'`).Scan(&n)
	if n != 1 {
		t.Errorf("asset A must survive (other source still observes it)")
	}
	var srcs string
	_ = s.DB.QueryRow(`SELECT sources FROM assets WHERE value='198.51.100.101'`).Scan(&srcs)
	if containsStr(srcs, "src-a") {
		t.Errorf("src-a name must be stripped from the surviving asset's sources: %s", srcs)
	}
}

// ---------------------------------------------------------------------------
// Purge + stats.
// ---------------------------------------------------------------------------

func TestPurgeFixturesScoped(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	fixtureSrc := ensureSource(t, s, "fixture")
	realSrc := ensureSource(t, s, "real-src")

	// One fixture asset, one real asset (provenance=real).
	fFix := assetFact(fixtureSrc, storage.AssetKindIP, "203.0.113.50")
	fFix.Observation.Provenance = storage.ProvenanceFixture
	fReal := assetFact(realSrc, storage.AssetKindIP, "198.51.100.50")
	fReal.Observation.Provenance = "real"
	if _, err := s.IngestFacts(ctx, []Fact{fFix, fReal}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	st, err := s.PurgeFixtures(ctx)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if st.ObservationsRemoved != 1 || st.AssetsRemoved != 1 {
		t.Fatalf("purge stats = %+v", st)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT count(*) FROM assets WHERE value='198.51.100.50'`).Scan(&n)
	if n != 1 {
		t.Fatal("purge must NEVER touch non-fixture rows")
	}
}

func TestStatsCounts(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	src := ensureSource(t, s, "count-src")
	if _, err := s.IngestFacts(ctx, []Fact{
		assetFact(src, storage.AssetKindIP, "198.51.100.60"),
		serviceFact(src, "198.51.100.61", 80, "http", "caddy"),
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	st, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Assets != 2 || st.Services != 1 || st.Observations != 2 || st.FixtureObs != 2 {
		t.Fatalf("stats = %+v", st)
	}
	if st.LastObserved == nil {
		t.Fatal("last_observed must be set")
	}
}

// TestOpenPermissions (SQ-04) — fresh file 0600, dir 0700, loose
// existing file gets a PermNote, not a silent widening.
func TestOpenPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SQ-04 asserts POSIX permission bits; on Windows Stat reports fixed 0666/0777 masks and chmod is a best-effort no-op by design (open.go isNoChmodOS) — the contract is enforced and verified on POSIX")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "gseer.db")
	res, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = res.DB.Close()
	if res.Mode != OpenCreated {
		t.Fatalf("mode = %s, want created", res.Mode)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("SQ-04 VIOLATION: fresh file mode = %04o, want 0600", uint32(st.Mode().Perm()))
	}
	dst, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dst.Mode().Perm() != 0o700 {
		t.Errorf("SQ-04 VIOLATION: dir mode = %04o, want 0700", uint32(dst.Mode().Perm()))
	}

	// Existing loose file: opened as-is + PermNote (never silent).
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	res2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = res2.DB.Close()
	if res2.PermNote == "" {
		t.Error("a loose existing file must surface a permission note (SQ-04 policy)")
	}
	// The policy does NOT silently rewrite an operator-widened file.
	st2, _ := os.Stat(path)
	if st2.Mode().Perm() != 0o644 {
		t.Errorf("existing-file policy must keep the mode (got %04o)", uint32(st2.Mode().Perm()))
	}
}
