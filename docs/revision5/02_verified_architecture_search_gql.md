# Section 2 — Verified Source Architecture (Part C: `internal/search/`, `internal/gql/`, `internal/fprule/`, `internal/suppress/`, `internal/dashboard/`, `internal/api/`, `internal/cli/`)

## 2.13 `internal/search/` — SearchService

| File | Symbol | Status | Actual current behavior |
|------|--------|--------|-------------------------|
| `service.go` | `Service` | Implemented | Single search implementation shared by CLI (`gseer search`), REST API, and dashboard. `Search(ctx, Query) (*Result, error)`: validate → plan → execute → shape. `DefaultMode = ModeService` (PD-1). Read-only against `search_index`; no collector/egress dependency (AC-3.1 import-graph test). |
| | `Query` | Implemented | `GQL`, `Mode` ("service"/"asset"), `Sort`, `Limit`, `Timeout`, `Cursor`, `Fields`, `WithStats`. |
| | `Result` / `Row` | Implemented | `Rows []Row`, `NextCursor`, `HasMore`, `QueryInfo`, `Index`, `Explain`, `Notice`. `Row` has `row_kind` (`service` \| `asset-only`), asset fields, service fields (`port`, `transport`, `protocol`, `name`, `product`, `version`, etc.), http/tls fields, `technologies []string`, `sources []string`, `first_seen`, `last_seen`, `stale`. |
| | Mode semantics | Implemented | `ModeService` = one row per (asset, service) pair plus orphan assets (raw `search_index` projection). `ModeAsset` = `DISTINCT ON (asset_id)` collapsing to one row per asset (most recent `last_seen`). |
| | Pagination | Implemented | Keyset pagination over composite row key `(row_id, row_kind)` + sort value. `row_id` alone is NOT unique across the union view (services.id on service rows, assets.id on asset-only rows are independent sequences that can collide numerically). Cursor is base64 `CursorPayload{SortField, RowID, RowKind, Value, Null}`. |
| `facets.go` | `Facets` | Implemented | Same GQL chain as search (parse → validate → `CompileWhere`). Count semantics by mode: service mode counts each (asset,service) row once; asset mode counts `DISTINCT (row_kind, asset_id)`. Dimensions: port, protocol, technology, service, country, source, freshness. |
| `row.go` | `Row` + scanner | Implemented | Projection-aware scanner binds by column name. `sortValue` supports string/int/time fields including `tls.cert_expiry`. |
| `stats.go` | `Stats` | Implemented | Index freshness metadata (`Empty`, `FixtureOnly`, `Assets`, `Services`, `Observations`, `LastObserved`, `Notice()`). Used by doctor and `WithStats`. |

## 2.14 `internal/gql/` — field registry, validator, planner

| File | Symbol | Status | Actual current behavior |
|------|--------|--------|-------------------------|
| `validator/validator.go` | `fields` registry | Implemented | 24 canonical fields. Key entries: `port` (TypeInt, canonical `port`), `transport`, `protocol`, `name`, `product`, `version` (all TypeString), `http.status` (TypeInt, alias `status`), `http.title` (alias `title`), `http.server` (alias `server`), `tls.cert_expiry` (TypeTime, alias `cert_expiry`), `technologies` (TypeList, aliases `tech`, `fingerprint`), `sources` (TypeList), `first_seen`/`last_seen` (TypeTime), `stale` (TypeBool), `geo_country` (alias `country`), `geo_region` (alias `region`), `geo_city` (alias `city`), `ip` (TypeString + `IN_CIDR`), `domain`/`hostname` (TypeString + `IN_DOMAIN`). |
| | Alias rewrite | Implemented | `lookupField` resolves user-written name → canonical key; alias rewritten in place. `port` and `port` are the same canonical field `port` (no `service.port` prefix exists in v0.1.0). |
| | Typing | Implemented | Strict: INT fields reject quoted values; TIME fields require RFC 3339; BOOL accepts only `"true"`/`"false"`; LIST requires CONTAINS. |
| | `ALL(...)` | Rejected | `QuantAll` → `GQL_E_NOT_SUPPORTED_IN_VERSION` with `!ANY` suggestion. Kept rejected in v0.2.0 unless separately approved. |
| `planner/planner.go` | `searchColumns` map | Implemented | Canonical path → `search_index` column. `port` → `port`, `transport` → `transport`, etc. Dotted paths (`http.title` → `http_title`, `tls.cert_subject_cn` → `cert_subject_cn`). **No `service.port` / `service.transport` prefixed canonical paths exist.** |
| | `Compile` | Implemented | Lowers validated AST to parameterized SQL Plan against `search_index`. Values are `$N` placeholders (never interpolated). Keyset cursor, `DISTINCT ON (asset_id)` for asset mode, deterministic tiebreaker `row_id ASC, row_kind ASC`. |
| | Sortable fields | Implemented | `port`, `transport`, `protocol`, `name`, `product`, `version`, `first_seen`, `last_seen`, etc. List (`technologies`, `sources`) and bool (`stale`) excluded from ORDER BY. |

