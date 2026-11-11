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

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/Lutfifakee-Project/gseer/internal/storage"
)

// redrive.go — v0.1.0 dead-letter re-drive (roadmap: "dead-letter
// re-drive").  A dead letter is a PRESERVED record, never a dropped
// one; the re-drive is the operator's explicit second chance:
//
//   - shape 1 (invalid raw records): the stored RawFact fields rebuild
//     a RawFact and re-run BuildFacts (the same normalization as a
//     live run — fixes to data-quality rules apply on re-drive);
//   - shape 2 (failed facts): the stored Fact JSON rebuilds directly;
//
// both are re-stamped with a per-dead-letter delivery cursor
// ("redrive:<id>") so the L1 identity is deterministic: re-running a
// re-drive is idempotent (already-ingested rows dedupe).
//
// The suppression gate applies at ingestion exactly as in a live run —
// a rule added after the record dead-lettered still blocks it, and the
// dead letter then reports the suppression as its failure reason.
//
// Nothing here runs uninvoked: `gseer collect redrive` is an operator
// command.

// RedriveStats reports one re-drive pass.
type RedriveStats struct {
	Considered   int `json:"considered"`
	Redriven     int `json:"redriven"`      // ingestion attempted
	Resolved     int `json:"resolved"`      // applied successfully
	StillFailing int `json:"still_failing"` // attempted, failed again (kept pending, attempts bumped)
	Suppressed   int `json:"suppressed"`    // refused by the suppression gate (kept pending)
	Discarded    int `json:"discarded"`     // only with Discard
}

// RedriveOptions bounds one re-drive pass.
type RedriveOptions struct {
	// SourceID restricts to one source; 0 = all sources.
	SourceID int64
	// Limit bounds the pass (0 = 500).
	Limit int
	// Discard marks the pending rows discarded instead of re-driving.
	Discard bool
}

// Redrive runs one pass over pending dead letters.
func Redrive(ctx context.Context, store Backend, opts RedriveOptions) (*RedriveStats, error) {
	stats := &RedriveStats{}

	if opts.Discard {
		n, err := store.DiscardDeadLetters(ctx, opts.SourceID, opts.Limit)
		if err != nil {
			return stats, err
		}
		stats.Considered = int(n)
		stats.Discarded = int(n)
		return stats, nil
	}

	records, err := store.PendingDeadLetters(ctx, opts.SourceID, opts.Limit)
	if err != nil {
		return stats, err
	}
	stats.Considered = len(records)

	// Source names resolve once for reasons/audit lines.
	for _, rec := range records {
		fact, ferr := rebuildFact(rec)
		if ferr != nil {
			stats.StillFailing++
			if err := store.FailDeadLetter(ctx, rec.ID, "unrebuildable payload: "+ferr.Error()); err != nil {
				return stats, err
			}
			continue
		}
		stats.Redriven++
		res, ierr := store.IngestFact(ctx, fact)
		switch {
		case ierr != nil:
			stats.StillFailing++
			if err := store.FailDeadLetter(ctx, rec.ID, truncateReason(ierr.Error())); err != nil {
				return stats, err
			}
		case res.Suppressed:
			stats.Suppressed++
			if err := store.FailDeadLetter(ctx, rec.ID, "suppressed by policy (E_DB_SUPPRESSED)"); err != nil {
				return stats, err
			}
		default:
			stats.Resolved++
			if err := store.ResolveDeadLetter(ctx, rec.ID, " | redriven "+time.Now().UTC().Format(time.RFC3339)); err != nil {
				return stats, err
			}
		}
	}
	return stats, nil
}

// rebuildFact reconstructs an ingestion-ready Fact from either stored
// payload shape (see ingestWithPoisonIsolation and the invalid-record
// path in runner.go — the writers of the two shapes).
func rebuildFact(rec storage.DeadLetter) (*storage.Fact, error) {
	// Shape 2: {"raw": {"fact": <Fact>}} — the poison-isolation writer.
	if rawRaw, ok := rec.Payload["raw"]; ok {
		rawMap, ok := rawRaw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("raw is not an object")
		}
		factJSON, ok := rawMap["fact"]
		if !ok {
			return nil, fmt.Errorf("raw carries no fact")
		}
		b, err := json.Marshal(factJSON)
		if err != nil {
			return nil, err
		}
		var f storage.Fact
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, err
		}
		restampFact(&f, rec)
		return &f, nil
	}

	// Shape 1: the invalid-record writer (asset_kind/value/observed_at/
	// confidence/extra) — rebuild the RawFact and re-run the SAME
	// normalization the live pipeline uses.
	kind, _ := rec.Payload["asset_kind"].(string)
	value, _ := rec.Payload["value"].(string)
	if kind == "" || value == "" {
		return nil, fmt.Errorf("payload lacks asset_kind/value")
	}
	raw := RawFact{AssetKind: kind, Value: value}
	if obs, ok := rec.Payload["observed_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, obs); err == nil {
			raw.ObservedAt = t
		}
	}
	if conf, ok := rec.Payload["confidence"].(string); ok {
		raw.Confidence = conf
	}
	if extra, ok := rec.Payload["extra"].(map[string]any); ok {
		raw.Extra = extra
	}
	facts, bad := BuildFacts("", []RawFact{raw})
	if len(bad) > 0 || len(facts) == 0 {
		return nil, fmt.Errorf("record still invalid: %s", firstBadReason(bad))
	}
	f := facts[0]
	restampFact(&f, rec)
	return &f, nil
}

// restampFact gives the rebuilt fact a deterministic delivery identity:
// L1 = (source_id, "redrive:<dead-letter id>") — idempotent re-runs.
func restampFact(f *storage.Fact, rec storage.DeadLetter) {
	f.Observation.SourceID = rec.SourceID
	f.Observation.SourceCursor = fmt.Sprintf("redrive:%d", rec.ID)
	f.Observation.ID = redriveObservationID(rec.SourceID, rec.ID)
	if f.Observation.ObservedAt.IsZero() {
		f.Observation.ObservedAt = rec.FirstSeen
	}
	if f.Observation.Provenance == "" {
		f.Observation.Provenance = "collection"
	}
}

func redriveObservationID(sourceID, deadLetterID int64) int64 {
	// Same fnv bookkeeping scheme as the runner/fixture id assignments,
	// namespaced so the id spaces never collide.
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "redrive:%d:%d", sourceID, deadLetterID)
	// Mask to 63 bits: observations.id is a positive BIGINT and the
	// conversion must never overflow (same convention as runner.observationID).
	return int64(h.Sum64() & 0x7fff_ffff_ffff_ffff)
}

func firstBadReason(bad []BadRecord) string {
	if len(bad) == 0 {
		return "unknown"
	}
	return bad[0].Reason
}

func truncateReason(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
