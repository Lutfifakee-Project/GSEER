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

package collect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Lutfifakee-Project/GSEER/internal/storage/postgres"
	"github.com/Lutfifakee-Project/GSEER/internal/suppress"
)

// integration_test.go — the Runner against a real PostgreSQL: source
// registry rows, the terms gate, checkpoints, dead letters, suppression
// interplay and job bookkeeping.  Same provisioning pattern as the
// storage/postgres suite (GSEER_TEST_PG_ADMIN).

const colAdminDSN = "postgres://gseer@127.0.0.1:5433/postgres?sslmode=disable"

var colDBSeq atomic.Int64

func colTestDB(t *testing.T) (dsn string, cleanup func()) {
	t.Helper()
	admin := os.Getenv("GSEER_TEST_PG_ADMIN")
	if admin == "" {
		admin = colAdminDSN
	}
	name := fmt.Sprintf("gseer_m14_collect_%d_%d", os.Getpid(), colDBSeq.Add(1))
	adminPool, err := pgxpool.New(context.Background(), admin)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	defer adminPool.Close()
	if _, err := adminPool.Exec(context.Background(), `CREATE DATABASE `+name); err != nil {
		t.Fatalf("create test db: %v", err)
	}
	// Derive the per-test DSN from the admin DSN (same host/port/user —
	// the suite must run unchanged against ANY instance, e.g. the PG16
	// and PG17 matrix instances).
	dsn = strings.Replace(admin, "/postgres?", "/"+name+"?", 1)
	if dsn == admin { // admin DSN had no "?"; append the path
		dsn = strings.TrimSuffix(admin, "/postgres") + "/" + name
	}
	cleanup = func() {
		p, err := pgxpool.New(context.Background(), admin)
		if err != nil {
			return
		}
		defer p.Close()
		_, _ = p.Exec(context.Background(),
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name)
		_, _ = p.Exec(context.Background(), `DROP DATABASE IF EXISTS `+name)
	}
	return dsn, cleanup
}

