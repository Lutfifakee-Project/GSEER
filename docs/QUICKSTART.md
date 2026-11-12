# GSEER — Quickstart (v0.3.1)

> Canonical fresh-install workflow.  Every command below was executed
> against the real binary during the release-readiness rounds.  Two
> paths:
>
> - **Path A — personal mode (default, zero-config):** one embedded
>   SQLite file; no database server, no Docker, no account.
> - **Path B — self-hosted:** PostgreSQL 16+ with the hardened
>   deployment order.  Credential model and the fail-closed privilege
>   audit: `docs/DEPLOYMENT.md`.

## Path A — personal mode (zero-config)

Install once, then run:

> The first ENGINE command you run — bare `gseer`, `gseer search …`,
> `gseer source add …` — initializes personal mode the same way when no
> configuration exists yet.  Nothing to provision by hand.

```bash
go install -v github.com/Lutfifakee-Project/gseer/cmd/gseer@latest
gseer
#   Welcome to GSEER — internet-asset search engine.
#   [ ok  ] personal database: ~/.gseer/gseer.db      (one SQLite file, 0600)
#   [ ok  ] migrations: schema at head v1 (AT_HEAD)
#   [ ok  ] new database file created with 0600 permissions
#   [ ok  ] config written: ~/.gseer/config.yaml (profile=personal)
gseer doctor
#   4 PASS, 0 FAIL — index reported EMPTY (that is honest)

# Evaluation data (labeled SYNTHETIC everywhere; QS-6 consent gate):
gseer setup --load-fixtures
gseer search 'port==443 && title CONTAINS "login"'

# Real data — your own collection (nothing runs uninvoked):
gseer source add my-dns --type dns --seed-mode config \
    --seeds example.net,example.org --enable --yes
gseer collect run --source my-dns

# The seeds themselves are assets too — searchable by exact value:
gseer search 'asset_value=="example.net"'
gseer search 'asset_kind=="domain"'

# Backup / restore (one file — copy it anywhere):
gseer db backup --out backup.db
gseer db restore backup.db
```

That is the whole personal workflow: the binary, one SQLite file, and
your collection.  The same `source`/`collect`/`suppress`/`export`
surface as self-hosted works against the single file.

### What `db restore` actually guarantees

`gseer db restore` is a **recoverable replacement**, deliberately not
marketed as "atomic".  What it does, in order:

1. **Stages** a full copy of the backup in a fresh temporary file
   inside the live database's own directory, fsyncs it, and **validates
   it** with a real SQLite open plus an integrity check — your live
   database is not touched during any of this.
2. **Moves the live database aside** — together with its `-wal`/`-shm`
   sidecars, so a stale WAL can never be recovered against the new
   file — to `gseer.db.restore-old` in the same directory.  A leftover
   `.restore-old` from an earlier restore is never deleted: it is
   archived to a timestamped name and the move is printed.
3. **Renames the staged copy onto the live path** (same-directory
   rename: atomic per step on Linux/macOS; on Windows the replacement
   uses `MoveFileExW` with `MOVEFILE_REPLACE_EXISTING` and
   `MOVEFILE_WRITE_THROUGH`).  For the brief moment between the two
   renames the live path does not exist — a crash there loses nothing,
   because the previous database is intact at `gseer.db.restore-old`.

If any step fails, the command exits non-zero, the renames already
performed are rolled back, and the live database is left byte-for-byte
as it was; a rollback that itself fails is **reported explicitly** (the
message names where every file is — follow its instructions before
running GSEER again).  After a successful restore the previous database
stays at `gseer.db.restore-old` until YOU remove it, so the recovery
for a wrong-file restore is:

```
mv ~/.gseer/gseer.db.restore-old ~/.gseer/gseer.db
```

Platform notes: restore while no other GSEER process is using the
database.  On Linux/macOS a concurrently running GSEER keeps working
through the swap (its handles follow the old file); on Windows an open
database or WAL sidecar makes the rename fail and the restore aborts
cleanly with everything back in place.  Windows has no POSIX mode
bits — the restored file's ACL follows the parent directory (which
`setup --personal` created as a private directory); on Linux/macOS the
restored database is always `0600`.

### Where collection targets come from — `seed-mode` and `seed-kind`

- `--seed-mode config` (default): the queries target the seeds YOU type
  in `--seeds` (domains for `dns`, addresses for `ip2asn`/`ptr`).
- `--seed-mode index`: the queries target identifiers ALREADY in your
  index — e.g. re-resolve every domain you have collected, or enrich
  every IP.  Nothing is fetched from outside your own index.