### GQL alias findings (relevant to Section 10)

- There is NO `service.port` / `service.transport` / `service.protocol` / `service.name` / `service.product` / `service.version` field in v0.1.0. The canonical fields are bare: `port`, `transport`, `protocol`, `name`, `product`, `version`.
- `port` is TypeInt (canonical `port`). It is service-level data (from `search_index.port`), NOT asset-level. Revision 4 must not describe `port` as an asset-level port filter.
- Sorting/faceting operate on these bare service fields; no duplicate semantic dimensions exist in v0.1.0.

## 2.15 `internal/fprule/` — fingerprint engine

| File | Symbol | Status | Actual current behavior |
|------|--------|--------|-------------------------|
| `rule.go` | `Rule`, `MatchSpec`, `Test`, `Family`, `Field` | Implemented | Closed family set (web-server, cms, language, framework, web-tech, service). Closed field set (http.server, http.title, tls.version, tls.cert_subject_cn, tls.cert_issuer_cn, ssh.banner). `Lint()` validates ids, family, product, confidence, provenance, matches, tests (≥2, ≥1 positive + ≥1 negative), exploitation-pattern rejection. `LoadDir(dir)` loads `*.yaml` rules, one per file named `<id>.yaml`. |
| `engine.go` | `Registry.Apply(evidence)` | Implemented | Rule fires when ANY match spec hits (OR); best hit per rule; one product survives (highest confidence); returns `[]Match{RuleID, Family, Product, Version, Confidence}`. Fail-closed on compile problems. `Products(matches)` → `[]string` product names. |
| `seed/` | 22 rule files | Implemented | Enumerated in Section 9. |

## 2.16 `internal/suppress/` — suppression

| File | Symbol | Status | Actual current behavior |
|------|--------|--------|-------------------------|
| `matcher.go` | `Matcher` | Implemented | Compiled rule set: `ips`, `cidrs`, `domains`, `suffixes`, `sources` maps. `MatchesAsset(assetKind, v)` returns the suppressing rule or nil. Most-specific-first ordering (longest CIDR prefix / longest suffix; tie → smallest rule id). Disabled and expired rules skipped. Fail-closed on un-normalizable value. |
| `normalize.go` | `NormalizeIP`, `NormalizeDomain`, `NormalizeValue` | Implemented | Canonical: lowercase, no trailing dot, IDNA punycode (A-labels), netip-canonical IPs, masked CIDRs. |
| `doc.go` | semantics | Implemented | Two-layer fail-closed enforcement: Go gate (store) + DB backstop trigger (migration 0005). Cross-type propagation (domain rule blocking resolved IPs) deliberately NOT automatic. |

**Suppression relationship finding:** Suppression matches **assets** (asset-level only). There is no `suppressions` → `assets` foreign key (`suppression_applications.asset_id` is a PLAIN BIGINT, not a FK). One suppression rule can match multiple assets; one asset can match multiple rules (rule table has `UNIQUE (kind, value)`; matching is by value, not by asset row). `suppression_applications` is the audit/application record. There is NO Asset → Suppression N:1 relational constraint. Service ingestion uses the same suppression boundary only indirectly (asset gated when written; retraction cascades asset→services→observations via ON DELETE CASCADE).

## 2.17 `internal/dashboard/`, `internal/api/`

