# GSEER — Changelog

All notable changes per release.  Format: Keep-a-Changelog-ish, newest
first.  Versions follow semver (three-component).  The milestone-level
verification evidence lives in `GSEER_BACKLOG.md`.

## [v0.1.0] — First Useful Self-Hosted (Phase 3)

Milestones M18–M24 (see `GSEER_BACKLOG.md` for the per-milestone test
evidence).  No breaking changes to the v0.0.2 surface.

### Added

- **Fingerprint rule engine** (`internal/fprule`, M18): versioned rule
  collection (22 seed rules across web-server / cms / language /
  framework / web-tech / service families) with provenance, evidence-
  based confidence tiers, mandatory false-positive regression tests,
  and a lint that rejects exploitation/credential-shaped signatures.
  Ingestion enrichment attaches derived `fingerprint.technologies` to
  observations whose http/tls/ssh evidence matches (source-provided
  fingerprints always win; fixture loads stay verbatim).  Contribution
  format + confidence contract: `docs/RULES.md`.
- **`gseer rules list` / `gseer rules validate [--dir]`** (M18): the
  real implementations (previously exit-64 stubs); validate lints AND
  runs every rule's regression tests (exit 0 clean / 2 problems).
- **Faceted search** (M19): `planner.CompileWhere` + `search.Facets` —
  port, protocol, technology, service, country, source, freshness
  aggregates over the same GQL filter and SearchService as search
  (service mode counts rows; asset mode counts distinct assets).
  Surfaces: `gseer search --facets [--facet …] [--max-values]`,
  `GET /api/v1/facets`, dashboard facet panel.
- **Search export** (M20): `internal/export` streams JSON / CSV /
  NDJSON through the SearchService's keyset cursor (stable page
  sequence, O(page) memory).  CLI `gseer export` (stdout streams carry
  status on stderr; safe filenames — `..`/control-char/overwrite
  refusal) and streaming `GET /api/v1/export` with a server-generated
  Content-Disposition.  RFC 4180 CSV escaping; no credentials ever in
  the stream (pinned by tests).
- **Signed update system** (M21): `internal/update` + `gseer update
  --check|--apply` — Ed25519 manifest verification against the key
  embedded in the binary (verification FIRST, download LAST), semver
  version gate (downgrades refused without `--force`; unparseable
  versions fail closed), exact GOOS/GOARCH artifact selection,
  SHA-256 + size verification, capped/redacting HTTP fetcher, atomic
  rename with `.bak` backup and unconditional rollback.  Maintainer
  tooling: `cmd/gseer-release` (keygen/sign/verify).  `docs/UPDATE.md`.
- **Full Docker stack** (M22): multi-stage non-root `Dockerfile`,
  `docker-compose.full.yml` (db + server, healthchecks via `/health`,
  `depends_on: service_healthy`, persistent volume, NO published DB
  port, host-loopback API binding, mandatory generated API token) and
  `gseer setup --with-compose --full` (writes the stack + `.env` with
  both secrets at 0600).  Lifecycle docs: `docs/DOCKER.md`.
- **Dead-letter re-drive** (M23): `gseer collect redrive
  [--source|--all|--discard]` — rebuilds preserved records through the
  same normalization pipeline (suppression still applies; failures bump
  attempts and stay pending; `--discard` is the operator's explicit
  judgment).
- **Backup/restore procedure** (M23): documented pg_dump/pg_restore
  workflow inside the compose stack (`docs/DOCKER.md` §4).
- Config: `fingerprints:` (disabled / rules_dir), `update:` (manifest_url)
  sections with `GSEER_FINGERPRINTS_*` / `GSEER_UPDATE_MANIFEST_URL` env
  overrides.

### Changed

- `gseer update` and `gseer export` are real commands (previously
  hidden exit-64 stubs); only `scan` and `asset` remain planned.
- `technologies` in `search_index` can now be populated by rule
  enrichment at ingestion (previously only source-provided
  fingerprint evidence).

## [v0.0.2] — Passive Intelligence (Phase 2)

Milestones M13–M17.

### Added

- **Suppression engine** (M13): `suppressions` + `suppression_applications`
  (migration 0005), the Go ingestion gate + DB backstop trigger
  (`GSUP1` → `E_DB_SUPPRESSED`), retroactive retraction, and
  `gseer suppress add/list/remove`.  IPv4/IPv6/IDNA/trailing-dot/
  label-boundary matching pinned by tests; fixture purge stays
  orthogonal.
- **Collection framework** (M14): migration 0006 (collection_jobs,
  source_checkpoints, ingestion_dead_letter, scopes), `internal/collect`
  (terms gate → checkpoints → retries/backoff → normalize/dedupe →
  suppression-gated ingestion → poison dead-lettering → job
  bookkeeping), rate-limited HTTP client with body cap + credential
  redaction, asset-only honesty contract.
- **Source reviews + connectors** (M15): `docs/SOURCES.md` register
  (verdicts enforced by code — `E_SOURCE_TERMS`); `dns` + `ip2asn`
  reviewed-enabled; `rdap` + `ct` implemented but disabled pending
  provider authorization; `gseer source list/add/enable/disable`,
  `gseer collect run/status`.
- **Security regression review of the M10 surfaces** (M16): security
  response headers on API + dashboard, trusted-proxy client-IP opt-in
  (XFF ignored by default, right-most hop through configured CIDRs,
  fail-closed parse), dashboard GET-only/CSRF-surface pin, PID-file
  identity verification + 0600.
- v0.1.0-style milestone register entries M13–M17 in
  `GSEER_BACKLOG.md`.

## [v0.0.1] — Search Engine Developer Preview (Phase 1)

Milestones M1–M12: GQL (lexer/parser/validator/planner), PostgreSQL
storage + migrations, SearchService, REST API + dashboard + `server
start/stop`, CLI, auth + rate limiting, fixture corpus (labeled
synthetic), quickstart/doctor/packaging, governance pins as CI tests.
No real data; no connectors; search-only (Search ≠ Scan, ADR-0010).