- `--seed-kind` applies ONLY in `seed-mode index`: it selects the asset
  kind the seeds are drawn from (`ip` for `ip2asn`/`ptr`, `domain` for
  `dns`).  It does not re-classify or reinterpret the seeds themselves,
  and it has no effect in `seed-mode config`.

```bash
# example: enrich every IP already in the index with ASN data
gseer source add my-asn --type ip2asn --seed-mode index --seed-kind ip --enable --yes
```

---

## Path B — self-hosted (PostgreSQL 16+)

## 0. Prerequisites

| Component | Requirement |
| --- | --- |
| Go | 1.26+ (only for building from source — `go.mod` requires 1.26) |
| PostgreSQL | **16+ (OD-7, enforced by `gseer setup` and `gseer doctor`); verified end-to-end on 16.15 and 17.2** |
| Docker + Compose | easiest way to run PostgreSQL (`postgres:17-alpine` template) — or use any PostgreSQL 16+ you already run |
| OS | Linux / macOS / Windows |

No global dataset, no hosted service: a fresh install has an **empty**
index and searches exactly what you collect into it.

## 1. Build

```bash
git clone https://github.com/Lutfifakee-Project/gseer.git
cd gseer
make build VERSION=v0.3.1
./bin/gseer version
#   gseer v0.3.1
# (optional: sudo cp bin/gseer /usr/local/bin/ so `gseer` is on PATH)
```

## 2. Provision PostgreSQL

Easiest — the generated compose file (loopback-only, healthcheck-gated):

```bash
gseer setup --self-hosted --with-compose   # writes docker-compose.yml + .env (random password, chmod 600)
docker compose up -d db                    # DB now running (healthcheck-gated)
docker compose ps db                       # expect: healthy
```

Already running your own PostgreSQL 16+? Skip to step 3 and pass its
DSN (the credential you pass here is the **operator** credential —
database owner / migration executor; `docs/DEPLOYMENT.md` §2).

## 3. Hardened setup: bootstrap → migrate → grants → audit (one command)

```bash
gseer setup --self-hosted --db-url "$(grep '^GSEER_DSN=' .env | cut -d= -f2-)"
#   [ ok  ] DB reachable at postgres://127.0.0.1:5432/gseer?sslmode=disable
#   [ ok  ] PG version: 17 (minimum 16, OD-7)
#   [boot] bootstrap: created role gseer_policy_owner (NOLOGIN)
#   [boot] bootstrap: created role gseer_app (LOGIN; password injected by the deployment procedure)
#   [boot] bootstrap: granted gseer_policy_owner membership to gseer (D-47, SET TRUE INHERIT TRUE)
#   [ ok  ] migrations: 8 applied, schema at head v17
#   [ ok  ] runtime credential: password set for gseer_app (rotate anytime: …)
#   [audt] EFFECTIVE-PRIVILEGE AUDIT PASSED: …
#   [ ok  ] privilege audit: PASSED (fail-closed gate, OD-5)
```

All embedded migrations (0001–0006 + 0016 + 0017; versions 0007–0015
are permanently reserved) apply automatically — zero manual SQL.  The
command also runs the hardened v0.2.0 deployment order: role bootstrap
(step 1), migrations (step 2), the least-privilege `gseer_app` runtime
grant matrix (step 3), and the fail-closed effective-privilege audit
(step 4).  A failed audit **refuses the deployment** (exit 5) and
names every failure — never "fix" it by granting `gseer_app` more
privileges (`docs/DEPLOYMENT.md` §3.5).

## 4. Store the runtime credential (one-time)

`setup` prints the runtime DSN exactly once — store it now:

```bash
export GSEER_PROFILE=self-hosted
export GSEER_DB_DSN="$(grep '^GSEER_DSN=' .env | cut -d= -f2-)"          # operator credential
export GSEER_DB_RUNTIME_DSN='postgres://gseer_app:<password>@127.0.0.1:5432/gseer?sslmode=disable'
```

The **operator credential** (`GSEER_DB_DSN`) runs setup/migrations and
operator-side diagnostics.  The **runtime credential**
(`GSEER_DB_RUNTIME_DSN`, role `gseer_app`) is the least-privilege
identity the server and the data-plane commands use.  `gseer server
start` refuses to run under the operator credential — that is the
OD-5 contract, and there is no bypass.  Lost the runtime password?
`gseer setup --self-hosted --reset-runtime-password` rotates it and
prints a new DSN.

Without `GSEER_PROFILE`/`GSEER_DB_DSN` the CLI defaults to the
`client` profile and fails with
`E_CONFIG_REQUIRED: required field "client.endpoint" is missing …`
(exit 3) — that error is the config layer telling you the profile is
still `client`, not a broken install.

