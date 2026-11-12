# GSEER v0.3.1 — Install Path & First-Run Correctness

> GSEER — internet-asset search engine
> Search internet assets. Collect authorized data. Store locally. Search easily.

> **PENDING RELEASE.**  This document ships with the v0.3.1 source tree,
> but the release is NOT tagged and NOT published yet.  The owner tags
> the commit and publishes; nothing here claims otherwise.

v0.3.1 is a **correctness release**: real-world acceptance testing on
Windows found two failures that POSIX CI could not see, the documented
install command had been broken since the v0.3.0 artifact, and the
first engine command on a fresh machine could dead-end in an unrelated
configuration error.  No check was weakened, no security boundary
moved, and no behavior was faked.

## Fixed

- **Windows CI correctness, fixed against the real Windows behavior.**
  Two windows-latest failures, both invisible on POSIX:
  (1) on the injected close-failure path, the restore's `copyFile`
  removed the partial staging file while its handle was still open —
  Windows refuses to delete an open file — so a leftover
  `gseer.db.restore-staging` survived every simulated close failure
  and even broke the test framework's temp-directory cleanup.  The
  cleanup now releases the real handle first, removes the file only
  afterwards, and reports a removal that still fails next to the
  primary error instead of silently discarding it.  (2) the
  install-path guard compared go.mod's first line byte-for-byte; a
  Windows clone (`core.autocrlf`) ends every text line with CR, so the
  guard rejected the CORRECT lowercase declaration while printing two
  identical-looking lines.  Only the CR artifact is stripped now — a
  BOM, wrong case, or any other deviation still fails the guard
  (the stale UPPERCASE declaration is still caught under CRLF).
- **The exact documented install command now matches the module.**
  `go.mod` declared the module path with an UPPERCASE repo segment
  while every README/INSTALL/QUICKSTART command teaches the lowercase
  form — and Go module paths are case-sensitive, so
  `go install -v github.com/Lutfifakee-Project/gseer/cmd/gseer@latest`
  failed with "module declares its path as … but was required as …".
  The module declaration, all imports, the ldflags `-X` injections,
  and the doc URLs now use the one lowercase path — pinned by guards
  that keep `go.mod`, the imports, and the docs on the lowercase
  public path forever.
- **Zero-configuration first run covers every engine command.**  With
  no configuration anywhere, the first `gseer search`, `gseer source
  add`, `gseer collect`, `gseer suppress`, `gseer export`, or
  `gseer db backup` failed with `E_CONFIG_REQUIRED: client.endpoint`
  — presented to users exactly like "source/collect are rejected in
  personal mode".  Engine commands now run the SAME personal-mode
  initialization bare `gseer` uses (one code path, no drift) when
  nothing is configured; an explicit `--profile`/env choice is never
  overridden.  `search --validate`/`--explain` need no configuration
  and provision nothing; `doctor` on a fresh home reports the
  zero-config state read-only.

## Added

- **The operator's DNS seed is a first-class searchable domain asset.**
  Collecting `--seeds example.com` records `example.com` itself as a
  `domain` asset (confidence `high`, evidence marked `seed: {mode: …}`)
  so `gseer search 'asset_value=="example.com"'` finds it.  The honesty
  contract is kept: the seed fact asserts only that the connector
  TARGETED the name — it carries no DNS record type, never claims a
  record answer the resolver did not return, and is counted neither as
  an answer nor as an error.  Asset identity stays
  `(kind="domain", value)` after canonicalization, so re-collecting the
  same seed from several sources merges into ONE asset with unioned
  source provenance instead of duplicates.
- **`db restore` hardened into an explicitly recoverable replacement**
  — staging file → fsync → close with its error checked → forced
  `0600` → real SQLite validation → sidecar-aware move-aside → rename,
  with reverse-order rollback and an archived predecessor.  User-facing
  text says "recoverable replacement", never "atomic".  Pinned by a
  deterministic failure-injection battery through I/O seams plus
  Windows-specific tests for the replacement primitive.
- **The v0.3.1 acceptance battery is tracked in the repository**
  (`scripts/v031-acceptance.sh`): it drives the real compiled binary in
  an isolated temporary home (never the operator's data), fails fast
  with explicit `[PASS]`/`[FAIL]` lines, counts only independently
  executed assertions, and covers restore-failure recovery alongside
  the core first-run/search/backup surface.  Synthetic fixture checks
  and live-DNS checks are labeled separately.
- DNS/ip2asn regression hardening: nil and empty `net.MX`/`net.NS`
  answer entries, whitespace-only and malformed hostnames,
  trailing-root-dot normalization, multi-line origin answers (first
  line wins), and registry-only AS-name answers are pinned by tests.

## Documentation consistency (this release cycle)

- The product principles line reads exactly as the official identity:
  **Easy to Use · Fully Self-Hosted · Local-First · Search ≠ Scan ·
  Transparent Data Collection.**
- The v0.3.0 brand policy is re-established: external data providers
  are named ONLY in `docs/SOURCES.md`, the data-source review
  register — the one official provider register.  Provider names that
  had drifted into the README connector table, the CLI terms-review
  help, and connector verdict metadata are genericized there again;
  rejected source categories in the register are explained by rejection
  reason, not by vendor name.  Real provider identity, collection
  method, provenance, terms, restrictions, freshness, and limitations
  remain fully documented in the register.
  Historical records — the shipped `CHANGELOG.md` sections and the
  earlier release notes under `docs/releases/` — keep the provider
  mentions they shipped with (they are records of their time, not
  current positioning); code comments name the providers generically,
  while the actual endpoints, wire-format fixtures and connector
  behavior are untouched for correctness.
- The restore example no longer says "atomic" (consistent with the
  recoverable-replacement wording everywhere else); stale
  v0.1.0/v0.2.0 version stamps and examples are updated to v0.3.1; and
  every installation instruction teaches the same
  `go install -v github.com/Lutfifakee-Project/gseer/cmd/gseer@latest`
  command.

## Upgrade notes

- From v0.3.0: nothing to migrate by hand.  Personal databases keep
  their schema (head v1); PostgreSQL deployments stay at schema v17.
- The module-path fix matters to anyone who installs from source:
  the lowercase path is the one that resolves — use the documented
  `go install` command verbatim.

## Verification

- Linux: full suite green (`go test -count=1 ./...`), `-race` clean,
  `go vet` clean, `golangci-lint` 0 issues, `govulncheck` clean.
- PostgreSQL 16 fail-hard battery green.
- The tracked acceptance battery green against the real binary in an
  isolated home.
- Windows: the two CI failures are fixed and the full
  `go test -count=1 ./...` suite passes on windows-latest (CI run
  43153349926).
- Cross-compilation of all release targets succeeds.
