# GSEER — Release Notes v0.2.1

**Compatibility, Personal Mode, Release Engineering.** v0.2.1 is a
compatible increment over v0.2.0: the search query surface accepts the
operator-style syntax self-hosters already type, a zero-dependency
personal mode on SQLite lands next to the hardened PostgreSQL path, and
the release-engineering gap closes (validated release config, `go
install` support, draft-gated release workflow).  No security boundary
is weakened, no PostgreSQL schema changes ship, and v0.2.0
installations upgrade in place.

## Highlights

- **GQL compatibility layer.** The parser, validator, and planner now
  accept the query forms operators know from hosted asset-search
  engines: `*=` suffix-anchored fuzzy match, bare-term free text,
  `after`/`before` rewrites, derived booleans, empty-value semantics
  (`http.title=""` matches *missing*, not literal-empty), quoted-integer
  coercion (`port="443"`), and field aliases (`org`, `app`,
  `base_protocol`, `status_code`, `cert.subject.cn`).  The `host` alias
  matches hostname **or** IP in one predicate — implemented with
  `host(inet)` so single-host `/32` prefixes match correctly under both
  exact equality and suffix wildcards.  Full grammar and examples:
  `docs/SEARCH-SYNTAX.md`.
- **Personal mode on SQLite — no PostgreSQL required.**
  `gseer setup --personal` initializes a single database file (default
  `~/.gseer/gseer.db`, override with `--db-path`) for single-operator
  use on a laptop or VPS.  The store implements the full SQ-01…SQ-06
  remediation set: upsert FirstSeen consistency, millisecond timestamp
  precision with migration compatibility, DSN special-character safety,
  restrictive file permissions (0600 file / 0700 directory policy, with
  an explicit strategy for pre-existing files), single-writer WAL
  concurrency with `busy_timeout`, and deny-wins suppression-race
  handling (atomic transactions, audit rows, fail-closed expiry).
  Search runs through a dedicated SQLite planner with parity tests
  against the PostgreSQL planner; `gseer db backup` and
  `gseer db restore` provide consistent online snapshots.
- **Release engineering (REL-01/02/03).** The GoReleaser configuration
  is valid YAML validated against GoReleaser v2.20.0 (the pinned
  workflow version); a full snapshot build of every configured
  os/arch pair passed locally, and CI now validates the release config
  on every push so an invalid config can never merge again.  Releases
  are produced as **drafts** — nothing publishes without a maintainer
  reviewing and pressing publish.
- **`go install` support (REL-01).** The module-root main package is a
  pure delegate to `cmd/gseer`, covered by entrypoint-equivalence
  tests, so both install paths work:
  `go install github.com/Lutfifakee-Project/gseer/cmd/gseer@v0.2.1` or
  `make build VERSION=v0.2.1`.
- **Documentation and hygiene.** Engine references outside
  `docs/SOURCES.md` are neutral (brand policy); `.gitignore` gains a
  safety net for personal-mode SQLite artifacts so a runtime database
  can never be committed by accident.  The repository layout is also
  tidied: release notes live in `docs/releases/`, the banner in
  `docs/assets/`, and internal engineering evidence
  (architecture-spec revisions, CI integration report) stays
  local-only — the root keeps only standard Go project files.

## Compatibility changes (read before upgrading)

- **None breaking.** PostgreSQL 16+ remains the requirement for the
  self-hosted server profile (OD-7); migrations 0016/0017 remain the
  newest, so `gseer db migrate` is a no-op for current installs.  The
  SQLite store is additive and profile-gated (`GSEER_PROFILE=personal`)
  — self-hosted deployments are untouched.
- **GQL grammar is additive.** Queries that worked on v0.2.0 keep their
  semantics; `=` stays exact and fuzzy matching remains opt-in via
  `*=`.  New aliases and coercions only widen what is accepted.

## Honest limitations

- Personal mode is **single-operator, single-node** tooling: no
  least-privilege role model, no fail-closed privilege audit, no
  network serving — it complements, and does not replace, the hardened
  PostgreSQL deployment.
- The embedded SQLite engine does not provide the suppression-policy
  SQL enforcement layer of migration 0016; suppression in personal mode
  is enforced in-process (deny-wins, audited) instead.
- Release binaries appear as platform archives **after** a maintainer
  publishes the draft.  Verify every download against `checksums.txt`
  (SHA-256) and compare with `gseer doctor`, which prints the running
  binary's SHA-256.

## Install & verify

```bash
# From source (Go 1.26+)
git clone https://github.com/Lutfifakee-Project/gseer.git
cd GSEER
make build VERSION=v0.2.1
./bin/gseer version        # → gseer v0.2.1 (<commit>)
./bin/gseer doctor         # prints the binary's SHA-256

# Or without cloning
go install github.com/Lutfifakee-Project/gseer/cmd/gseer@v0.2.1
```

Self-hosted server path: `gseer setup --self-hosted --with-compose`
(unchanged from v0.2.0).  Personal path: `gseer setup --personal`.
