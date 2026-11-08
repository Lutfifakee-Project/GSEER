# GSEER — Deployment, Credentials, and the Hardened Runtime Model

This page is the canonical description of GSEER's deployment and runtime
model. It covers three things operators keep asking about:

1. **Which credentials do what** — operator/database-administration
   credentials vs the runtime application credential, and which `gseer
   doctor` checks need which.
2. **The hardened startup sequence and its fail-closed privilege
   audit** — what is checked, why it runs before any runtime traffic,
   what a refusal means, and how to repair a deployment safely.
3. **The PostgreSQL version policy** — what is required and what has
   actually been verified.

Every claim carries an evidence class:

- **VERIFIED (real binary)** — exercised against the v0.2.0 binary in
  this repository during the release-readiness round (PostgreSQL 16.15
  and 17.2, fresh databases on both, plus a v0.1.0 → v0.2.0 upgrade
  rehearsal).
- **OWNER-APPROVED CONTRACT** — normative decisions OD-1…OD-7 from the
  owner's task book of 2026-11-05, implemented in this release as
  approved. The five Rev 6.7.2 SQL artifacts are embedded byte-for-byte
  and hash-enforced by the test suite (§5).

---

## 1. Which model am I running?

v0.2.0 ships **only** the hardened model — the v0.1.0 single-credential
model was its development baseline and remains runnable by the v0.1.0
binary on an unchanged `GSEER_DB_*` configuration.

| Aspect | v0.2.0 hardened model — VERIFIED |
| --- | --- |
| Database roles | **Three**: the operator credential (whatever the DSN names; database owner / migration executor), `gseer_policy_owner` (NOLOGIN; owns the suppression-policy layer), and `gseer_app` (LOGIN; the **only** credential the server uses at runtime, least-privilege). |
| Schema migration | Embedded migrations 0001–0006 + **0016** (`gseer_policy` enforcement layer; refusal-first) + **0017** (search safe-casts). Versions 0007–0015 are permanently reserved (OD-4) — they do not exist and never will. Applied by `gseer setup --self-hosted` under the operator credential. |
| Pre-runtime privilege audit | **In-binary and mandatory.** `gseer server start` runs the fail-closed effective-privilege audit under `gseer_app` before the port binds; a failure refuses startup (exit 5) with the complete failure list. There is no bypass, flag, or override (OD-5). |
| Runtime credential enforcement | `server start` requires `database.runtime_dsn` / `GSEER_DB_RUNTIME_DSN` **and** verifies the connecting user IS `gseer_app` (`E_RUNTIME_CREDENTIAL_MISMATCH`, exit 5, otherwise). CLI data-plane commands prefer it and print an explicit notice when they fall back to the operator credential — never silently. |
| PostgreSQL floor | **16** (OD-7; enforced by `gseer setup` and `gseer doctor`). Verified end-to-end on 16.15 and 17.2. |

---

## 2. The credential model

### 2.1 Operator vs runtime — VERIFIED

- **Operator credential** — `GSEER_DB_DSN` / `database.dsn` (or the
  DSN components). Runs `gseer setup --self-hosted` (bootstrap,
  migrations, grants, audit), `gseer db migrate status|diagnose|force`,
  and the operator-side doctor checks (reachability, PG version,
  migration ledger, index statistics). In the guided deployments this
  is the database owner (the compose default is its superuser).
- **Runtime credential** — `GSEER_DB_RUNTIME_DSN` /
  `database.runtime_dsn`; the `gseer_app` role. The server serves ALL
  traffic as this identity, and the data-plane CLI commands (search,
  export, collect, suppress, source, host) use it when configured.
- **One API token, separate from the database.** `GSEER_TOKEN`
  protects the REST API; it is hashed at rest and required for any
  non-loopback server bind (`E_CONFIG_INSECURE` — enforced in the
  config layer and again in `server start`). VERIFIED.
- **Rotation.** `gseer setup --self-hosted --reset-runtime-password`
  rotates the `gseer_app` password and prints the new runtime DSN
  once. The bootstrap script itself never contains or generates
  credentials.
- **`gseer doctor` is strictly read-only.** It never migrates and never
  repairs; it reports `PASS / FAIL / HINT` (and `-` for skipped) with a
  fix line per check. VERIFIED: a fresh install reports `6 PASS, 0
  FAIL` with the index honestly reported as `EMPTY` (a HINT, not a
  failure), and a fixture-loaded index is labeled
  `SYNTHETIC EVALUATION DATA` rather than passed off as real data.
