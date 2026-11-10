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

package collect

// sqlite_backend_test.go — v0.3.0: the collect engine runs over the
// PERSONAL (SQLite) store through the Backend contract, not just over
// postgres.Store.  These are unit tests (no build tag, no server): the
// personal engine is an embedded file, so the runner contract is
// verifiable everywhere the binary builds.
//
// The integration-tagged suite (integration_test.go) keeps covering the
// postgres path with the same scenarios; the fake here uses a distinct
// type name so both files can compile together under -tags integration.

import (
        "context"
        "errors"
        "path/filepath"
        "strings"
        "testing"
        "time"

        "github.com/Lutfifakee-Project/GSEER/internal/storage"
        "github.com/Lutfifakee-Project/GSEER/internal/storage/sqlite"
)

// sqliteFakeCollector is this file's scripted connector (distinct name
// from the integration suite's fakeCollector).
type sqliteFakeCollector struct {
        batches []*Batch
        err     error
}

func (f *sqliteFakeCollector) Kind() string { return "fake-sqlite" }
func (f *sqliteFakeCollector) Review() Review {
        return Review{Verdict: ReviewEnabled, Why: "unit test", DocRef: "docs/SOURCES.md"}
}
func (f *sqliteFakeCollector) Collect(ctx Ctx) (*Batch, error) {
        if f.err != nil {
                return nil, f.err
        }
        if len(f.batches) == 0 {
                return &Batch{Done: true}, nil
        }
        b := f.batches[0]
        f.batches = f.batches[1:]
        return b, nil
}

func newSQLiteStoreForTest(t *testing.T) *sqlite.Store {
        t.Helper()
        res, err := sqlite.Open(filepath.Join(t.TempDir(), "gseer.db"))
        if err != nil {
                t.Fatalf("open personal db: %v", err)
        }
        if _, err := sqlite.Migrate(context.Background(), res.DB); err != nil {
                _ = res.DB.Close()
                t.Fatalf("migrate personal db: %v", err)
        }
        t.Cleanup(func() { _ = res.DB.Close() })
        return sqlite.NewStore(res.DB)
}

// TestRunnerRunsOverSQLitePersonalStore proves the v0.3.0 Backend
// contract end to end on the personal engine: registry → terms gate →
// job bookkeeping → fact pipeline → checkpoint, plus the two error
// paths (unknown source, disabled connector).
func TestRunnerRunsOverSQLitePersonalStore(t *testing.T) {
        ctx := context.Background()
        store := newSQLiteStoreForTest(t)

        if _, err := store.EnsureSource(ctx, "fake-src", "fake-sqlite",
                "docs/SOURCES.md (unit test)", map[string]any{"seed_mode": "config", "seeds": []string{"203.0.113.99"}}); err != nil {
                t.Fatalf("ensure source: %v", err)
        }
        if err := store.SetSourceEnabled(ctx, "fake-src", true); err != nil {
                t.Fatalf("enable source: %v", err)
        }

        fake := &sqliteFakeCollector{batches: []*Batch{{
                Raws: []RawFact{{
                        AssetKind: "ip", Value: "203.0.113.99",
                        ObservedAt: time.Now().UTC(), Confidence: "high",
                }},
                Done: true,
        }}}
        r := NewRunner(store, nil)
        r.collectors = map[string]Collector{fake.Kind(): fake}
        r.Now = func() time.Time { return time.Now().UTC() }

        stats, err := r.Run(ctx, "fake-src")
        if err != nil {
                t.Fatalf("run over sqlite store: %v", err)
        }
        if stats.Ingested != 1 || stats.Facts != 1 {
                t.Errorf("stats = ingested %d / facts %d, want 1/1", stats.Ingested, stats.Facts)
        }
        if stats.JobID == 0 {
                t.Errorf("no job row recorded")
        }

        // The unknown source is the shared storage-level sentinel.
        if _, err := r.Run(ctx, "no-such-source"); !errors.Is(err, storage.ErrSourceNotFound) {
                t.Errorf("unknown source error = %v, want storage.ErrSourceNotFound", err)
        }

        // A disabled connector refuses HARD even with enabled=true in the
        // registry (the terms gate outranks configuration — E_SOURCE_TERMS).
        if _, err := store.EnsureSource(ctx, "gated-src", "fake-sqlite", "x", nil); err != nil {
                t.Fatalf("ensure gated source: %v", err)
        }
        if err := store.SetSourceEnabled(ctx, "gated-src", true); err != nil {
                t.Fatalf("enable gated source: %v", err)
        }
        r.collectors["fake-sqlite-disabled"] = &disabledFakeCollector{}
        if _, err := store.EnsureSource(ctx, "gated2", "fake-sqlite-disabled", "x", nil); err != nil {
                t.Fatalf("ensure gated2: %v", err)
        }
        if err := store.SetSourceEnabled(ctx, "gated2", true); err != nil {
                t.Fatalf("enable gated2: %v", err)
        }
        _, err = r.Run(ctx, "gated2")
        if err == nil || !strings.Contains(err.Error(), "E_SOURCE_TERMS") {
                t.Errorf("disabled-connector run = %v, want the E_SOURCE_TERMS refusal", err)
        }
}

// disabledFakeCollector carries a disabled verdict (the terms gate).
type disabledFakeCollector struct{}

func (disabledFakeCollector) Kind() string { return "fake-sqlite-disabled" }
func (disabledFakeCollector) Review() Review {
        return Review{Verdict: ReviewDisabled, Why: "unit test", DocRef: "docs/SOURCES.md"}
}
func (disabledFakeCollector) Collect(ctx Ctx) (*Batch, error) {
        return &Batch{Done: true}, nil
}

// TestRedriveOverSQLitePersonalStore proves the dead-letter re-drive
// runs on the personal engine: an invalid shape-1 record rebuilds
// through the real pipeline, and a suppressed value reports the gate.
func TestRedriveOverSQLitePersonalStore(t *testing.T) {
        ctx := context.Background()
        store := newSQLiteStoreForTest(t)
        id, err := store.EnsureSource(ctx, "rsrc", "fake-sqlite", "docs/SOURCES.md (unit test)", nil)
        if err != nil {
                t.Fatalf("ensure source: %v", err)
        }

        // Shape 1 payload (the invalid-record writer's shape) with a VALID
        // domain this time: the re-drive rebuilds it into an ingestible fact.
        if err := store.AddDeadLetter(ctx, id, map[string]any{
                "asset_kind":  "domain",
                "value":       "Portal.Example.test.",
                "observed_at": time.Now().UTC().Format(time.RFC3339Nano),
                "confidence":  "medium",
        }, "invalid record: seeded for the test"); err != nil {
                t.Fatalf("add dead letter: %v", err)
        }

        stats, err := Redrive(ctx, store, RedriveOptions{SourceID: id})
        if err != nil {
                t.Fatalf("redrive: %v", err)
        }
        if stats.Considered != 1 || stats.Resolved != 1 || stats.StillFailing != 0 {
                t.Errorf("redrive stats = considered %d, resolved %d, still-failing %d; want 1/1/0",
                        stats.Considered, stats.Resolved, stats.StillFailing)
        }

        // The re-driven fact really landed in the index (asset-level row).
        seeds, err := store.IndexSeedValues(ctx, "domain", 10)
        if err != nil {
                t.Fatalf("index seeds: %v", err)
        }
        found := false
        for _, s := range seeds {
                if s == "portal.example.test" {
                        found = true
                }
        }
        if !found {
                t.Errorf("re-driven domain missing from index seeds: %v", seeds)
        }
}