## 5. Initial doctor checks

```bash
gseer doctor
#   [ PASS ] config validity: loaded env-only (profile=self-hosted)
#   [ PASS ] binary: version v0.3.1, …, sha256=…
#   [ PASS ] DB reachable: postgres://127.0.0.1:5432/gseer?sslmode=disable
#   [ PASS ] PG version: PostgreSQL 17 (minimum 16)
#   [ PASS ] migrations: at head v17
#   [ PASS ] privilege audit: fail-closed effective-privilege audit passed under the gseer_app runtime credential (OD-5)
#   [ HINT ] index: EMPTY — no assets or services ingested yet
#         Try: for evaluation data run: gseer setup --load-fixtures
#
# 6 PASS, 0 FAIL.
```

The EMPTY index is a **HINT**, not a failure — doctor never dresses an
empty index up as healthy.  Doctor is read-only: it never migrates and
never repairs.  Without the runtime credential the audit line is an
honest `HINT` (provision with `gseer setup --self-hosted`); in the
`client` profile the DB checks are skipped with an explicit
`[ -    ] DB checks: skipped (client profile has no local database)`
line instead of a fake result.

Optional evaluation data (labeled SYNTHETIC everywhere; QS-6 consent
gate — set `GSEER_POLICY_EVALUATION_FIXTURES=true` or answer `y`):

```bash
GSEER_POLICY_EVALUATION_FIXTURES=true gseer setup --load-fixtures
#   [ ok  ] fixtures loaded: 4 assets inserted, …; 7 observations inserted, …
gseer setup --purge-fixtures          # remove them again any time
```

## 6. Scope creation — name the identifiers you care about

GSEER never imports target lists.  Your collection scope is what *you*
register:

```bash
gseer source add my-dns --type dns --seed-mode config \
    --seeds example.net,example.org --enable --yes
#   Registered source "my-dns" (id=2, connector=dns, enabled=true)
gseer source list                     # registered sources with terms verdicts
gseer source disable my-dns           # manage: enable/disable any time
```

Connector verdicts are terms-reviewed (`docs/SOURCES.md`): `dns`,
`ptr`, and `ip2asn` are enableable; `rdap` and `ct` refuse in code
(`E_SOURCE_TERMS`) pending provider authorization.

## 7. Authorized collection — explicit, one command per run

Nothing collects uninvoked: no daemon, no scheduler, no sidecar.

```bash
gseer collect run --source my-dns
#   { "source": "my-dns", "connector": "dns", "job_id": 1,
#     "records": 8, "facts": 8, "ingested": 8, "dead_lettered": 0, … }
gseer collect status                  # jobs, checkpoints, dead letters
gseer collect redrive --all           # operator's explicit second chance
```

Passive facts are asset-level and carry provenance on every
observation (and now on the asset row itself — the `sources` facet
reflects collector data); discovery never invents ports, banners, or
HTTP titles.

## 8. GQL search

```bash
gseer search 'port==443'                          # empty state is honest (exit 0)
gseer search 'port=443'                           # `=` is sugar for `==`
gseer search 'title="phpMyAdmin"'                 # strings are double-quoted
gseer search 'server="nginx" && port=443'         # boolean combine
gseer search 'ip IN_CIDR "203.0.113.0/24"'        # CIDR containment
gseer search 'domain IN_DOMAIN "example.net"'     # apex + subdomains
gseer search 'ANY(host){ http.status == 200 }'    # host has ANY service with 200
gseer search 'port==443' --facets                 # facet aggregates, same filter
gseer search 'port==443' --mode asset             # one row per asset
gseer search 'port==443' --explain                # compiled query plan
gseer search 'port ===' --validate                # validate only — no DB contact
#   E_QUERY_INVALID: GQL_E_PARSE: unexpected "=" at line 1, column 8   (exit 2)
gseer export 'stale == "false"' --format csv --out results.csv
```

Search results are paginated and sortable (`--limit`, `--offset`,
`--sort` — see `gseer search --help`); facets aggregate over the SAME
filtered rows.  Malformed evidence can no longer break a search: since
migration 0017, a value that does not parse yields an empty cell, never
a query failure.

## 9. Host inspection — `gseer host`

One identifier in, the host's full picture out (read-only):

```bash
gseer host 203.0.113.10
#   HOST 203.0.113.10
#     kind: ip  ip: 203.0.113.10/32
#     asn: 64500 (AS64500 …) · geo: US
#     sources: my-dns
#     first_seen: … · last_seen: … · stale: false
#   SERVICES (3)
#     22/tcp ssh · OpenSSH 9.6
#     80/tcp http · http 301 "Redirecting"
#     443/tcp https · http 200 "Admin Login" · tls TLSv1.3
#   OBSERVATIONS: 3 total · last observed …
gseer host portal.example.net -o json   # machine-readable full report
```