- **Honest SKIP.** Under the `client` profile there is no local
  database, and doctor says so explicitly instead of pretending:
  `[ -    ] DB checks: skipped (client profile has no local database)`.
  When the runtime credential is not configured, the privilege-audit
  check is an honest HINT naming the provisioning command — doctor
  never pretends an unprovisioned deployment is hardened.
- **Which checks need which credential.** Every database-side check
  runs under the operator DSN by design; the privilege audit runs under
  the runtime credential when one is configured (proving both the
  deployment state and the credential's own reach — every catalog the
  audit consults is readable by non-superusers). The migration ledger
  (`public.schema_migrations`) is driver-owned and is **not** part of
  the runtime grant set: migration-state diagnostics are operator-
  credential work, and the runtime credential is deliberately denied
  the ledger.
- **Do not widen the runtime credential to make checks pass.** Granting
  a runtime credential read access to the ledger (or any other missing
  privilege) to make a doctor check "pass" would widen the runtime
  boundary and is explicitly not a supported repair.

### 2.2 The `gseer_app` runtime grant matrix — VERIFIED (embedded, hash-pinned)

Rev 6.7.2 §5.2 fixes the full runtime grant matrix; the idempotent
script ships embedded (`deploy/sql/gseer_v0672_runtime_grants_setup.sql`)
and is applied by `gseer setup --self-hosted` (step 3). Summary of the
boundary (the script is the normative source):

