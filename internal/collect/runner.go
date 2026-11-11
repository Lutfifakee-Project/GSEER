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
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/storage"
)

// runner.go — the execution engine around connectors: source registry
// lookup, the terms gate, checkpoints, transport retries with backoff,
// the fact pipeline, poison-record dead-lettering and job bookkeeping.
//
// Everything a connector produces flows through Backend.IngestFacts —
// the same L1/L2-idempotent, suppression-gated path every other writer
// uses.  The runner never writes assets directly.

// Runner executes connector passes against the store.
type Runner struct {
	Store  Backend // postgres.Store (self-hosted) or sqlite.Store (personal)
	Client *Client // shared default client (per-source config may override)

	// Now is injectable for tests; zero value = time.Now.
	Now func() time.Time
	// MaxAttempts bounds transport retries per batch (0 = DefaultMaxAttempts).
	MaxAttempts int
	// Backoff is the base delay between retries, doubled per attempt
	// (0 = DefaultBackoff).
	Backoff time.Duration
	// MaxBatches bounds one run's batch loop (0 = DefaultMaxBatches) —
	// a stuck cursor must not spin forever.
	MaxBatches int

	// collectors overrides the package registry (tests inject fakes).
	collectors map[string]Collector
}

// Runner defaults: passive-polite transport behavior.
const (
	DefaultMaxAttempts = 3
	DefaultBackoff     = 2 * time.Second
	DefaultMaxBatches  = 100
)

// NewRunner builds a Runner over store.
func NewRunner(store Backend, client *Client) *Runner {
	return &Runner{Store: store, Client: client}
}

func (r *Runner) registry() map[string]Collector {
	if r.collectors != nil {
		return r.collectors
	}
	return Registry()
}

// RunStats reports one run; marshalled into the collection_jobs.stats
// JSONB and rendered by the CLI.
type RunStats struct {
	Source     string `json:"source"`
	Connector  string `json:"connector"`
	JobID      int64  `json:"job_id"`
	Records    int    `json:"records"`
	Facts      int    `json:"facts"`
	Ingested   int    `json:"ingested"`
	Updated    int    `json:"updated"`
	Deduped    int    `json:"deduped"`
	Suppressed int    `json:"suppressed"`
	DeadLetter int    `json:"dead_lettered"`
	Batches    int    `json:"batches"`
	Cursor     string `json:"cursor"`
	Done       bool   `json:"done"`
	// Skipped is set (with the reason) when nothing ran.
	Skipped string `json:"skipped,omitempty"`
}

// Run executes one passive pass for the named registered source.
//
// Gate order (fail-closed):
//  1. unknown connector kind            → E_SOURCE_CONNECTOR (hard error)
//  2. connector review verdict disabled → E_SOURCE_TERMS (hard error:
//     the terms gate outranks a hand-flipped enabled=true row)
//  3. source enabled=false              → clean skip (Skipped="disabled")
func (r *Runner) Run(ctx context.Context, sourceName string) (*RunStats, error) {
	src, err := r.Store.GetSource(ctx, sourceName)
	if err != nil {
		return nil, err
	}
	col, ok := r.registry()[src.Connector]
	if !ok {
		return nil, fmt.Errorf("E_SOURCE_CONNECTOR: no connector registered for kind %q (source %q)",
			src.Connector, src.Name)
	}

	// Terms gate (P1 honesty contract 2): the review verdict is part of
	// the code, not of operator configuration — a connector whose
	// provider terms are not clearly compatible refuses to run, period.
	rv := col.Review()
	if rv.Verdict != ReviewEnabled {
		return nil, fmt.Errorf("E_SOURCE_TERMS: connector %q is disabled by terms review (%s; see %s): %s",
			src.Connector, rv.Verdict, rv.DocRef, rv.Why)
	}
	if !src.Enabled {
		return &RunStats{Source: src.Name, Connector: src.Connector,
			Skipped: "source is disabled — enable it explicitly (gseer source add --enable or gseer source enable)"}, nil
	}

	jobID, err := r.Store.StartCollectionJob(ctx, src.ID, "fetch")
	if err != nil {
		return nil, err
	}
	stats := &RunStats{Source: src.Name, Connector: src.Connector, JobID: jobID}

	runErr := r.runPass(ctx, src, col, stats)
	status, errMsg := "success", ""
	if runErr != nil {
		status, errMsg = "failed", runErr.Error()
	}
	if fjErr := r.Store.FinishCollectionJob(ctx, jobID, status, r.statsJSON(stats), errMsg); fjErr != nil && runErr == nil {
		runErr = fjErr
	}
	return stats, runErr
}

