# Section 10 — Canonical GQL Compatibility Specification

## 10.1 Canonical aliases (field registry)

Revision 4's recommendation adopted for v0.2.0:

| Canonical field (planner target) | Type | Legacy aliases | search_index column |
|----------------------------------|------|----------------|---------------------|
| `port` | Integer | `port` | `port` |
| `transport` | String | `transport` | `transport` |
| `protocol` | String | `protocol` | `protocol` |
| `name` | String | `name` | `name` |
| `product` | String | `product` | `product` |
| `version` | String | `version` | `version` |

**There is NO `service.port` / `service.transport` / ... prefixed field in v0.1.0.** The canonical fields are bare (`port`, `transport`, ...). Revision 4's "service-prefixed" canonical fields (`service.port`) do not exist in the source.

**Adopted compatibility direction for v0.2.0 (reconciled with source):**
- `service.port` → canonical `port` (the service-prefixed form normalizes to the same planner field as bare `port`).
- `port` → legacy alias, normalizes to canonical `port`.
- Equivalent for `transport`, `protocol`, `name`, `product`, `version`.

Rationale: The validator registry already maps aliases to canonical paths. Adding `service.port` as an alias that rewrites to canonical `port` preserves old queries (`port==443`) while allowing the service-prefixed spelling. Aliases normalize to the SAME planner field and SQL expression (`search_index.port`).

**Do not describe `port` as an asset-level port filter.** Both `port` and `service.port` refer to the same service-level data (`search_index.port`). `port` is TypeInt.

## 10.2 Correct types (verified + corrected)

| Field | Type | GQL value form | Notes |
|-------|------|----------------|-------|
| IP address (`ip`) | IP (string) | `"203.0.113.10"` | TypeString + `IN_CIDR`. Column `ip` is PG inet. |
| Port (`port`, `http.status`) | Integer | bare number `443` | TypeInt. Range ops allowed. |
| Status code (`http.status`) | Integer | bare number `200` | TypeInt, alias `status`. |
| First/last observation (`first_seen`, `last_seen`) | Timestamp | RFC 3339 `"2026-01-01T00:00:00Z"` | TypeTime. Range ops allowed. |
| Certificate expiration (`tls.cert_expiry`) | Timestamp | RFC 3339 | TypeTime, alias `cert_expiry`. Column `cert_expiry` (no `tls_` prefix in view). |
| Stale (`stale`) | Boolean | `"true"`/`"false"` | TypeBool. Only eq/neq. |
| Text attributes (`product`, `version`, `transport`, `protocol`, `name`, `http.server`, ...) | String | quoted `"nginx"` | TypeString / TypeList. |

**Parser literal support (verified):** The GQL lexer/parser supports StrValue (quoted), IntValue (bare number), ListValue (`[a, b]`). Timestamps are quoted strings validated as RFC 3339 by the validator. Bool is `"true"`/`"false"` quoted. The existing parser supports all required literal types.

**Preserve old query syntax:** `port=443` (single `=` sugar for `==`), `port==443`, `title="phpMyAdmin"`, `ip IN_CIDR "203.0.113.0/24"`, `domain IN_DOMAIN "example.net"`, `ANY(host){...}`, `ANY(service){...}`. All remain valid. Document compatibility behavior; do not silently break old queries.

## 10.3 `ALL(...)` rejection

`ALL(...)` is rejected by the validator (`GQL_E_NOT_SUPPORTED_IN_VERSION` with `!ANY` suggestion). **Keep `ALL(...)` rejected in v0.2.0 unless a separate, justified decision is approved.** Do not introduce new query-language semantics unnecessarily.

## 10.4 Result semantics

### One row per service (default service mode)
`ModeService` (PD-1 default): one row per (asset, service) pair plus orphan assets (asset-only rows where no service matches). A multi-service asset appears as multiple rows (one per service), each repeating the asset fields.

### One row per asset (asset mode)
`ModeAsset`: `DISTINCT ON (asset_id)`, preferring the row with the most recent `last_seen`, tiebroken by `row_id ASC`. One row per asset.

### Observations and current service attributes
Observations contribute to service attributes (`port`, `transport`, `protocol`, `name`, `product`, `version`, `last_seen`) via the service upsert (`latest sighting authoritative`; `last_seen = GREATEST`; `stale=false`). Each observation row is tied to one service (or asset-level if `service_id` NULL).

### Stable pagination with multi-service assets
Keyset pagination over composite row key `(row_id, row_kind)` + sort value. `row_id` alone is NOT unique across the union view (services.id on service rows, assets.id on asset-only rows are independent sequences that can collide numerically), so pagination advances over `(row_id, row_kind)`. In asset mode, pagination advances over deduplicated assets (`DISTINCT ON (asset_id)` applied before cursor/ORDER/LIMIT).

### Facets
Facets run the same GQL chain (`CompileWhere`) as search. Service mode counts each (asset,service) row once; asset mode counts `DISTINCT (row_kind, asset_id)`. Dimensions: port, protocol, technology, service (`name`), country, source, freshness. No duplicate semantic dimensions (aliases normalize to the same field).

### CLI / API / dashboard share one SearchService
`internal/search.Service` is the single implementation. `gseer search` (CLI), `GET /api/v1/search`, and the dashboard all call it. GQL semantics and pagination are identical everywhere.

## 10.5 Concrete queries and expected result shapes

```
gseer search 'port==443'
  → service mode: one row per (asset, service on port 443). Row: {asset_id, asset_value, port:443, transport, protocol, product, version, last_seen, technologies, sources, ...}

gseer search 'port==443' --mode asset
  → one row per asset that has a service on port 443 (DISTINCT ON asset_id).

gseer search 'product="nginx" && port==443'
  → services with product nginx on port 443.

gseer search 'ANY(host){ port==443 && product="nginx" }'
  → hosts (assets) that have ANY service on port 443 with product nginx. EXISTS subquery over search_index rows of same asset_id with row_kind='service'.

gseer search 'port==443' --facets
  → facets over port/protocol/technology/service/country/source/freshness for the filtered row set.

gseer search 'port==443' --validate
  → validate only, no DB contact.

gseer search 'port==443' --explain
  → AST → branch → SQL plan (parameterized, no literal interpolation).
```

Old queries remain valid:
```
gseer search 'port=443'            # `=` sugar for `==`, still valid
gseer search 'server="nginx"'      # alias `server` → canonical `http.server`
gseer search 'status==200'         # alias `status` → canonical `http.status`
```

## 10.6 Sorting and facets

- Sort fields: the sortable set (`port`, `transport`, `protocol`, `name`, `product`, `version`, `first_seen`, `last_seen`, `ip`, `domain`, `hostname`, etc.). List (`technologies`, `sources`) and bool (`stale`) excluded from ORDER BY. Default sort: `last_seen DESC`.
- Aliases (`service.port` → `port`) normalize to the same sortable field and SQL expression; sorting does not create duplicate dimensions.
- Facet dimension names are fixed; unknown names rejected with `E_QUERY_FACET`.

## 10.7 What v0.2.0 must NOT do
- Do not introduce `service.port` as a *different* field from `port`; they are the same.
- Do not describe `port` as asset-level.
- Do not add `ALL(...)` support without separate approval.
- Do not introduce new query-language semantics unnecessarily.
