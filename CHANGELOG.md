# GSEER — Changelog

All notable changes per release.  Format: Keep-a-Changelog-ish, newest
first.  Versions follow semver (three-component).

## [v0.1.0] — First Useful Self-Hosted (Phase 3)

Milestones M18–M24.  No breaking changes to the v0.0.2 surface.

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

### Fixed (pre-release corrections, before the v0.1.0 tag)

- **Security**: `github.com/go-chi/chi/v5` upgraded v5.2.1 → v5.5.0,
  clearing GO-2025-3770 / GHSA-vrw8-fxc6-2r93 (open redirect in
  `middleware.RedirectSlashes`) plus three further published chi
  advisories (GHSA-3fxj-6jh8-hvhx, GHSA-j6qf-3m9c-vwj3,
  GHSA-rqrp-8cg2-5958).  The API server never mounted any of the
  affected middleware (client-IP handling is owned by
  `internal/httpx`); `govulncheck ./...` now reports **zero**
  vulnerabilities.  `golang.org/x/net` upgraded v0.56.0 → v0.62.0 for
  the same reason.  A source-level regression test pins the API to the
  advisory-free chi middleware subset.
- **CI**: the lint job moved from `golangci-lint-action@v6` +
  `version: latest` (which resolved to a v1.64.x binary built with Go
  1.24 and could not process a Go 1.26 module) to
  `golangci/golangci-lint-action@v9.3.0` (node24) with golangci-lint
  pinned to **v2.15.0** (Go 1.26).  The existing v2 lint configuration
  is preserved; obsolete v0.0.1/M1 workflow comments were refreshed.
  The nightly full-DB battery is now genuinely nightly: a `schedule`
  trigger (`0 18 * * *` = 01:00 Asia/Jakarta) and a `workflow_dispatch`
  trigger were added — the job runs on schedule and on manual dispatch,
  never on ordinary pushes (the fast integration-test job still runs
  on every push).  The
  `update-windows` job runs the full unit suite, vet and build on
  windows-latest in addition to the updater suite and its race.
- **Lint**: the codebase is clean under golangci-lint v2.15.0 with the
  existing rule set (no rules weakened; the only new exclusion is a
  documented, narrowly-scoped G101 exemption for the compose generator's
  format-placeholder templates).  Dead code removed, unchecked errors
  made explicit, file/dir permission hardening (`update` target 0750,
  executable mode kept for replaced binaries), 63-bit masking for
  fnv-derived BIGINT bookkeeping ids, and eight complex functions
  refactored table-driven without behavior change (full unit + race +
  integration suites green before and after).
- **Updater durability** (`internal/update`): `copyFile` now returns
  only after the copy is written, chmod'd, fsynced AND closed — Close
  errors surface when nothing earlier failed and cleanup never masks
  the primary copy/sync error; the backup keeps the running binary's
  exact permission bits via an explicit chmod (umask-proof, also on
  re-backup over an old `.bak`), so a backup is only reported usable
  once it is fully durable.  A failed post-rename directory fsync is no
  longer silently ignored: it surfaces as the `E_UPDATE_DIRSYNC`
  warning (binary replaced, nothing rolled back, crash durability not
  guaranteed — `Apply` returns the result together with the warning,
  `IsDirSyncWarning` detects it, and `gseer update --apply` prints it
  without the false "left untouched" failure line).  Platform helpers:
  explicit open+fsync+close of the directory on POSIX; on Windows the
  replacement primitive itself is `MoveFileExW` with
  `MOVEFILE_REPLACE_EXISTING|MOVEFILE_WRITE_THROUGH` — Go's `os.Rename`
  calls plain `MoveFileW` with NO flags (never write-through), so the
  updater performs the replace itself; `MOVEFILE_COPY_ALLOWED` is
  deliberately omitted so a cross-volume replace fails loudly instead
  of degrading into a non-atomic copy.  Regression tests cover copy success /
  copy failure / sync failure / close failure and their precedence,
  backup permission preservation, atomic replacement, no-temp-leftover
  cleanup, rollback (incl. failed-rollback reporting) and the
  dir-sync warning contract.  Every `*Error` now carries its wrapped
  cause (`Unwrap`) — `errors.Is` / `errors.As` walk the live chain
  (MoveFileExW error → `*os.LinkError` → `*Error`) instead of a
  flattened string.  Windows-specific coverage (flags passed to
  `MoveFileExW`, replace with/without an existing target, API errors,
  rollback, warning propagation, platform-honest permission semantics
  via `assertPerm`, leak-free injected-close stubs) runs on the
  `update-windows` CI job (windows-latest, Go 1.27.x).  Every
  permission assertion in the updater suite — `apply_test.go`,
  `apply_windows_test.go` and the full-chain `TestApplyFullChain` in
  `update_test.go` included — goes through `assertPerm`: exact POSIX
  bits on Unix, writable/read-only semantics only on Windows (no
  exact-mode claim is made there, and no assertion was weakened or
  removed).

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
- Milestone register entries M13–M17.

## [v0.0.1] — Search Engine Developer Preview (Phase 1)

Milestones M1–M12: GQL (lexer/parser/validator/planner), PostgreSQL
storage + migrations, SearchService, REST API + dashboard + `server
start/stop`, CLI, auth + rate limiting, fixture corpus (labeled
synthetic), quickstart/doctor/packaging, governance pins as CI tests.
No real data; no connectors; search-only (Search ≠ Scan, ADR-0010).