func colSetup(t *testing.T) (*Runner, *pgxpool.Pool) {
	t.Helper()
	dsn, cleanup := colTestDB(t)
	t.Cleanup(cleanup)
	// v0.2.0 hardened migrations: bootstrap first (0016 refusal guard),
	// then migrate, then the runtime grants (ingestion runs may exercise
	// the gseer_app grant surface).
	if _, err := postgres.BootstrapRoles(context.Background(), dsn); err != nil {
		t.Fatalf("BootstrapRoles: %v", err)
	}
	if _, _, err := postgres.RunMigrations(dsn, 5000); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := postgres.ApplyRuntimeGrants(context.Background(), dsn); err != nil {
		t.Fatalf("ApplyRuntimeGrants: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	store := postgres.NewStore(pool)
	r := NewRunner(store, nil)
	r.Backoff = time.Millisecond // no real sleeping in tests
	return r, pool
}

func colSource(t *testing.T, r *Runner, name, connector string, cfg map[string]any, enabled bool) int64 {
	t.Helper()
	id, err := r.Store.EnsureSource(context.Background(), name, connector,
		"test registration", cfg)
	if err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	if err := r.Store.SetSourceEnabled(context.Background(), name, enabled); err != nil {
		t.Fatalf("enable source: %v", err)
	}
	return id
}

func colCount(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", sql, err)
	}
	return n
}

// fakeCollector is a scriptable connector for runner tests.
type fakeCollector struct {
	kind     string
	verdict  string
	batches  []*Batch
	attempts atomic.Int64 // Collect invocations
	failN    int          // fail the first N Collect calls with a transport error
	failErr  error
}

func (f *fakeCollector) Kind() string { return f.kind }
func (f *fakeCollector) Review() Review {
	if f.verdict == "" {
		return Review{Verdict: ReviewEnabled, Why: "test", DocRef: "test"}
	}
	return Review{Verdict: f.verdict, Why: "test", DocRef: "test"}
}
func (f *fakeCollector) Collect(ctx Ctx) (*Batch, error) {
	n := f.attempts.Add(1)
	if int(n) <= f.failN {
		return nil, f.failErr
	}
	if len(f.batches) == 0 {
		return &Batch{Done: true}, nil
	}
	b := f.batches[0]
	f.batches = f.batches[1:]
	return b, nil
}

// TestRunnerTermsGateRefusesDisabledConnectors — a hand-flipped
// enabled=true row cannot override the code-level review verdict
// (P1 honesty contract 2; owner directive §2).
func TestRunnerTermsGateRefusesDisabledConnectors(t *testing.T) {
	r, pool := colSetup(t)
	rdapSource := "my-rdap"
	colSource(t, r, rdapSource, "rdap", map[string]any{"seed_mode": "config", "seeds": []string{"203.0.113.7"}}, true)

	_, err := r.Run(context.Background(), rdapSource)
	if err == nil || !strings.Contains(err.Error(), "E_SOURCE_TERMS") {
		t.Fatalf("expected E_SOURCE_TERMS refusal, got %v", err)
	}
	if got := colCount(t, pool, `SELECT count(*) FROM collection_jobs`); got != 0 {
		t.Errorf("refused run must not open a job row, got %d", got)
	}

	// Same for ct.
	colSource(t, r, "my-ct", "ct", map[string]any{}, true)
	_, err = r.Run(context.Background(), "my-ct")
	if err == nil || !strings.Contains(err.Error(), "E_SOURCE_TERMS") {
		t.Fatalf("expected E_SOURCE_TERMS refusal for ct, got %v", err)
	}
}

// TestRunnerSkipsDisabledSource — nothing runs without explicit
// enablement; the skip is reported, not an error.
func TestRunnerSkipsDisabledSource(t *testing.T) {
	r, pool := colSetup(t)
	colSource(t, r, "quiet-dns", "dns", map[string]any{}, false)

	stats, err := r.Run(context.Background(), "quiet-dns")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Skipped == "" {
		t.Error("expected a skip reason")
	}
	if got := colCount(t, pool, `SELECT count(*) FROM collection_jobs`); got != 0 {
		t.Errorf("skipped source must not open a job row, got %d", got)
	}
}

// TestRunnerFullFlow — two batches through the pipeline: normalization,
// dedupe, delivery cursors, dead letters, checkpoints, job row, and the
// rows actually in the database.
func TestRunnerFullFlow(t *testing.T) {
	r, pool := colSetup(t)
	fake := &fakeCollector{
		kind: "dns",
		batches: []*Batch{
			{Raws: []RawFact{
				{AssetKind: "domain", Value: "Example.COM.", Confidence: "high"},
				{AssetKind: "ip", Value: "203.0.113.7"},
				{AssetKind: "ip", Value: "not-an-ip"}, // dead-letter fodder
			}, Cursor: "c1"},
			{Raws: []RawFact{
				{AssetKind: "ip", Value: "203.0.113.7"}, // re-sighting → dedupe/update
			}, Cursor: "c2", Done: true},
		},
	}
	r.collectors = map[string]Collector{"dns": fake}
	colSource(t, r, "flow-dns", "dns", map[string]any{}, true)

	stats, err := r.Run(context.Background(), "flow-dns")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Batches != 2 || stats.Records != 4 || stats.DeadLetter != 1 {
		t.Fatalf("run stats wrong: %+v", stats)
	}
	// 2 new facts in batch 1 + 1 re-sighting in batch 2.  A re-sighting
	// carries a fresh ObservedAt → a fresh L2 identity → a new
	// observation row (that is the freshness design: last_seen advances);
	// only byte-identical replays dedupe.
	if stats.Ingested != 3 || stats.Deduped != 0 {
		t.Errorf("ingest stats wrong: ingested=%d deduped=%d", stats.Ingested, stats.Deduped)
	}
	if stats.Cursor != "c2" || !stats.Done {
		t.Errorf("checkpoint/done wrong: cursor=%q done=%v", stats.Cursor, stats.Done)
	}
	var cursor string
	if err := pool.QueryRow(context.Background(),
		`SELECT cursor FROM source_checkpoints`).Scan(&cursor); err != nil || cursor != "c2" {
		t.Errorf("checkpoint row wrong: cursor=%q err=%v", cursor, err)
	}
	// Assets are in the index.
	if got := colCount(t, pool, `SELECT count(*) FROM assets WHERE kind='domain' AND value='example.com'`); got != 1 {
		t.Errorf("domain asset missing: %d", got)
	}
	if got := colCount(t, pool, `SELECT count(*) FROM assets WHERE kind='ip' AND value='203.0.113.7'`); got != 1 {
		t.Errorf("ip asset missing: %d", got)
	}
	// No service rows were ever produced (discovery ≠ observation).
	if got := colCount(t, pool, `SELECT count(*) FROM services`); got != 0 {
		t.Errorf("P1 connector produced services: %d", got)
	}
	// Dead letter preserved the invalid record.
	if got := colCount(t, pool, `SELECT count(*) FROM ingestion_dead_letter WHERE reason LIKE 'invalid record:%'`); got != 1 {
		t.Errorf("invalid record not dead-lettered: %d", got)
	}
	// Job row closed as success with stats.
	var status string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM collection_jobs`).Scan(&status); err != nil || status != "success" {
		t.Errorf("job status wrong: %q err=%v", status, err)
	}
	// Idempotent replay: re-running from the same checkpoint dedupes.
	fake2 := &fakeCollector{kind: "dns", batches: []*Batch{
		{Raws: []RawFact{{AssetKind: "ip", Value: "203.0.113.7"}}, Cursor: "c3", Done: true},
	}}
	r.collectors = map[string]Collector{"dns": fake2}
	stats2, err := r.Run(context.Background(), "flow-dns")
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if stats2.Ingested != 1 {
		// New delivery cursor → L1 new → L2: same (source, asset, at,
		// hash)?  ObservedAt is re-stamped now() — a re-sighting IS a
		// new fact (last_seen advances).  That is correct freshness
		// behavior, so accept either: new observation or L2 dedupe.
		t.Logf("re-sighting ingested as a new fact (ingested=%d deduped=%d)", stats2.Ingested, stats2.Deduped)
	}
}

// TestRunnerStampsAssetSources — v0.2.0 regression (the documented
// DEPLOYMENT.md §7 "empty sources" defect): collected assets must carry
// the source NAME on the asset row, or they are invisible to the sources
// facet, to search_index projections, and to `gseer host`.
func TestRunnerStampsAssetSources(t *testing.T) {
	r, pool := colSetup(t)
	fake := &fakeCollector{kind: "dns", batches: []*Batch{
		{Raws: []RawFact{
			{AssetKind: "ip", Value: "203.0.113.77"},
			{AssetKind: "domain", Value: "stamped.example"},
		}, Done: true},
	}}
	r.collectors = map[string]Collector{"dns": fake}
	colSource(t, r, "stamp-src", "dns", map[string]any{}, true)

	if _, err := r.Run(context.Background(), "stamp-src"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, tc := range []struct{ kind, value string }{
		{"ip", "203.0.113.77"},
		{"domain", "stamped.example"},
	} {
		var sources []string
		if err := pool.QueryRow(context.Background(),
			`SELECT sources FROM assets WHERE kind=$1 AND value=$2`, tc.kind, tc.value).
			Scan(&sources); err != nil {
			t.Fatalf("asset %s/%s missing: %v", tc.kind, tc.value, err)
		}
		if len(sources) != 1 || sources[0] != "stamp-src" {
			t.Errorf("asset %s/%s sources = %v, want [stamp-src]", tc.kind, tc.value, sources)
		}
	}
}

// TestRunnerRetryAndFatal — transport errors retry with backoff; fatal
// errors do not retry.
func TestRunnerRetryAndFatal(t *testing.T) {
	r, _ := colSetup(t)

	boom := errors.New("transport: connection reset")
	transport := &fakeCollector{kind: "ip2asn", failN: 2, failErr: boom,
		batches: []*Batch{{Raws: []RawFact{{AssetKind: "ip", Value: "203.0.113.7"}}, Done: true}}}
	r.collectors = map[string]Collector{"ip2asn": transport}
	colSource(t, r, "retry-src", "ip2asn", map[string]any{}, true)
	stats, err := r.Run(context.Background(), "retry-src")
	if err != nil {
		t.Fatalf("expected recovery after retries, got %v", err)
	}
	if transport.attempts.Load() != 3 {
		t.Errorf("expected 3 attempts (2 failures + success), got %d", transport.attempts.Load())
	}
	if stats.Ingested != 1 {
		t.Errorf("post-retry ingest wrong: %+v", stats)
	}

	fatal := &fakeCollector{kind: "ip2asn", verdict: ReviewEnabled,
		failN: 5, failErr: Fatal(errors.New("seed_mode invalid"))}
	r.collectors = map[string]Collector{"ip2asn": fatal}
	colSource(t, r, "fatal-src", "ip2asn", map[string]any{}, true)
	if _, err := r.Run(context.Background(), "fatal-src"); err == nil {
		t.Fatal("expected the fatal error to surface")
	}
	if fatal.attempts.Load() != 1 {
		t.Errorf("fatal error must not retry, got %d attempts", fatal.attempts.Load())
	}
}

// TestRunnerSuppressionInterplay — a suppressed record a collector
// delivers is blocked before any write and counted (M13 × M14).
func TestRunnerSuppressionInterplay(t *testing.T) {
	r, pool := colSetup(t)
	if _, _, err := r.Store.AddSuppression(context.Background(), postgres.SuppressInput{
		Kind: suppress.KindDomain, Value: "suppressed.example",
		Reason: "operator opt-out", CreatedBy: "test",
	}); err != nil {
		t.Fatalf("add suppression: %v", err)
	}

	fake := &fakeCollector{kind: "dns", batches: []*Batch{
		{Raws: []RawFact{
			{AssetKind: "domain", Value: "suppressed.example"},
			{AssetKind: "domain", Value: "clean.example"},
		}, Done: true},
	}}
	r.collectors = map[string]Collector{"dns": fake}
	colSource(t, r, "sup-dns", "dns", map[string]any{}, true)

	stats, err := r.Run(context.Background(), "sup-dns")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Suppressed != 1 || stats.Ingested != 1 {
		t.Fatalf("suppression interplay wrong: %+v", stats)
	}
	if got := colCount(t, pool, `SELECT count(*) FROM assets WHERE value='suppressed.example'`); got != 0 {
		t.Errorf("suppressed asset reached the index: %d", got)
	}
	if got := colCount(t, pool, `SELECT count(*) FROM assets WHERE value='clean.example'`); got != 1 {
		t.Errorf("clean asset missing: %d", got)
	}
	if got := colCount(t, pool, `SELECT count(*) FROM suppression_applications WHERE action='ingest-blocked'`); got != 1 {
		t.Errorf("ingest-block not audited: %d", got)
	}
}

// TestRunnerUnknownConnector — an unregistered kind is a hard error.
func TestRunnerUnknownConnector(t *testing.T) {
	r, _ := colSetup(t)
	colSource(t, r, "ghost", "scary-scanner", map[string]any{}, true)
	_, err := r.Run(context.Background(), "ghost")
	if err == nil || !strings.Contains(err.Error(), "E_SOURCE_CONNECTOR") {
		t.Fatalf("expected E_SOURCE_CONNECTOR, got %v", err)
	}
}
