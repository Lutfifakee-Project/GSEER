# M4 query-plan evidence — search_index baseline and migration 0004

Environment: PostgreSQL 17.2 (127.0.0.1:5433), synthetic corpus of
50,000 ip assets + 50,000 services (1 per asset, ports distributed 443/80/8443
≈ 1/3 each) + 50,000 observations (1/3 with an `http` evidence payload titled
"Admin Login", 2/3 without http/tls/fingerprint payloads), `ANALYZE`d.
Corpus generator: `seedPlanScaleData` in
`internal/storage/postgres/integration_test.go`; scratch seeder:
`scripts/seed_scratch.sql`; probe: `scripts/planprobe.sh`.
Measured 2026-11-02 with `EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)`,
schema at migrations 0001–0003 (pre-0004 baseline) and 0001–0004 (post).

## Baseline findings (migrations 0001–0003)

| Query | Execution | Dominant costs |
|---|---|---|
| Q1 `asset_value = '198.51.100.100.100'` | 59.0–38.9 ms | Seq Scan on assets (49,999 rows removed) in **both** view arms; Seq Scan + Sort of all observations ×3 (latest_* arms) |
| Q2 `port = 443` | 74.1–79.5 ms | Seq Scan on services (33,334 rows removed); latest_* Seq Scan + Sort ×3 |
| Q3 `ip << '198.51.100.0/24'` | 49.7–43.3 ms | latest_* Seq Scan + Sort ×3 (the ip filter itself already used `ix_assets_ip` Bitmap Index Scan → fine) |
| Q4 `http_title ILIKE '%login%'` | 87.8–93.7 ms | latest_* Seq Scan + Sort ×3; ILIKE filter sits above the DISTINCT ON and cannot be pushed below it |

Universal hot spot: the `latest_http` / `latest_tls` / `latest_fingerprints`
helper views recompute `DISTINCT ON (service_id) … ORDER BY service_id,
observed_at DESC, id DESC` from a full Seq Scan of `observations` plus a
Sort on every search query — the single dominant cost in all four plans.

## Candidate experiments (scratch DB, same corpus)

| Candidate | Effect | Decision |
|---|---|---|
| `ix_observations_http_scan` (partial: `(service_id, observed_at DESC, id DESC) WHERE service_id IS NOT NULL AND evidence ? 'http'`) | planner replaces Seq Scan + Sort with ordered Index Scan in every plan | **adopt** |
| `ix_observations_tls_scan` (same shape, `? 'tls'`) | same | **adopt** |
| `ix_observations_fp_scan` (same shape, `? 'fingerprint'` + `jsonb_typeof(…) = 'array'`) | same | **adopt** |
| `ix_assets_value` (`assets (value)`) | Q1 arms → Index Scan; Q1 15.2 → 7.8 ms | **adopt** |
| `ix_services_port` (`services (port)`) | 1/3 selectivity: Bitmap Heap Scan cost ≈ Seq Scan (Q2 63.6 vs 58.3 ms, within noise) | **reject** — not justified by the plans |
| pg_trgm GIN for `http_title ILIKE` | ILIKE applies to latest_http's computed output column; cannot be pushed below DISTINCT ON; an expression index on the JSONB is not selectable through the view | **reject** — plain-view trade-off documented; maintained backing table (SPEC-05 §3 option, 0003 header) is the escalation path |

## Post-0004 measurements (migrations 0001–0004)

| Query | Baseline | Post-0004 | Speedup | Plan evidence |
|---|---|---|---|---|
| Q1 value eq | 38.9 ms | 7.8 ms | 5.0× | `Index Scan using ix_assets_value` ×2 arms; ordered Index Scans on all three `*_scan` partials |
| Q2 port eq | 79.5 ms | 58.5–61.2 ms | 1.3× | services port filter remains Seq Scan (1/3 selectivity — expected); latest_* arms indexed |
| Q3 ip CIDR | 43.3 ms | 9.3–11.7 ms | 4.6× | `ix_assets_ip` Bitmap Index Scan + indexed latest_* arms |
| Q4 title ILIKE | 93.7 ms | 75.5–77.4 ms | 1.2× | latest_* arms indexed; residual cost is DISTINCT-ON view materialization (16,666 matching http rows) |

Q2/Q4 residuals are inherent to the plain-union-view design at broad
selectivity; they are acceptable for v0.0.1 fixture scale and are covered
by the maintained-backing-table backlog option (M9 performance work).

## Regression guard

`TestSearchPlanIndexes` (integration tag) runs the same four queries over a
migrated DB seeded with `seedPlanScaleData` and asserts the plans select
`ix_assets_value` and the three `ix_observations_*_scan` partial indexes —
so a future schema change that silently degrades these access paths fails
the suite instead of shipping.
