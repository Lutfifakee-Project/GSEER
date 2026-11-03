# GSEER

> Internet Asset Intelligence & Search Engine.
> **Discover. Identify. Search.**

[![CI](https://github.com/Lutfifakee-Project/GSEER/actions/workflows/ci.yml/badge.svg)](https://github.com/Lutfifakee-Project/GSEER/actions/workflows/ci.yml)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL--3.0-blue.svg)](LICENSE)

GSEER is an open-source, **self-hosted** internet-asset intelligence and
search engine. You point it at identifiers you care about, its passive
collectors gather public facts about them (DNS records, ASN ownership),
and you search everything it has indexed with a simple query language —
through a CLI, a REST API, and a built-in dashboard.

Everything runs on your machine: your database, your data, your rules.

---

## Features

- **Search (GQL)** — one query language, three surfaces (CLI, REST API,
  dashboard), all backed by a single `SearchService`. Search is always
  read-only: a query can never trigger collection or mutate data.
- **Faceted search** — port / protocol / technology / service / country /
  source / freshness aggregates over the same filter
  (`gseer search --facets`).
- **Export** — streamed JSON / CSV / NDJSON with cursor-stable
  pagination and RFC 4180 escaping (`gseer export`).
- **Passive collection** — explicit, operator-driven collectors with a
  terms-review gate, checkpoints, dead-letter bookkeeping, and re-drive.
  Nothing ever runs uninvoked.
- **Suppression policy** — block IPs/CIDRs/domains from ever entering
  your index, retroactively if needed (`gseer suppress`).
- **Fingerprint rules** — a lint-gated, versioned rule collection that
  classifies technology evidence an observation already carries into
  products with confidence (`gseer rules`).
- **Signed updates** — `gseer update` verifies an Ed25519-signed
  manifest against a key embedded in the binary before anything is
  downloaded or replaced.
- **Full Docker stack** — one command writes a db + server compose file
  with healthchecks, a persistent volume, and a generated API token.

## Requirements

| Component | Requirement |
| --- | --- |
| Go | 1.26+ (only for building from source) |
| Docker + Compose | for the bundled PostgreSQL 17 (recommended path) |
| PostgreSQL | 15+ if you run your own (v0.1.0 is verified against 17) |
| OS | Linux / macOS / Windows |

> No release tarball exists yet — build from source (below). Docker is
> the easiest way to satisfy the PostgreSQL dependency.

## Quick Start

Copy-paste, in order. Total time: a few minutes.

```bash
# 1. Build the binary (requires Go 1.26+)
git clone https://github.com/Lutfifakee-Project/GSEER.git
cd GSEER
make build VERSION=v0.1.0
./bin/gseer version
#   → gseer v0.1.0
#   (optional: sudo cp bin/gseer /usr/local/bin/ so `gseer` is on PATH)

# 2. Write the compose file + a .env with a random database password
gseer setup --self-hosted --with-compose

# 3. Start PostgreSQL (healthcheck-gated)
docker compose up -d db
docker compose ps db          # wait until it shows "healthy"

# 4. Initialize the schema — all migrations, zero manual SQL
gseer setup --self-hosted --db-url "$(grep '^GSEER_DSN=' .env | cut -d= -f2-)"
#   → [ ok ] migrations: 6 applied, schema at head v6

# 5. Verify the installation
gseer doctor
#   → 5 PASS, 0 FAIL.  (The index is reported as EMPTY — that is honest.)

# 6. Your first search (a fresh install has an empty index)
gseer search 'port==443'
#   → The index is empty — no assets have been ingested yet.

# 7. (Optional) load the labeled SYNTHETIC evaluation corpus so you can
#    try search/export/facets without collecting anything
gseer setup --load-fixtures          # asks for a y/yes confirmation
gseer search 'port==443 && title CONTAINS "login"'
#   Every surface labels this data: SYNTHETIC EVALUATION DATA.

# 8. Start the dashboard + API (runs in the foreground)
gseer server start
#   Dashboard:  http://127.0.0.1:8443/
#   API health: http://127.0.0.1:8443/api/v1/health
```

Prefer everything in Docker? `gseer setup --with-compose --full` writes
a `docker-compose.full.yml` (db + server, generated API token, database
not exposed to the host) — then `docker compose -f
docker-compose.full.yml up -d --build`. See `docs/DOCKER.md`.

**Stop / restart the service:**

```bash
# server start runs in the foreground:
#   stop with Ctrl-C in its terminal, or from another terminal:
gseer server stop
gseer server status
gseer server start          # start it again
docker compose stop db      # stop PostgreSQL;  docker compose start db
```

## First searches (GQL)

GQL is the GSEER Query Language. `=` is sugar for `==`; strings are
double-quoted; numbers are bare integers.

```bash
gseer search 'port=443'                            # `=` is sugar for `==`
gseer search 'title="phpMyAdmin"'                  # strings are double-quoted
gseer search 'server="nginx" && port=443'          # boolean combine
gseer search 'ip IN_CIDR "203.0.113.0/24"'         # CIDR containment
gseer search 'domain IN_DOMAIN "example.net"'      # apex + subdomains
gseer search 'ANY(host){ http.status == 200 }'     # host has ANY service with 200
gseer search 'port==443' --facets                  # facet aggregates over the same rows
gseer search 'stale == "false"' --facets --facet port,protocol,country --max-values 5
gseer search 'port==443' --mode asset              # one row per asset
gseer search 'port==443' --explain                 # print the compiled query plan
gseer search 'port==443' --validate                # validate only, no DB contact
```

The full field registry (every field and its operators) is in
`gseer search --help`. Export the results:

```bash
gseer export 'stale == "false"' --format csv --out results.csv
gseer export 'port == 443' --format ndjson --out -   # stdout; stats go to stderr
```

## CLI Command Reference

Run `gseer --help` for the live list; `gseer <command> --help` documents
every flag. Verified against v0.1.0:

| Command | Purpose |
| --- | --- |
| `gseer search <query>` | Search the index with GQL (`--facets`, `--mode`, `--limit`, `--sort`, `--explain`, `--validate`, `-o json`) |
| `gseer export <query>` | Stream results as JSON / CSV / NDJSON (`--format`, `--out`, `--max`) |
| `gseer setup` | Configure client mode, or `--self-hosted` to provision (`--with-compose [--full]`, `--load-fixtures`, `--purge-fixtures`) |
| `gseer doctor` | Diagnose config / DB / schema / index health (read-only) |
| `gseer server start\|status\|stop` | Manage the local API + dashboard |
| `gseer source add\|list\|enable\|disable` | Register and manage passive data sources |
| `gseer collect run\|status\|redrive` | Run collectors; inspect jobs; re-drive dead letters |
| `gseer suppress add\|list\|remove` | Manage the suppression policy |
| `gseer rules list\|validate` | Fingerprint rules (list; lint + false-positive tests) |
| `gseer update --check\|--apply` | Verify / apply signed updates |
| `gseer db migrate status\|diagnose\|force` | Inspect and recover schema migrations |
| `gseer version` | Version, commit, build info |
| `gseer completion bash\|zsh\|fish\|powershell` | Shell completion |

Planned but **not** in this release: `gseer scan`, `gseer asset`.
Invoking them prints a pointer and exits with code 64 — no scanning
exists in GSEER.

## Dashboard

`gseer server start` serves a server-rendered dashboard at
`http://127.0.0.1:8443/` (loopback by default). It is read-only
(GET-only, no cookies, no login) and runs the same `SearchService` as
the CLI: the search box accepts GQL, results show the same rows, and
the facet panel mirrors `--facets`. The dashboard never triggers
collection.

## REST API

The API lives under `/api/v1` on the same port. On the default loopback
bind no token is required; a non-loopback bind **requires** one
(`GSEER_TOKEN`, enforced by the config layer).

| Endpoint | Method | Purpose |
| --- | --- | --- |
| `/api/v1/health` | GET | Liveness (`{"status":"ok"}`) |
| `/api/v1/ready` | GET | Readiness |
| `/api/v1/version` | GET | Version info |
| `/api/v1/search` | GET / POST | Search (same GQL, same semantics as the CLI) |
| `/api/v1/facets` | GET | Facet aggregates for a GQL filter |
| `/api/v1/export` | GET | Streamed JSON / CSV / NDJSON (`Content-Disposition` attachment) |
| `/api/v1/validate` | POST | Validate a GQL query |
| `/api/v1/explain` | POST | Compiled query plan |
| `/api/v1/stats` | GET | Index statistics |

```bash
curl 'http://127.0.0.1:8443/api/v1/search?q=port%3D%3D443'
curl -H "Authorization: Bearer $GSEER_TOKEN" 'http://127.0.0.1:8443/api/v1/search?q=port%3D%3D443'
```

## Data Collection

**Nothing collects uninvoked.** You register a source, enable it, and
run it — one explicit command each. Targets are identifiers you named
or assets already in your index; there are no third-party target lists
and no enumeration.

```bash
# Register a passive DNS source (targets = domains you choose)
gseer source add my-dns --type dns --seed-mode config \
    --seeds example.net,example.org --enable --yes

# Run it once (explicit; no daemon, no scheduler)
gseer collect run --source my-dns

# What landed (passive facts are asset-level: a DNS answer proves the
# name was published — it never invents ports, banners or HTTP titles)
gseer search 'domain IN_DOMAIN "example.net"'

# Bookkeeping and recovery
gseer collect status                       # jobs, checkpoints, dead letters
gseer collect redrive --all                # re-ingest dead-lettered records
gseer collect redrive --source my-dns --discard

# Keep classes of records out of your index entirely
gseer suppress add cidr 10.0.0.0/8 --reason "RFC1918"
gseer suppress add domain_suffix internal.example --reason "our own space"
gseer suppress list
```

Connector status in v0.1.0:

| Connector | Status |
| --- | --- |
| `dns` | **Operational** — verified end-to-end against operator-chosen names |
| `ip2asn` | Reviewed and enableable (Team Cymru free community service) |
| `rdap` | **Disabled** — RIR terms do not grant indexing/redistribution yet |
| `ct` (crt.sh) | **Disabled** — no published terms covering automated collection |

Disabled connectors are enforced in code: enabling or running them
refuses with `E_SOURCE_TERMS`. Verdicts and the review register live in
`docs/SOURCES.md`.

## Configuration

Precedence: CLI flags > environment variables > YAML config file >
defaults. The config file lives at `~/.gseer/config.yaml` (override
with `--config` or `GSEER_CONFIG`); see `.env.example` for the full
annotated set.

| Variable | Meaning |
| --- | --- |
| `GSEER_PROFILE` | `client` (default) or `self-hosted` |
| `GSEER_DB_DSN` | Full PostgreSQL DSN (overrides the components below) |
| `GSEER_DB_HOST` / `GSEER_DB_PORT` / `GSEER_DB_USER` / `GSEER_DB_PASSWORD` / `GSEER_DB_NAME` / `GSEER_DB_SSLMODE` | DSN components |
| `GSEER_LISTEN` | Server bind address (default `127.0.0.1:8443`; non-loopback requires `GSEER_TOKEN`) |
| `GSEER_TOKEN` | API token for the REST API (`Authorization: Bearer …`) |
| `GSEER_LOG_LEVEL` / `GSEER_LOG_FORMAT` | `debug\|info\|warn\|error` / `text\|json` |
| `GSEER_POLICY_EVALUATION_FIXTURES` | Standing consent for the fixture load/purge gate |
| `GSEER_FINGERPRINTS_RULES_DIR` / `GSEER_FINGERPRINTS_DISABLED` | Operator fingerprint rules / kill switch |
| `GSEER_UPDATE_MANIFEST_URL` | Signed-update channel |
| `GSEER_SERVER_TRUSTED_PROXIES` | Reverse-proxy CIDRs allowed to set `X-Forwarded-For` |

`gseer setup --with-compose` generates a `.env` (mode 0600) with a
random database password — never commit a real `.env`.

## Troubleshooting

| Symptom | What it means / what to do |
| --- | --- |
| `E_CONFIG_REQUIRED: client.endpoint …` | You are in the client profile. For local use run with `--self-hosted` (it implies the profile). |
| `E_DB_UNREACHABLE: cannot reach PostgreSQL` | The database is not running. `docker compose up -d db`, then `docker compose ps db` until healthy. |
| `E_CONFIG_INSECURE: non-loopback bind requires a token` | Set `GSEER_TOKEN` (or `server.token`) before binding a non-loopback address. |
| `E_SOURCE_TERMS` when enabling/running a source | That connector's provider terms are not cleared for automated collection (`docs/SOURCES.md`). This is a policy gate, not a bug. |
| `E_QUERY_*` on search/facets | The GQL did not validate. The error names the position and a `Try:` fix; `gseer search '<q>' --validate` checks without touching the DB. |
| `E_UPDATE_UNSIGNED` | The update manifest has no valid signature against the embedded key. It is refused — see `docs/UPDATE.md`. |
| Doctor reports `HINT index: EMPTY` | Normal on a fresh install. Load fixtures for evaluation, or register a source and run `gseer collect run`. |
| Migration failed mid-startup | `gseer db migrate status`, then `gseer db migrate diagnose`; `force` exists for recovery and asks for confirmation. |
| `server start` says the port is taken | Another instance is running (`gseer server status`) or something else uses 8443 — change `GSEER_LISTEN`. |

## Security, Limitations, and License

**Security posture**

- **Search ≠ Scan** — no code path in this repository port-scans,
  banner-grabs, or enumerates the internet. A query can only read.
  Planned active observation (`gseer scan`) does not exist in this
  release; invoking it exits 64.
- **Nothing runs uninvoked** — no daemon, no scheduler. Every collection
  run is an explicit operator command, gated by the terms register.
- **Suppression is enforced twice** — in every ingestion path AND by a
  database trigger that refuses suppressed writes if any path forgets.
- **Loopback by default** — the DB binds `127.0.0.1` only (compose full
  stack: no host port at all); the server binds loopback and a
  non-loopback bind requires a token; security headers on every
  response; API tokens are hashed at rest.
- **Updates are verified before anything is downloaded** — Ed25519
  signature against the key embedded in the binary, then version gate,
  platform match, SHA-256, atomic replace with rollback.

**Limitations (honest)**

- **Self-hosted index only.** A fresh install has an empty database;
  you search exactly what your sources collected. There is **no**
  global, FOFA-scale index — GSEER is the engine, not a hosted service.
- **Passive facts are asset-level.** A DNS answer or a certificate-log
  entry proves an identifier was published; it never invents a port, a
  banner, or an HTTP title. Service-level rows appear only from
  evidence that actually carries them.
- **RDAP and crt.sh/CT are disabled** until their providers' terms are
  reviewed and approved. The enforcement is in code, not just docs.
- **Fingerprint coverage is deliberately conservative** — the seed set
  classifies only well-documented header/banner evidence; CMS/framework
  identification extends via contributions (`docs/RULES.md`).
- **No exploitation signatures.** The rule linter rejects anything
  shaped like an exploit or credential payload.

**License**

GNU Affero General Public License v3.0 — see [`LICENSE`](LICENSE).
Contributions are welcome: fingerprint rules (`docs/RULES.md`) are the
easiest starting point; every Go file carries the AGPL header and CI
enforces it.

## Documentation

- [`docs/QUICKSTART.md`](docs/QUICKSTART.md) — the canonical fresh-install workflow
- [`docs/INSTALL.md`](docs/INSTALL.md) — install methods, checksums, build from source
- [`docs/DOCKER.md`](docs/DOCKER.md) — full stack: startup, upgrade, backup/restore, recovery
- [`docs/SOURCES.md`](docs/SOURCES.md) — passive data-source review register (terms verdicts)
- [`docs/RULES.md`](docs/RULES.md) — fingerprint rule format, confidence tiers, contributions
- [`docs/UPDATE.md`](docs/UPDATE.md) — the signed-update chain, key ceremony, recovery
- [`docs/ROADMAP.md`](docs/ROADMAP.md) — release register (what is live per release)
- [`CHANGELOG.md`](CHANGELOG.md) — per-release changes
- [`GSEER_BACKLOG.md`](GSEER_BACKLOG.md) — milestone register with per-milestone test evidence
