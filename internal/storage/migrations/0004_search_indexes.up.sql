-- GSEER migration 0004_search_indexes.up.sql — evidence-driven supporting
-- indexes for the search_index union view (M4 STEP 2/3).
--
-- Evidence basis (EXPLAIN (ANALYZE, BUFFERS) over the synthetic 50k
-- asset/service/observation corpus, PostgreSQL 17.2 — see
-- internal/storage/postgres/testdata/plan_baseline.md for the full plans):
--
--   Q1 asset_value eq      38.9ms →  7.8ms (5.0x)
--   Q2 port eq             79.5ms → 61.2ms (1.3x)
--   Q3 ip CIDR             43.3ms →  9.5ms (4.6x)
--   Q4 http_title ILIKE    93.7ms → 77.4ms (1.2x)
--
-- What the evidence justified:
--
--   1) ix_observations_{http,tls,fp}_scan — the latest_* helper views
--      (0002) recompute DISTINCT ON (service_id) … ORDER BY
--      service_id, observed_at DESC, id DESC on EVERY search query;
--      at baseline that is a Seq Scan of all observations plus a Sort
--      per view arm — the single dominant cost in all four plans.
--      Each partial index pre-filters (evidence kind) and pre-orders
--      (service_id, observed_at DESC, id DESC) exactly matching the
--      view's ORDER BY, so the planner replaces Seq Scan + Sort with
--      an ordered Index Scan.
--
--   2) ix_assets_value — asset_value point equality (the primary M9
--      planner facet).  The (kind, value) unique constraint cannot
--      serve value-only equality (leading column kind missing), so
--      both view arms Seq-Scanned 50k assets (49,999 rows removed
--      each).  A plain btree on (value) turns each arm into an index
--      scan.
--
-- What the evidence REJECTED (kept out of this migration on purpose):
--
--   * ix_services_port — at the corpus's port distribution (1/3
--     selectivity) the planner's Bitmap Heap Scan cost the same as
--     the Seq Scan it replaced (63.6ms vs 58.3ms Q2, within noise);
--     port-only searches are inherently broad at fixture scale.
--     Revisit with real corpus statistics (M9) if selective port
--     predicates materialize.
--
--   * a pg_trgm GIN index for http_title ILIKE — the ILIKE predicate
--     applies to latest_http's computed output column, which cannot
--     be pushed below the DISTINCT ON; an expression index on the
--     underlying JSONB would not be selectable through the view.
--     Sub-100ms at 50k rows is acceptable for v0.0.1; the maintained
--     backing table (SPEC-05 §3 performance option, 0003 header)
--     remains the escalation path and stays on the backlog.
--
-- Transaction-safe like every GSEER migration (no CONCURRENTLY):
-- the migrate driver wraps this file in one atomic transaction.

-- ---------------------------------------------------------------------------
-- latest_* helper-view scan indexes (dominant cost in every baseline plan).
-- Each predicate matches the corresponding 0002 view WHERE clause verbatim
-- so the planner can prove implication and select the partial index.
-- ---------------------------------------------------------------------------
CREATE INDEX ix_observations_http_scan
    ON observations (service_id, observed_at DESC, id DESC)
    WHERE service_id IS NOT NULL
      AND evidence ? 'http';

CREATE INDEX ix_observations_tls_scan
    ON observations (service_id, observed_at DESC, id DESC)
    WHERE service_id IS NOT NULL
      AND evidence ? 'tls';

CREATE INDEX ix_observations_fp_scan
    ON observations (service_id, observed_at DESC, id DESC)
    WHERE service_id IS NOT NULL
      AND evidence ? 'fingerprint'
      AND jsonb_typeof(evidence -> 'fingerprint' -> 'technologies') = 'array';

-- ---------------------------------------------------------------------------
-- asset_value point equality (primary search facet; both view arms).
-- ---------------------------------------------------------------------------
CREATE INDEX ix_assets_value ON assets (value);
