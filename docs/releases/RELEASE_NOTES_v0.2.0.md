# GSEER — Release Notes v0.2.0

**Hardened Self-Hosted Readiness.** v0.2.0 turns the owner-approved
hardened deployment contract (OD-1…OD-7, Revision 6.7.2) into the
shipped binary: the fail-closed privilege audit gates `gseer server
start` before the port binds, the server serves traffic only under the
least-privilege `gseer_app` runtime credential, and the whole hardened
deployment order is one command.  It also ships the GAP-1 safe-cast
migration, the read-only `gseer host` inspection command, and the
release-quality round (PostgreSQL 16 + 17 matrices, fresh-install and
fresh-clone verification, real-binary E2E with negative security
tests).

## Highlights

- **Fail-closed privilege audit, in-binary (OD-5).** `gseer server
  start` runs the embedded effective-privilege audit under `gseer_app`
  before binding: missing grants, unsafe role attributes, ownership
  violations, and escalation paths in the full membership closure
  refuse startup with the complete failure list (exit code 5).  No
  flag, env var, or config key can skip it.
- **Runtime credential switch (M-B).** Operator (`GSEER_DB_DSN`) vs
  runtime (`GSEER_DB_RUNTIME_DSN`) credentials are distinct; the
  runtime identity is `gseer_app`, provisioned and least-privileged by
  the pinned Rev 6.7.2 grant matrix.  The server refuses to serve as
  anything else (`E_RUNTIME_CREDENTIAL_MISMATCH`, exit 5).  CLI
  data-plane commands prefer the runtime credential and say so when
  they fall back.
- **Migration 0016 (pinned) + 0017 (safe-cast, GAP-1).** The
  `gseer_policy` enforcement layer with SECURITY DEFINER policy
  functions (all suppression writes; direct DML refused with `GSUP2`),
  and search-safe casts so malformed evidence can no longer break
  search/facets/export.  Versions 0007–0015 are permanently reserved
  (OD-4).  All artifacts are embedded byte-for-byte and hash-enforced
  by the test suite.
- **Hardened setup in one command.** `gseer setup --self-hosted` runs
  bootstrap → migrate → runtime grants → privilege audit, provisions
  the `gseer_app` password (printed once; `--runtime-password-env` for
  deterministic/compose provisioning; `--reset-runtime-password` to
  rotate), and refuses the deployment on any audit failure.
- **`gseer host <ip|hostname|domain>`** — read-only host inspection
  (asset + services + latest HTTP/TLS/fingerprints + provenance;
  `-o json`).
- **PostgreSQL 16+ minimum (OD-7).** Enforced by setup and doctor;
  verified end-to-end on 16.15 and 17.2.
- **Source-provenance fix.** Collected assets now carry the source name
  (`sources` facet, `gseer host`) — previously only fixture rows did.

## Compatibility changes (read before upgrading)

- **PostgreSQL 16+ is required** (was 15+). Upgrade the cluster first
  (`docs/UPDATE.md` §8).
- **The server requires the runtime credential.** A v0.2.0
  `gseer server start` without `GSEER_DB_RUNTIME_DSN` refuses with
  `E_RUNTIME_CREDENTIAL_REQUIRED` (exit 3); with a DSN that is not
  `gseer_app`, it refuses with `E_RUNTIME_CREDENTIAL_MISMATCH`
  (exit 5).  Run `gseer setup --self-hosted` once to provision.
- **Migrations moved out of `server start`** into `gseer setup
  --self-hosted` (the operator credential's job — the runtime
  credential is denied the migration ledger by design).  The audit
  verifies the schema before any traffic.
- **Exit code 5 now also covers security refusals** (failed privilege
  audit, runtime-credential identity mismatch) in addition to the
  client-profile 401 case — see the exit-code table in
  `docs/DEPLOYMENT.md` §3.4.
- **Rollback** = previous binary + restored backup; the v0.1.0 binary
  keeps working on the unchanged `GSEER_DB_*` configuration.

## Supported PostgreSQL versions

| Version | Status |
| --- | --- |
| 17.x | Supported — verified end-to-end (17.2) |
| 16.x | Supported — verified end-to-end (16.15); minimum (OD-7) |
| 15.x | **Not supported** for v0.2.0 (the v0.1.0 binary remains the 15-compatible release) |

## Release artifacts

v0.2.0 is a **source-only self-hosted release**: the source archive
`GSEER-v0.2.0-source.zip` + its SHA-256 sidecar.  No prebuilt binaries,
no published image, no hosted update channel (`docs/INSTALL.md`).  The
build is reproducible: two fresh-clone builds with the same toolchain
and flags produce byte-identical binaries.

## Verification evidence (this release)

- Unit suites: 19 packages green.  Integration suites (`-tags
  integration`): green on PostgreSQL 16.15 and 17.2 — hardened setup,
  migration up/down round-trips, suppression policy paths, the v0.1.0 →
  v0.2.0 upgrade path, search/planner/facets, export, fixture
  round-trip, collect/redrive.
- Real-binary E2E on both majors: hardened setup → doctor (6 PASS,
  0 FAIL) → fixtures → search/facets/export → suppress (policy
  functions) → `gseer host` → authorized DNS collection → server start
  (audit under `gseer_app`) → health → graceful stop.
- Negative security tests: revoked grant → exit 5 with the GSUPF list;
  decoy DML grant on `public.suppressions` → caught twice, exit 5;
  operator DSN in `runtime_dsn` → exit 5.

## Known limitations (honest)

- Self-hosted index only — no global dataset, no hosted search service,
  no equivalence with any hosted global search engine of any kind.  You
  search exactly what your own reviewed sources collected.
- The client-profile CLI cannot present the API's Bearer token (use
  `curl -H "Authorization: Bearer …"`).
- Passive DNS facts are asset-level: resolved IPs are searchable with
  source provenance, but the resolving name is on the observation's
  evidence, not mirrored into the asset's hostname/domain columns.
- RDAP and crt.sh/CT connectors stay disabled pending provider terms
  (enforced in code — `docs/SOURCES.md`).
- Suppression IDENTITY sequence gaps are cosmetic (0016's in-migration
  refusal probes consume values; register R7).
- No prebuilt binaries and no hosted update channel yet (binary
  distribution is the EU-3 roadmap item).

## Documentation

`docs/QUICKSTART.md` (canonical verified workflow), `docs/INSTALL.md`,
`docs/DEPLOYMENT.md` (credential model + hardened startup + audit),
`docs/DOCKER.md` (hardened full stack), `docs/UPDATE.md` (§8: the
v0.1.0 → v0.2.0 upgrade runbook), `docs/ROADMAP.md` (release register),
`CHANGELOG.md` (full change list).