// runPass is the batch loop: collect → pipeline → ingest → checkpoint.
func (r *Runner) runPass(ctx context.Context, src storage.DataSourceRow, col Collector, stats *RunStats) error {
	cfg, err := DecodeSourceConfig(src.ConfigJSON)
	if err != nil {
		return err
	}
	c := Ctx{
		Context:    ctx,
		Source:     SourceRef{ID: src.ID, Name: src.Name},
		Config:     cfg,
		Limit:      cfg.LimitPerRun,
		IndexSeeds: func(kind string, limit int) ([]string, error) { return r.Store.IndexSeedValues(ctx, kind, limit) },
		Now:        r.now,
	}
	if c.Limit <= 0 {
		c.Limit = DefaultRunLimit
	}
	c.Cursor, err = r.Store.GetCheckpoint(ctx, src.ID, "default")
	if err != nil {
		return err
	}
	c.Client = r.clientFor(cfg)

	maxBatches := r.MaxBatches
	if maxBatches <= 0 {
		maxBatches = DefaultMaxBatches
	}
	for i := 0; i < maxBatches; i++ {
		batch, err := r.collectRetry(ctx, col, c)
		if err != nil {
			return err
		}
		stats.Batches++
		stats.Records += len(batch.Raws)
		if err := r.pipeline(ctx, src, batch, stats); err != nil {
			return err
		}
		if batch.Cursor != "" {
			// The checkpoint advances only after the batch ingested
			// (at-least-once delivery; L1 identity makes replays no-ops).
			if err := r.Store.SetCheckpoint(ctx, src.ID, "default", batch.Cursor); err != nil {
				return err
			}
			stats.Cursor = batch.Cursor
			c.Cursor = batch.Cursor
		}
		if batch.Done {
			stats.Done = true
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return fmt.Errorf("run aborted after %d batches without a Done batch (cursor %q) — raise limit_per_run or fix the connector",
		maxBatches, c.Cursor)
}

// pipeline turns one batch's raw records into facts and ingests them,
// dead-lettering the records that could not be applied.
func (r *Runner) pipeline(ctx context.Context, src storage.DataSourceRow, batch *Batch, stats *RunStats) error {
	facts, bad := BuildFacts(src.Connector, batch.Raws)
	stats.Facts += len(facts)

	// Stamp the delivery identity: source row + per-record cursor
	// derived from the batch cursor, so a re-run from the same
	// checkpoint reproduces identical L1 identities (idempotent replay).
	// Observation.ID is caller-assigned (the column has no sequence
	// default): fnv1a-64 over the delivery identity, the fixture
	// loader's deterministic-id scheme adapted to collectors.  It is a
	// bookkeeping id only — L1 (source_id, source_cursor) is the
	// idempotency key.
	for i := range facts {
		if facts[i].Observation.SourceID == 0 {
			facts[i].Observation.SourceID = src.ID
		}
		facts[i].Observation.SourceCursor = deliveryCursor(batch.Cursor, i)
		facts[i].Observation.ID = observationID(src.ID, facts[i].Observation.SourceCursor)
		// Source provenance on the ASSET row too (v0.2.0 fix): the
		// fixture loader stamps Asset.Sources; collectors must do
		// the same or the collected assets land with an empty
		// sources array — invisible to the sources facet, to
		// search_index projections, and to `gseer host`.  A
		// connector-provided value (never set today) wins.
		if len(facts[i].Asset.Sources) == 0 {
			facts[i].Asset.Sources = []string{src.Name}
		}
	}

	if err := r.ingestWithPoisonIsolation(ctx, facts, stats); err != nil {
		return err
	}

	// Invalid records are preserved, never silently dropped.
	for _, b := range bad {
		stats.DeadLetter++
		if err := r.Store.AddDeadLetter(ctx, src.ID, map[string]any{
			"asset_kind":  b.Raw.AssetKind,
			"value":       b.Raw.Value,
			"observed_at": b.Raw.ObservedAt.UTC().Format(time.RFC3339Nano),
			"confidence":  b.Raw.Confidence,
			"extra":       b.Raw.Extra,
		}, "invalid record: "+b.Reason); err != nil {
			return err
		}
	}
	return nil
}

// ingestWithPoisonIsolation applies the batch at once; on failure it
// re-drives fact-by-fact so exactly the poison records dead-letter while
// the healthy ones still land (already-ingested facts dedupe at L1/L2 —
// the per-fact re-drive is idempotent).
func (r *Runner) ingestWithPoisonIsolation(ctx context.Context, facts []Fact, stats *RunStats) error {
	if len(facts) == 0 {
		return nil
	}
	st, err := r.Store.IngestFacts(ctx, facts)
	r.accumulate(stats, st)
	if err == nil {
		return nil
	}
	for i := range facts {
		one, ferr := r.Store.IngestFact(ctx, &facts[i])
		if ferr == nil {
			r.accumulateOne(stats, one)
			continue
		}
		payload, mErr := json.Marshal(map[string]any{"fact": facts[i]})
		reason := "ingest failed: " + ferr.Error()
		if mErr != nil { // unmarshalable fact: record the identity instead
			payload, _ = json.Marshal(map[string]any{
				"asset_kind": facts[i].Asset.Kind, "value": facts[i].Asset.Value,
			})
		}
		if dErr := r.Store.AddDeadLetter(ctx, facts[i].Observation.SourceID,
			map[string]any{"raw": json.RawMessage(payload)}, reason); dErr != nil {
			return errors.Join(ferr, dErr)
		}
	}
	return nil
}

// collectRetry runs one Collect with transport retries (FatalError skips
// them: a refused query will not heal by repeating it).
func (r *Runner) collectRetry(ctx context.Context, col Collector, c Ctx) (*Batch, error) {
	attempts := r.MaxAttempts
	if attempts <= 0 {
		attempts = DefaultMaxAttempts
	}
	backoff := r.Backoff
	if backoff <= 0 {
		backoff = DefaultBackoff
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			t := time.NewTimer(backoff << (attempt - 1))
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
		}
		batch, err := col.Collect(c)
		if err == nil {
			return batch, nil
		}
		var fatal FatalError
		if errors.As(err, &fatal) {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("connector %q failed after %d attempts: %v", col.Kind(), attempts, lastErr)
}

// clientFor resolves the per-source transport: connector config overrides
// (requests_per_second / timeout_ms) build a dedicated client; otherwise
// the runner's shared client applies.
func (r *Runner) clientFor(cfg SourceConfig) *Client {
	if r.Client == nil && cfg.RequestsPerSecond <= 0 && cfg.TimeoutMs <= 0 {
		return nil
	}
	if cfg.RequestsPerSecond <= 0 && cfg.TimeoutMs <= 0 {
		return r.Client
	}
	cc := ClientConfig{RequestsPerSecond: cfg.RequestsPerSecond}
	if cfg.TimeoutMs > 0 {
		cc.Timeout = time.Duration(cfg.TimeoutMs) * time.Millisecond
	}
	return NewClient(cc)
}

// deliveryCursor derives one record's L1 cursor from the batch cursor +
// record index (deterministic across re-runs from the same checkpoint).
func deliveryCursor(batchCursor string, index int) string {
	return hexSHA256([]byte(fmt.Sprintf("v1|batch:%s|rec:%d", batchCursor, index)))
}

// observationID derives the caller-assigned observations.id: fnv1a-64
// over "collect:<source_id>:<cursor>", stable across replays of the same
// delivery (collisions land in the L1/L2 dedupe path, never in
// correctness).
func observationID(sourceID int64, cursor string) int64 {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "collect:%d:%s", sourceID, cursor)
	return int64(h.Sum64() & 0x7fff_ffff_ffff_ffff) // positive BIGINT
}

func (r *Runner) accumulate(stats *RunStats, st storage.IngestStats) {
	stats.Ingested += st.ObservationsInserted
	stats.Deduped += st.ObservationsDeduped
	stats.Suppressed += st.Suppressed
	stats.Updated += st.AssetsUpdated
}

func (r *Runner) accumulateOne(stats *RunStats, res storage.FactResult) {
	if res.Suppressed {
		stats.Suppressed++
		return
	}
	if res.ObservationInserted {
		stats.Ingested++
	} else {
		stats.Deduped++
	}
	if res.AssetInserted {
		// A fresh asset is part of Ingested's row story; asset-level
		// counters are informational.
		_ = res
	} else {
		stats.Updated++
	}
}

func (r *Runner) statsJSON(stats *RunStats) map[string]any {
	raw, err := json.Marshal(stats)
	if err != nil {
		return map[string]any{"error": "stats marshal failed"}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{"error": "stats decode failed"}
	}
	return out
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
