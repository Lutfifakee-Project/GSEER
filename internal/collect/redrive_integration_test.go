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
	"testing"

	"github.com/Lutfifakee-Project/GSEER/internal/storage/postgres"
)

// redrive_integration_test.go — the v0.1.0 dead-letter re-drive on PG:
// a run that dead-letters records, then the operator's re-drive that
// resolves (or honestly re-fails) them.

func TestRedriveResolvesRecoverableRecords(t *testing.T) {
	r, pool := colSetup(t)
	srcID := colSource(t, r, "redrive-dns", "dns", map[string]any{"seed_mode": "config", "seeds": []string{"localhost"}}, true)
	_ = srcID

	// One run: a healthy record + one invalid record (dead-letter
	// fodder, shape 1).
	r.collectors = map[string]Collector{"dns": &fakeCollector{kind: "dns", batches: []*Batch{{
		Raws: []RawFact{
			{AssetKind: "ip", Value: "203.0.113.201", Confidence: "high"},
			{AssetKind: "ip", Value: "not-an-ip", Confidence: "high"},
		},
		Done: true,
	}}}}
	if _, err := r.Run(context.Background(), "redrive-dns"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := colCount(t, pool, `SELECT count(*) FROM ingestion_dead_letter WHERE status='pending'`); got != 1 {
		t.Fatalf("pending dead letters = %d, want 1", got)
	}

	stats, err := Redrive(context.Background(), r.Store, RedriveOptions{SourceID: srcID})
	if err != nil {
		t.Fatalf("redrive: %v", err)
	}
	// The record is STILL invalid: the rebuild goes through the SAME
	// normalization (BuildFacts), so it cannot reach ingestion — the
	// honest outcome is still_failing, not a fake resolution.
	if stats.Considered != 1 || stats.Redriven != 0 || stats.Resolved != 0 || stats.StillFailing != 1 {
		t.Fatalf("stats = %+v, want considered=1 redriven=0 still_failing=1", stats)
	}
	// The row stays pending with a bumped attempts counter.
	if got := colCount(t, pool, `SELECT count(*) FROM ingestion_dead_letter WHERE status='pending' AND attempts=2`); got != 1 {
		t.Errorf("failed re-drive must keep the row pending with attempts=2, got %d", got)
	}

	// Discard: the operator's judgment closes the queue.
	stats, err = Redrive(context.Background(), r.Store, RedriveOptions{SourceID: srcID, Discard: true})
	if err != nil {
		t.Fatalf("discard: %v", err)
	}
	if stats.Discarded != 1 {
		t.Fatalf("discarded = %d, want 1", stats.Discarded)
	}
	if got := colCount(t, pool, `SELECT count(*) FROM ingestion_dead_letter WHERE status='discarded'`); got != 1 {
		t.Errorf("discarded rows = %d, want 1", got)
	}
}

func TestRedriveRecoversTransientFactFailures(t *testing.T) {
	r, pool := colSetup(t)
	srcID := colSource(t, r, "transient-dns", "dns", map[string]any{"seed_mode": "config", "seeds": []string{"localhost"}}, true)

	// A fact that fails ingestion at first (FK ordering failure is
	// synthetic here — we force it by removing the suppression-free
	// precondition: ingest a fact whose observation lacks the required
	// ObservedAt is a usage error; instead simulate the transient case
	// by directly writing a dead letter with a VALID fact payload and
	// then re-driving it).
	// (The transient-failure path is covered by the runner's
	// retry-then-recover test; here we pin the re-drive resolution.)
	factJSON := `{"asset":{"kind":"ip","value":"203.0.113.202","ip":"203.0.113.202","sources":["redrive-eval"]},
	              "observation":{"source_cursor":"orig-1","payload_hash":"orig-hash","observed_at":"2026-01-15T00:00:00Z","provenance":"collection"}}`
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO ingestion_dead_letter (source_id, payload, reason) VALUES ($1, $2::jsonb, 'ingest failed: synthetic')`,
		srcID, `{"raw":{"fact":`+factJSON+`}}`); err != nil {
		t.Fatalf("seed dead letter: %v", err)
	}

	stats, err := Redrive(context.Background(), r.Store, RedriveOptions{SourceID: srcID})
	if err != nil {
		t.Fatalf("redrive: %v", err)
	}
	if stats.Resolved != 1 {
		t.Fatalf("stats = %+v, want resolved=1", stats)
	}
	if got := colCount(t, pool, `SELECT count(*) FROM ingestion_dead_letter WHERE status='resolved'`); got != 1 {
		t.Errorf("resolved rows = %d, want 1", got)
	}
	// The re-driven fact actually landed.
	if got := colCount(t, pool, `SELECT count(*) FROM assets WHERE value='203.0.113.202'`); got != 1 {
		t.Errorf("re-driven asset rows = %d, want 1", got)
	}
	// The delivery identity is deterministic: a second pass is a no-op.
	stats, err = Redrive(context.Background(), r.Store, RedriveOptions{SourceID: srcID})
	if err != nil {
		t.Fatalf("second redrive: %v", err)
	}
	if stats.Considered != 0 {
		t.Errorf("second pass considered = %d, want 0 (resolved rows are not pending)", stats.Considered)
	}
}

func TestRedriveSuppressionInterplay(t *testing.T) {
	r, pool := colSetup(t)
	srcID := colSource(t, r, "suppressed-dns", "dns", map[string]any{"seed_mode": "config", "seeds": []string{"localhost"}}, true)

	// Seed a dead letter whose fact is suppressed by policy.
	factJSON := `{"asset":{"kind":"ip","value":"10.9.9.9","ip":"10.9.9.9","sources":["redrive-eval"]},
	              "observation":{"source_cursor":"orig-2","payload_hash":"orig-hash-2","observed_at":"2026-01-15T00:00:00Z","provenance":"collection"}}`
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO ingestion_dead_letter (source_id, payload, reason) VALUES ($1, $2::jsonb, 'ingest failed: synthetic')`,
		srcID, `{"raw":{"fact":`+factJSON+`}}`); err != nil {
		t.Fatal(err)
	}
	// Suppress 10.9.9.9 BEFORE the re-drive.
	if _, _, err := r.Store.AddSuppression(context.Background(), postgres.SuppressInput{Kind: "ip", Value: "10.9.9.9", Reason: "redrive test", CreatedBy: "t"}); err != nil {
		t.Fatalf("add suppression: %v", err)
	}

	stats, err := Redrive(context.Background(), r.Store, RedriveOptions{SourceID: srcID})
	if err != nil {
		t.Fatalf("redrive: %v", err)
	}
	if stats.Suppressed != 1 || stats.Resolved != 0 {
		t.Fatalf("stats = %+v, want suppressed=1 resolved=0 (the gate outranks the re-drive)", stats)
	}
	if got := colCount(t, pool, `SELECT count(*) FROM assets WHERE value='10.9.9.9'`); got != 0 {
		t.Errorf("suppressed asset was re-driven into the index")
	}
	if got := colCount(t, pool, `SELECT count(*) FROM ingestion_dead_letter WHERE status='pending' AND reason LIKE '%suppressed%'`); got != 1 {
		t.Errorf("suppressed dead letter not honestly labeled: %d", got)
	}
}