`gseer host` matches the identifier against asset value, ip, hostname,
and domain.  It is strictly read-only (SELECT-only in the runtime grant
matrix) and self-hosted; the planned `gseer scan`/`gseer asset`
commands do not exist (they exit 64 — see `docs/ROADMAP.md`).

## 10. Server startup (hardened: audit before bind)

```bash
gseer server start
#   Running the fail-closed privilege audit (OD-5) ...
#   [audt] … audit notices …
#   [ ok  ] privilege audit: PASSED — binding permitted (fail-closed gate, OD-5)
#   GSEER server listening on http://127.0.0.1:8443
#     Auth:       disabled (loopback bind only — set server.token for remote access)
#     Index:      SYNTHETIC EVALUATION DATA (fixture corpus; not real observations)
#   Ready. Press Ctrl-C to stop.
```

What the gate enforces before the port binds:

1. the runtime credential is configured (`E_RUNTIME_CREDENTIAL_REQUIRED`,
   exit 3, otherwise) and **is** `gseer_app`
   (`E_RUNTIME_CREDENTIAL_MISMATCH`, exit 5, for anything else);
2. the fail-closed effective-privilege audit **passes under gseer_app
   itself** — any missing grant, unsafe role attribute, or escalation
   path in the membership closure refuses startup with the complete
   failure list (exit 5).  There is no bypass, flag, or override.

A non-loopback bind is additionally refused without an API token
(`E_CONFIG_INSECURE`, exit 3).  Migrations are operator work
(`gseer setup --self-hosted`); the runtime credential is deliberately
denied the migration ledger and can never run them.

## 11. Health verification

```bash
gseer server status
#   Server: RUNNING (pid NNNN) on http://127.0.0.1:8443 — health OK
curl -s http://127.0.0.1:8443/api/v1/health
#   {"data":{"status":"ok"}, …}
curl -s http://127.0.0.1:8443/api/v1/version
curl -s 'http://127.0.0.1:8443/api/v1/search?q=port%3D%3D443'
```

If the server was started with `GSEER_TOKEN` set, every `/api/v1`
request needs `Authorization: Bearer $GSEER_TOKEN` — the client
profile CLI cannot present that header, so use `curl` (or query from
inside the compose network, `docs/DOCKER.md`).

## 12. Shutdown

```bash
gseer server stop            # verifies process identity, then SIGTERM
#   Server stopped (pid NNNN).
# (a foreground `server start` stops with Ctrl-C)
docker compose stop db       # stop PostgreSQL;  docker compose start db
```

## Troubleshooting quick picks

| Symptom | Meaning / fix |
| --- | --- |
| `E_RUNTIME_CREDENTIAL_REQUIRED` on `server start` | No runtime credential configured. Run `gseer setup --self-hosted` (prints `GSEER_DB_RUNTIME_DSN` once) and export it. |
| `E_RUNTIME_CREDENTIAL_MISMATCH` (exit 5) | `database.runtime_dsn` points at something that is not `gseer_app` (e.g. the operator credential). Point it at the gseer_app DSN — the server never serves traffic under another identity. |
| `E_PRIVILEGE_AUDIT_FAILED` (exit 5) | The fail-closed audit found real deployment problems (missing grants, unsafe roles, escalation paths). Fix the deployment per `docs/DEPLOYMENT.md` §3.5 — never grant `gseer_app` more privileges to "make it pass". |
| `E_CLIENT_ONLY` from `host`/`server`/`export` | Your shell is still in the `client` profile — export `GSEER_PROFILE=self-hosted` (+ DSNs), or pass `--profile self-hosted`. |
| `E_BOOTSTRAP` during setup | The operator credential could not create/validate the hardened roles. Run setup with the database-owner credential, or remediate the named role state. |
| `[note ] GSEER_DB_RUNTIME_DSN is not set …` on a data command | That command is running under the operator credential (explicit notice, not silent). Configure the runtime DSN for the least-privilege path. |

## Where to go next

- `docs/DEPLOYMENT.md` — credential model, the hardened startup
  sequence, the fail-closed privilege audit, PostgreSQL policy.
- `docs/INSTALL.md` — install methods, checksums, build from source.
- `docs/DOCKER.md` — full db + server stack, backup/restore, recovery.
- `docs/UPDATE.md` — signed updates and the in-place binary upgrade path.
- `docs/SOURCES.md` — connector terms register.  `docs/RULES.md` —
  fingerprint rule contributions.
- `docs/ROADMAP.md` — what is live per release.
