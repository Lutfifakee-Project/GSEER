# GSEER — Changelog

All notable changes per release.  Format: Keep-a-Changelog-ish, newest
first.  Versions follow semver (three-component).

## [Unreleased] — Personal-Mode Wire-Up Fixes

Found by the first real personal-mode walkthrough (`go install` →
`setup --personal` → daily commands).  No security boundary is
weakened; the PostgreSQL paths are untouched.

### Fixed

- **`go install` binaries report their real version.**  The version,
  commit, and built_at fields fall back to the toolchain's own build
  information (`debug.ReadBuildInfo`: module version, `vcs.revision`,
  `vcs.time`) when no `-ldflags` injection is present — so
  `go install .../cmd/gseer@v0.2.1` no longer prints
  "gseer dev / commit unknown".  Values are never fabricated: a plain
  development build keeps the honest "dev"/"unknown" reporting.
- **`gseer doctor` runs real personal-mode checks.**  A personal
  profile used to fall into the client branch ("DB checks: skipped
  (client profile has no local database)" + a server-endpoint hint).
  Doctor now checks the database file (existence first — it never
  creates one), the read-only SQLite migration state, and the same
  index-contents honesty contract (EMPTY / FIXTURE-labeled / counts).
- **`gseer setup --load-fixtures` / `--purge-fixtures` follow the saved
  personal profile.**  The standalone fixture actions tried to parse
  the SQLite database FILE as a `postgres://` DSN
  (`E_DB_UNREACHABLE: cannot parse …`).  They now ingest/purge through
  the personal store behind the same QS-6 gate; the load output no
  longer suggests `gseer server start`, which personal mode cannot run.
- **`gseer export` works in personal mode.**  The exporter now pages
  through any `Search` implementation (`export.Searcher`), so personal
  mode streams the same JSON / CSV / NDJSON via the embedded SQLite
  service.  The PostgreSQL path is byte-identical.
- **Honest refusals instead of PostgreSQL errors on the personal
  profile.**  `source` / `collect` (collection registry is
  PostgreSQL-only) and `host` (report runs through the PostgreSQL
  service) now refuse with `E_PROFILE_UNSUPPORTED` naming what is
  missing and what to do, instead of a misleading
  `E_DB_UNREACHABLE: cannot reach PostgreSQL`.
- **`setup --personal` next-steps hint fixed**: it suggested
  `gseer server start`, which refuses non-self-hosted profiles; the
  hint now points at the personal surfaces (search, fixtures,
  suppression, backup).

### Added

- Personal-mode test coverage: standalone fixture load/purge round
  trip, doctor personal checks (including the read-only no-create
  contract for a missing database file), the profile gates, and a
  personal NDJSON export through the CLI.

## [v0.2.1] — Compatibility Layer, Personal Mode, Release Engineering

Compatible increment over v0.2.0.  PostgreSQL stays the primary engine
for self-hosted server deployments; SQLite personal mode is additive
and profile-gated.  No security boundary is weakened.

### Added

- **GQL compatibility layer**: `*=` suffix-anchored fuzzy match, bare
  free text, `after`/`before` rewrites, derived booleans, empty-value
  semantics, quoted-integer coercion (`port="443"`), field aliases
  (`org`, `app`, `base_protocol`, `status_code`, `cert.subject.cn`),
  and the `host` alias matching hostname OR IP.  Grammar + examples in
  `docs/SEARCH-SYNTAX.md`.
- **Personal mode on SQLite** (`gseer setup --personal`,
  `GSEER_PROFILE=personal`): single-file DB (default
  `~/.gseer/gseer.db`) with the SQ-01…SQ-06 remediation set (upsert
  FirstSeen consistency, millisecond timestamps, DSN special-character
  safety, 0600/0700 file-permission policy, single-writer WAL +
  `busy_timeout`, deny-wins suppression race with audit rows and
  fail-closed expiry), a dedicated SQLite search planner with
  PostgreSQL-parity tests, and `gseer db backup` / `gseer db restore`.
- **Release engineering (REL-01/02/03)**: module-root `main` as a pure
  `cli.Execute` delegate so `go install .../cmd/gseer@v0.2.1` works,
  validated GoReleaser v2.20.0 configuration with a green snapshot
  matrix, pinned release workflow producing draft releases only, and a
  CI release-config validation job.

### Fixed

- Single-host `/32` CIDR text-equality and suffix-wildcard matching
  (`ip *= "*.10"`) via `host(inet)` instead of masked `ip::text`.
- Quoted integer literals (`port="443"`, `port IN (80,"443")`) coerce
  in place; non-numeric quoted values still fail with a typed error
  and hint.

### Docs

- Neutral engine references outside `docs/SOURCES.md` (brand policy);
  `.gitignore` safety net for personal-mode SQLite artifacts.
- Root layout tidied: release notes → `docs/releases/`, banner →
  `docs/assets/`; internal evidence docs (architecture-spec revisions,
  CI integration report) are local-only and no longer published.

## [v0.2.0] — Hardened Self-Hosted Readiness

The hardened deployment model owner-approved as OD-1…OD-7 (Revision
6.7.2) lands in the binary, with the release-quality verification round
(fresh installs, fresh-clone reproducibility, PostgreSQL 16.15 + 17.2
matrices, real-binary E2E).  No security boundary is weakened; the
least-privilege direction stays one-way.

### Added

- **Migration 0016 — suppression policy hardening (pinned, hash-enforced).**
  The `gseer_policy` enforcement layer: refusal-first guard (OD-3),
  SECURITY DEFINER policy functions, statement-level advisory lock
  (GSER/POL1 keys, OD-6), `fn_check_asset_suppression` v2 backstop.
  Versions 0007–0015 stay permanently reserved (OD-4).  The pinned
  Rev 6.7.2 artifacts are embedded byte-for-byte
  (`internal/storage/migrations/`, `deploy/sql/`) and their SHA-256
  hashes are enforced by the test suite (`artifacts_test.go`).
- **Migration 0017 — search safe-casts (GAP-1 mandate).**
  `gseer_safe_int` / `gseer_safe_timestamptz` helpers and rebuilt
  `latest_http` / `latest_tls` views: malformed evidence can no longer
  break search/facets/export/host (a malformed value yields an empty
  cell instead of a query failure).  Up/down round-trip equality is
  automated.
- **Hardened setup — the deployment order in one command.**
  `gseer setup --self-hosted` now runs bootstrap → migrate → runtime
  grants → privilege audit (steps 1–4 of the approved sequence, in that
  order because the 0016 guard requires the roles), provisions the
  `gseer_app` runtime password (generated, printed once; or
  deterministic via `--runtime-password-env`; rotate via
  `--reset-runtime-password`), and refuses the deployment with the
  complete failure list when the audit fails.
- **Runtime credential switch (M-B).** `database.runtime_dsn` /
  `GSEER_DB_RUNTIME_DSN`: the server serves traffic ONLY as the
  least-privilege `gseer_app` credential; CLI data-plane commands
  prefer it and print an explicit notice when falling back to the
  operator credential (never silently).  Suppression writes go through
  the 0016 policy functions in every path (direct DML is refused by the
  policy gate with `GSUP2`).
- **In-binary fail-closed privilege audit (M-C, OD-5).**
  `gseer server start` runs the embedded audit under `gseer_app`
  BEFORE the port binds: a missing grant, unsafe role attribute, or
  escalation path in the membership closure refuses startup with the
  full failure list (exit 5).  A runtime-credential identity check
  refuses to serve as anyone other than `gseer_app` (exit 5).  No
  bypass exists.  `gseer doctor` runs the same audit as a check.
- **`gseer host <ip|hostname|domain>`** — read-only host inspection:
  the asset row (kind/value/ASN/geo/sources/first-last seen), every
  service with its latest HTTP/TLS/fingerprint state, and the
  observation provenance breakdown; `-o json` for machines.
- **PostgreSQL 16+ minimum (OD-7).** Enforced by `gseer setup` and
  `gseer doctor` (`minimum 16`); the compose stack keeps
  `postgres:17-alpine`.
- **Docker full stack, hardened (EU-2).** The server container
  provisions itself (bootstrap → migrate → grants → audit) and serves
  under `gseer_app`; the generated `.env` carries a dedicated
  `GSEER_APP_PASSWORD`; no container ever serves as the operator.

### Fixed

- **Collected assets carry source provenance.** Passive collectors now
  stamp the source name onto the asset row (the fixture loader already
  did): the `sources` facet, search_index projections, and `gseer host`
  reflect collector data instead of an empty array (a documented v0.1.0
  limitation, now closed with a regression test).
- **Honest migration counters.** `migrations: N applied` counts FILES,
  not version numbers — under the reserved 0007–0015 range the v0.1.0 →
  v0.2.0 upgrade applies 2 files to head 17 (not "11 applied").
- **Test-suite hermeticity and order independence.**
  `TestSetupSelfHostedFreshMachineContract` no longer depends on the
  absence of a local PostgreSQL (machines running one — CI included —
  made it fail); `executeRoot` resets cobra's per-command help/version
  flags so the banner/help tests no longer poison the E2E tests below
  them (the same shared-state class closed for command flags earlier).
- Integration harnesses bootstrap the hardened roles before migrating,
  derive their per-test DSNs from `GSEER_TEST_PG_ADMIN` (the suite now
  runs unchanged against any instance — the PG16/PG17 matrix), and the
  v0.1.0 → v0.2.0 upgrade path is an automated test
  (`TestV010ToV020UpgradePath`).

### Documentation (this release)

- `docs/DEPLOYMENT.md` rewritten around the implemented model (the
  SPECIFIED→VERIFIED gap is closed): credential model, the automated
  hardened sequence, what the audit checks, refusal table with safe
  repairs, the no-bypass rationale, exit-code table (5 = security
  refusal), and the hash-pinned artifact register.
- `docs/QUICKSTART.md` rewritten as the complete verified v0.2.0
  workflow (build → compose → hardened setup → runtime credential →
  doctor → scope → collection → GQL search → `gseer host` → hardened
  server start → health → shutdown) with troubleshooting picks.
- README, INSTALL, `.env.example`, DOCKER, UPDATE, and ROADMAP aligned
  to the v0.2.0 behavior; every documented command executed against the
  real binary on PostgreSQL 16.15 and 17.2.

### Verified (release evidence)

- Unit suites: 19 packages green.  Integration suites (build tag
  `integration`): green on PostgreSQL 16.15 **and** 17.2 (fresh
  databases per test; the matrix covers setup, migration up/down,
  suppression policy paths, search/planner/facets, export, fixture
  round-trip, collect/redrive).
- Real-binary E2E on both majors: hardened setup → doctor (6 PASS,
  0 FAIL) → fixtures → search/facets/export → suppress add/re-add/
  list/remove → `gseer host` (table + JSON) → authorized DNS collection
  → server start (audit under `gseer_app`) → health → graceful stop.
- Negative security tests (real binary): revoked grant → startup
  refused exit 5 with the GSUPF failure; decoy `INSERT` grant on
  `public.suppressions` → caught twice (ACL walk + point check), exit 5;
  operator DSN in `runtime_dsn` → `E_RUNTIME_CREDENTIAL_MISMATCH`,
  exit 5.
- Reproducible build: two fresh-clone builds produce byte-identical
  binaries (`-trimpath`, same toolchain).

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