| Granted to `gseer_app` | Deliberately NOT granted |
| --- | --- |
| `CONNECT, TEMPORARY` on the database (both explicit, OD-2 Choice A — the retraction path needs `CREATE TEMP TABLE … ON COMMIT DROP`) | Any sequence privilege (IDENTITY inserts use `nextval_internal()`; no ACL check — verified on 17.2 during Rev 6.7.2's run) |
| `USAGE` on schemas `public` and `gseer_policy` | `CREATE` on any schema |
| `SELECT, INSERT, UPDATE, DELETE` on `assets`, `services` | **Any DML on `public.suppressions`** — the runtime path is `SELECT`-only (granted by migration 0016); inserts/updates/deletes go through the `gseer_policy` SECURITY DEFINER functions only |
| `SELECT, INSERT, DELETE` on `observations` (no UPDATE path exists in the code, so none is granted) | `schema_migrations` (driver-owned ledger — operator-credential territory) |
| `SELECT, INSERT, UPDATE` on `data_sources` (no DELETE path exists), `collection_jobs`, `source_checkpoints`, `ingestion_dead_letter` | `scopes` (no runtime path; P2 registry) |
| `SELECT, INSERT` on `suppression_applications` (append-only audit surface) | Ownership of anything in `public` |
| `SELECT` on the search views (`latest_http`, `latest_tls`, `latest_fingerprints`, `search_index`) and `EXECUTE` on the three `gseer_policy` suppression functions | `SUPERUSER` / `CREATEDB` / `CREATEROLE` / `BYPASSRLS` — the bootstrap script refuses to create or keep a role with any of these |

Suppression writes go through the policy functions in every code path
(v0.2.0): `gseer suppress add|remove` call
`gseer_policy.suppression_insert` / `suppression_delete` inside one
transaction that first takes the same advisory lock the 0016 statement
trigger takes (the GSER/POL1 keys, OD-6) — which is what keeps the
re-add detection (`existed`) exact under concurrency. Direct DML on
`public.suppressions` is refused by the policy gate (ERRCODE `GSUP2`)
for every role.

The role bootstrap (`gseer_v0672_bootstrap_roles.sql`) is
refuse-first: if `gseer_policy_owner` or `gseer_app` already exists
with an unsafe attribute set, it fails with an explicit remediation
message instead of "fixing" anything silently. If a pre-existing
`gseer_policy` schema is encountered, migration 0016 refuses rather
than adopting it (OD-3, refusal-first; the only soft path is verified
adoption named by the guard's own error message).

**The least-privilege direction is one-way.** If a check or audit
fails, the repair is to fix the deployment — never to grant the
missing privilege to `gseer_app`. Every "add a grant until it works"
shortcut would defeat the exact boundary the audit exists to prove.

---

## 3. The hardened startup sequence and the fail-closed privilege audit

### 3.1 The deployment order — VERIFIED (automated by `gseer setup --self-hosted`)

1. **Bootstrap the roles** — the embedded bootstrap script
   (creates/validates `gseer_policy_owner` and `gseer_app`; D-47
   membership for the migration executor).
2. **Migrate to head** — migrations end at **0017**; the ledger must
   read `(17, false)`. An incomplete or dirty migration state is *not*
   a validly deployed schema, whatever else is true.
3. **Apply the runtime grants** — the embedded §5.2 matrix
   (idempotent; the database name is substituted when it is not the
   default `gseer` — the only transformation, and it is reported).
4. **Run the effective-privilege audit** — must pass. A failure
   refuses the deployment (exit 5). The audit is read-only; it
   mutates nothing.
5. **Only then** start the server with the **`gseer_app` credential** —
   never with the operator credential (identity-enforced).

`gseer setup --self-hosted` runs steps 1–4 in order and refuses at the
first failure. The bootstrap runs BEFORE the migrations because the
0016 refusal guard verifies the roles and the D-47 membership.

### 3.2 What the audit checks — VERIFIED (embedded, hash-pinned)

The audit evaluates **effective** privileges, not just the grant
tables: it walks the **full membership closure from `gseer_app`** —
every role reachable through `pg_auth_members` at any depth, including
edges regardless of their `INHERIT`/`SET` options (PostgreSQL 16
`SET ROLE` confers everything the granted role can do even when
`INHERIT` is false, so assuming options away would be unsound). The
walk is deliberately false-positive-lean. It fails closed on any of:

- `gseer_app` holding `SUPERUSER`, `CREATEROLE`, `CREATEDB`, or
  `BYPASSRLS` (self-grant escalation on PG 16 is why `CREATEROLE` is
  forbidden even for a non-superuser);
- any **reachable** role (at any depth) holding `SUPERUSER` /
  `BYPASSRLS` / `CREATEROLE`;
- any reachable role with direct `INSERT`/`UPDATE`/`DELETE`/`TRUNCATE`
  on `public.suppressions`, or owning it;
- `gseer_app` owning anything in `public`;
- missing baseline privileges (database `CONNECT`/`TEMPORARY`, schema
  `USAGE`, the §5.2 table grants);
- sequence or ledger privileges that must not exist.

`has_table_privilege()` / `has_function_privilege()` follow the
privileges-of-role closure internally, so the point checks and the
closure walk double-cover each other. Rev 6.7.2's verification run
included three decoy roles that prove each escalation class (INHERIT
path, SET-only path with `INHERIT FALSE`, superuser-membership path)
is caught. The audit is runnable by the operator **or** by `gseer_app`
itself — and `gseer server start` runs it **as `gseer_app`**, so a
passing startup audit simultaneously proves the deployment state and
the runtime credential's own reach.

### 3.3 Why the audit runs before any runtime traffic (OD-5)

OD-5 (owner-approved): *fail-closed privilege audit before application
runtime traffic*. The ordering is the security property: a server that
binds its port first would serve search/ingest traffic with
**unproven** privileges, and an escalation present in the membership
closure would already be reachable by the time anyone looked. Running
the audit between the grant setup and the first request means the
deployment either starts with privileges that were just proven, or
does not start at all.

### 3.4 What a refusal means — and what exit code 5 means

- **In-binary gate (VERIFIED, real binary):** `gseer server start`
  refuses with `E_PRIVILEGE_AUDIT_FAILED`, the complete failure list,
  and **exit code 5** on any audit failure. Reproduced live: revoking
  `SELECT` on `public.assets` from `gseer_app` makes the next start
  print `gseer_app lacks SELECT on public.assets` and exit 5; granting
  `INSERT` on `public.suppressions` to `gseer_app` is caught twice
  (ACL walk + point check) and exits 5. There is no flag, env var, or
  config key that skips the gate.
- **Exit code 5 = security/privilege refusal.** The stable table:

| Code | Meaning (v0.2.0 binary) |
| --- | --- |
| 0 | Success (including a zero-result search) |
| 2 | Invalid query / invalid input (`E_QUERY_*`) |
| 3 | Configuration failure (`E_CONFIG*`, non-loopback bind without token, missing runtime credential) |
| 4 | Service unavailable (endpoint/DB/index; audit could not run) |
| 5 | Security/privilege refusal: failed privilege audit, runtime-credential identity mismatch — and (client profile) a 401 from a token-protected API |
| 64 | Usage error / planned-command pointer (`gseer scan`, `gseer asset`) |
| 70 | Internal error |

- **Common refusals and the safe repair:**

| Refusal | Root cause | Safe repair |
| --- | --- | --- |
| Audit: `role gseer_app does not exist` | Step 1 (bootstrap) not run, or run against the wrong cluster/database | Re-run `gseer setup --self-hosted` with the operator credential on the correct database |
| Audit: unsafe role attributes | Someone re-enabled `SUPERUSER`/`CREATEROLE`/… on a managed role | Remediate the role attributes manually (`ALTER ROLE … NOSUPERUSER …` per spec), then re-run setup |
| Audit: reachable role with `suppressions` DML | A stray grant or membership edge (direct, INHERIT, or SET-only) | **Revoke the grant/edge** (operator credential), re-run the audit. Never solve this by hiding it from the audit |
| Audit: `gseer_app` owns an object in `public` | Objects created while logged in as the runtime role | Reassign ownership to the proper owner (operator credential), re-run audit |
| Audit: missing table/view grants | Grants not applied (step 3 skipped) or a revoked privilege | Re-run `gseer setup --self-hosted` (idempotent, additive-only) |
| `E_RUNTIME_CREDENTIAL_REQUIRED` (exit 3) | `database.runtime_dsn` / `GSEER_DB_RUNTIME_DSN` not configured | `gseer setup --self-hosted` prints the runtime DSN once; export it |
| `E_RUNTIME_CREDENTIAL_MISMATCH` (exit 5) | The runtime DSN connects as something that is not `gseer_app` (e.g. the operator credential pasted in) | Point `database.runtime_dsn` at the `gseer_app` credential |
| Migration ledger dirty / not at head | A migration failed mid-run | `gseer db migrate status` → `gseer db migrate diagnose` (read-only); repair only per its guidance |
| `E_CONFIG_INSECURE` | Non-loopback `server.listen` without an API token | Set `server.token` / `GSEER_TOKEN`, or keep the loopback default |

### 3.5 Why there is no bypass

The audit's authority comes from the deployment contract, not from a
runtime flag: OD-5 makes a passed audit a precondition of runtime
traffic, the audit is read-only (it cannot be "passed" by mutating
it), the role attributes it checks cannot be self-granted by
`gseer_app` (bootstrap enforces `NOSUPERUSER NOCREATEDB NOCREATEROLE
NOBYPASSRLS`, and the closure walk catches any reachable role that
would launder privileges), and the enforcement direction is
refusal-first everywhere: migration 0016 refuses an unexpected
pre-existing `gseer_policy` schema (OD-3) instead of adopting it, the
bootstrap refuses unsafe role attributes instead of repairing them
silently, the grant script is additive-only — it never revokes,
widens, or "fixes" an existing state to make an audit pass — and the
runtime-credential identity check refuses to serve traffic as anyone
other than `gseer_app`. A deployment that fails the audit is repaired
by fixing the deployment, never by re-interpreting the check.

---

## 4. Startup gates in the v0.2.0 binary — VERIFIED

The gates `gseer server start` applies, in order:

1. Config loads and validates (self-hosted profile required —
   `E_CLIENT_ONLY` otherwise; exit 3).
2. Non-loopback bind requires an API token — `E_CONFIG_INSECURE`
   (exit 3); enforced in the config layer as well.
3. The runtime credential is configured (exit 3 otherwise) and the
   pool opens with it.
4. **Runtime-credential identity check**: the connecting user must BE
   `gseer_app` (`E_RUNTIME_CREDENTIAL_MISMATCH`, exit 5) — a superuser
   or operator DSN in `runtime_dsn` is refused exactly like a failed
   audit.
5. **The fail-closed effective-privilege audit runs under `gseer_app`
   before the listener exists.** A failure prints the complete GSUPF
   list and exits 5 — the port is never bound on a failed audit.
6. Trusted-proxy configuration is parsed strictly (an invalid CIDR is
   a config error, never silently ignored).
7. `net.Listen` on the configured address (loopback `127.0.0.1:8443`
   by default), pidfile written `0600`, index honestly reported
   (`EMPTY` / `SYNTHETIC EVALUATION DATA` / counts), graceful shutdown
   on SIGINT/SIGTERM, `gseer server stop` verifies process identity
   before signalling.

Migrations are deliberately NOT among the server's steps: they are
operator work (`gseer setup --self-hosted`), and the runtime credential
is denied the ledger so it can never run them. An unaudited/incomplete
schema cannot pass gate 5 (the audit names every missing object).

---

## 5. Artifact register (hash-verified, embedded)

The five Rev 6.7.2 companion artifacts are embedded byte-for-byte
(migrations 0016 up/down in `internal/storage/migrations/`; the three
deployment scripts in `deploy/sql/`, embedded via `deploy/deploy.go`)
and their SHA-256 hashes are enforced by
`internal/storage/migrations/artifacts_test.go` — a byte drift fails
the build's test suite. The 0017 pair is hash-pinned the same way.

| Artifact | SHA-256 | Role in the sequence |
| --- | --- | --- |
| `0016_suppression_policy_hardening.up.sql` | `4871884f2a627711d54dad71e2cd4c2a9b26cd4ffbcca6584e6580db8ae1b99c` | Pinned migration 0016 — `gseer_policy` enforcement layer, single-owner ACL provenance, refusal-first pre-existing-schema guard |
| `0016_suppression_policy_hardening.down.sql` | `096bf7b744d63d818381f3a0a0c46550dc232d4361f8249e68820beefbc3ab24` | Ordered rollback for 0016 |
| `gseer_v0672_bootstrap_roles.sql` | `ff31bdeadb4ffbf97600786248a2fb2d09919e635e1d3a2fd9dcaa4bd1e9e2f4` | Step 1 — role bootstrap (refuse-first) |
| `gseer_v0672_runtime_grants_setup.sql` | `fd816c8ca8b6e86d1517d58971f32b19ebe903fe79841396b5eafdcc7f1ad979` | Step 3 — the §5.2 grant matrix |
| `gseer_v0672_effective_privilege_audit.sql` | `d11a4167c4b1905072c74f2f5d7234ee8010dc22245198c7f5a9435ddf84dffe` | Step 4 — the fail-closed audit (read-only) |
| `0017_search_safe_casts.up.sql` | `723e2c786d62515b85248c19b15b29ec0a27ffafef79a79eb2ace4cd265c867d` | GAP-1 safe-cast migration (pinned at M-H) |
| `0017_search_safe_casts.down.sql` | `1074545a68e48e324b1816117c40664d1e9e453ffcd1e154d9e0c074f40711b4` | Ordered rollback for 0017 (ledger returns to `(16, false)`) |

The same files stay runnable by hand with `psql` for deployments that
manage the steps outside the CLI; mixing hand-applied steps with CLI
provisioning is safe (everything is idempotent) but unnecessary.

---

## 6. PostgreSQL version policy

- **v0.2.0 floor: PostgreSQL 16** (OD-7, owner-approved). `gseer
  setup` and `gseer doctor` enforce and report it
  (`PG version: PostgreSQL N (minimum 16)`); the pinned migrations
  carry their own PG16 floor guards. The Rev 6.7.2 privilege model
  (membership `SET`/`INHERIT` options, the closure-walking audit) is
  only expressible on PG16+.
- **Verified (release-readiness round, real binary + full integration
  suite): PostgreSQL 16.15 and 17.2** — hardened setup → doctor
  (`6 PASS, 0 FAIL`) → fixtures → search/facets/export/host →
  suppress (policy functions) → server start (audit under
  `gseer_app`) → health → stop all pass on both; the
  `-tags integration` suites pass on both; the v0.1.0 → v0.2.0
  upgrade path (ledger `(6, false)` → `(17, false)`) is an automated
  test.
- **v0.1.0 (historical):** floor was PostgreSQL 15 (PD-8), retained by
  the v0.1.0 binary.

---

## 7. Known limitations (honest)

- The `gseer` client-profile CLI cannot present the REST API's Bearer
  token (no token flag in the client path) — query a token-protected
  server with `curl -H "Authorization: Bearer …"` or from inside the
  compose network.
- Passive DNS discovery lands asset-level rows: the resolved IPs are
  searchable and carry source provenance (fixed in v0.2.0), but the
  connecting hostname/domain is recorded on the observation's
  evidence, not mirrored into the asset's hostname/domain columns —
  `gseer host <name>` therefore resolves name-kind assets, not the
  IPs a name resolved to. Search by the name itself (`domain
  IN_DOMAIN …`) to find name assets; the ip assets surface via
  `sources`/facets.
- Suppression IDENTITY sequence gaps are cosmetic (migration 0016's
  in-migration refusal probes consume sequence values; register R7).
- v0.2.0 is a source-only release: no prebuilt binaries, no published
  image, no hosted update manifest, no hosted/global dataset, and no
  public search service (docs/ROADMAP.md). `gseer update` verifies
  Ed25519-signed manifests, but no hosted channel exists yet — the
  updater stays honest about an unconfigured/unreachable channel.
- Binary distribution (reproducible release channel with published
  checksums) is the EU-3 roadmap item; `make build` is reproducible
  today (two fresh-clone builds produce identical hashes when built
  with the same toolchain and flags), but nothing prebuilt is
  published.