| File | Symbol | Status | Actual current behavior |
|------|--------|--------|-------------------------|
| `dashboard/dashboard.go` | dashboard | Implemented (thin) | Shares the `search.SearchService`; dashboard test exists. |
| `api/server.go`, `api/middleware.go`, `api/envelope.go` | REST API | Implemented | Token-authed (SHA-256 hash of plaintext token), trusted-proxy opt-in, `/api/v1/search`, `/api/v1/facets`, `/api/v1/export`, `/api/v1/health`. Shares `search.SearchService` so GQL semantics identical across CLI/API/dashboard. |
| **No `/api/v1/scan`** | — | Absent | Confirmed absent. Active observation is outside v0.1.0/v0.2.0 implementation scope. |

## 2.18 `internal/cli/` — commands

| Command | File | Status | Behavior / exit code |
|---------|------|--------|----------------------|
| `gseer` root | `root.go` | Implemented | Help tree truthful; `version` working; coming-later stubs. Exit codes: 0 success, 2 invalid input, 3 config, 4 unavailable, 5 auth, 64 usage/planned, 70 internal. |
| `gseer search` | `search.go` | Implemented | Calls `search.Service`. `--validate`, `--explain`, `--facets`, `--mode`, `--sort`, `--limit`, `--cursor`. |
| `gseer setup` | `setup.go` | Implemented | `--self-hosted` (reachability → PG version → migrations to head), `--with-compose` (write compose + `.env`), `--load-fixtures`/`--purge-fixtures` (QS-6 gate), `--db-url` override. |
| `gseer doctor` | `doctor.go` | Implemented | Config validity, binary identity, DB checks, index contents (EMPTY/fixture-labeled/live counts), server reachability (client profile). Read-only; never migrates. |
| `gseer source` | `source.go` | Implemented | `list`/`add`/`enable`/`disable`. Registration records terms review; enablement explicit; terms gate outranks hand-flipped enabled row. |
| `gseer collect` | `collect.go` | Implemented | `run` (`--source`/`--all`), `status`, `redrive`. Passive only; nothing scans; nothing auto-runs. |
| `gseer suppress` | `suppress.go` | Implemented | `add`/`list`/etc. Suppression policy management. |
| `gseer rules` | `rules.go` | Implemented | `list`/`validate` (lint + false-positive regression tests). |
| `gseer db` | `db.go` | Implemented | Migration/status/diagnose commands. |
| `gseer server` | `server.go` | Implemented | `start`/`status`/`stop`. API + dashboard. Migrations run automatically on start (QS-7). |
| `gseer export` | `export.go` | Implemented | Streamed JSON/CSV/NDJSON export. |
| `gseer update` | `update.go` | Implemented | Signed manifest → verify → checksum → atomic replace. |
| `gseer asset` | `coming_later.go` | **Hidden stub** | Registered as hidden command; any invocation prints pointer line + exits 64. NOT implemented. |
| `gseer scan` | `coming_later.go` | **Hidden stub** | Registered as hidden command; any invocation prints pointer line + exits 64. NOT implemented. Active observation waits on P2/PAC regime. |

### `gseer asset` registration verification

- `gseer asset` IS registered as a planned command (hidden, `DisableFlagParsing: true`, `Args: cobra.ArbitraryArgs`).
- It prints: `E_NOT_IMPLEMENTED: "gseer asset" is planned, not in this release (v0.1.0).` + `Asset detail view is planned for a later release — see docs/ROADMAP.md.` + `Exit code 64.`
- It calls `exitFunc(ExitUsagePlanned)` → exit code **64**.
- `TestNoPhantomCommands` (AC-5.7–5.8) asserts `scan` and `asset` are registered as `Hidden=true` so invocation does not fall through to cobra's "unknown command".

## 2.19 Summary of gaps relevant to v0.2.0

1. **No JSONL import command exists** anywhere in v0.1.0. Section 5 must define it from scratch.
2. **No service-level observation ingestion** in the collect path (asset-level only). Section 4/5 define the ServiceFact contract.
3. **No `service.port` prefixed GQL fields**; bare `port` etc. are service-level. Section 10 defines canonical/legacy aliases.
4. **Suppression is asset-level only**, no FK, no service-level suppression gate. Section 7 corrects the N:1 claim.
5. **`gseer asset` and `gseer scan` are stubs (exit 64).** Section 10 resolves the release boundary.
