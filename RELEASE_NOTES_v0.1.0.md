# GSEER v0.1.0 — Release Notes

## GSEER

GSEER is an open-source, **self-hosted** internet-asset intelligence and
search engine. You point it at identifiers you care about, its passive
collectors gather public facts about them (DNS records, ASN ownership),
and you search everything it has indexed with a simple query language
(GQL) — through a CLI, a REST API, and a built-in dashboard.

Everything runs on your machine: your database, your data, your rules.

GSEER is **not** a FOFA clone and makes no claim of global internet
coverage. It is a self-hosted search engine over data *you* collect.

**License:** GNU Affero General Public License v3.0 (AGPL-3.0) — see
`LICENSE`.

## v0.1.0 — First Useful Self-Hosted

GSEER v0.1.0 is the first useful self-hosted release: a **source-only**
distribution that gives operators a local search engine over their own
passively collected asset data. Nothing collects uninvited, and search is
always read-only — GSEER does not scan the internet.

## What's included

- **GQL search** — one query language across CLI, REST API, and dashboard;
  read-only by design (a query can never trigger collection or mutate
  data). Surfaces: `gseer search`, `GET /api/v1/search`, dashboard.
- **Faceted search** — port/protocol/technology/service/country/source/
  freshness aggregates over the same GQL filter
  (`gseer search --facets`, `GET /api/v1/facets`, dashboard facet panel).
- **Export** — streamed JSON / CSV / NDJSON with cursor-stable pagination
  (`gseer export`, `GET /api/v1/export`).
- **Fingerprint rules** — a lint-gated, versioned rule collection (22 seed
  rules across web-server / cms / language / framework / web-tech /
  service families) that classifies observed evidence into technology
  products with confidence (`gseer rules list|validate`).
- **Passive collection** — explicit, operator-driven collectors with a
  terms-review gate, checkpoints, dead-letter bookkeeping, and re-drive.
  Reviewed-enabled: `dns`, `ip2asn`. Held disabled pending provider
  authorization: `rdap`, `ct` (`gseer source`, `gseer collect`).
- **Suppression policy** — block IPs/CIDRs/domains from your index,
  retroactively if needed (`gseer suppress`); enforced at ingestion and
  by a database backstop trigger.
- **Docker stack** — `gseer setup --with-compose --full` writes a db +
  server compose file with healthchecks, a persistent volume, and a
  generated API token (`docs/DOCKER.md`). The image is built locally
  from the `Dockerfile`, not pulled from a registry.
- **Signed updater mechanism** — `gseer update --check|--apply` verifies
  an Ed25519-signed manifest against a key embedded in the binary before
  anything is downloaded or replaced (atomic rename with `.bak` backup
  and rollback; `docs/UPDATE.md`).

## Security and data-collection limits

- **Search ≠ Scan** — no port-scanning, banner-grabbing, or internet
  enumeration exists in this release; `gseer scan` is planned, not
  shipped. Invoking it prints a pointer and exits 64.
- **Nothing runs uninvited** — no background collection daemon or automatic
  collection scheduler; every collection run is an explicit operator command
  gated by the terms register.
- **Passive facts are asset-level only** — a DNS answer proves a name was
  published; it never invents ports, banners, or HTTP titles.
- **No global index** — a fresh install has an empty database; you search
  exactly what your sources collected. GSEER is the engine, not a hosted
  service.

## Requirements

- **Go 1.26+** — required to build from source (per `go.mod`).
- **PostgreSQL 15+** — self-hosted, or via the bundled compose stack
  (`postgres:17-alpine`); v0.1.0 is verified against PG 17.

## Getting the source and building

v0.1.0 is distributed as source only. The artifact is the source archive
`GSEER-v0.1.0-source.zip` together with its SHA-256 checksum sidecar
`GSEER-v0.1.0-source.zip.sha256`. Both files must be placed in the same
directory before verification.

The checksum published with v0.1.0 covers **only the source archive**.
There are no binary checksums in this release. The SHA-256 printed by
`gseer doctor` is a self-reported running binary fingerprint for
diagnostic purposes and must not be compared against the source archive
checksum — they are different artifacts.

Verify the downloaded source archive from the directory containing both
files. Do not claim verification succeeded if the `.sha256` sidecar is
not present.

```bash
set -eu

ARCHIVE="GSEER-v0.1.0-source.zip"
CHECKSUM="GSEER-v0.1.0-source.zip.sha256"

# Verify the source archive checksum.  Abort if the sidecar is absent.
sha256sum -c "$CHECKSUM"

# Extract source archive into a temporary directory.
TMP="$(mktemp -d)"
unzip -q "$ARCHIVE" -d "$TMP"

# Enter the extracted source directory (git archive prefix).
cd "$TMP/GSEER-v0.1.0"

# Build from extracted source.  Version is stamped via ldflags.
make build VERSION=v0.1.0

# Verify binary version and help surface.
./bin/gseer version
./bin/gseer --help
```

Build verification does not require Docker or PostgreSQL.  It only
requires Go 1.26+.

For self-hosted provisioning, Docker Engine and the Docker Compose
plugin are prerequisites when using Compose.  `gseer setup` supports two
profiles:

- `client` — configures a remote endpoint URL (`--server`).  `client.endpoint`
  is required for this profile.
- `self-hosted` — verifies PostgreSQL reachability, checks PostgreSQL
  version (15+), and runs migrations to head.

`gseer setup --self-hosted --with-compose` writes a db-only
`docker-compose.yml` and a generated `.env` first, then stops.  On a
fresh machine the database only exists once Compose starts it, so the
continuation is:

```bash
set -eu

# Write Compose stack and .env (does not start containers).
gseer setup --self-hosted --with-compose

# Start PostgreSQL.
docker compose up -d db

# Verify PostgreSQL is healthy before proceeding.
# Inspect `docker compose ps db` output and confirm status is "healthy".
# Do not proceed until PostgreSQL reports healthy.
docker compose ps db

# Initialize schema/migrations only after db is healthy.
gseer setup --self-hosted --db-url "$(grep '^GSEER_DSN=' .env | cut -d= -f2-)"

# Diagnose installation.  Expect PASS with an honest EMPTY index.
gseer doctor
```

`--with-compose` writes files first and stops.  If an existing
`docker-compose.yml` already defines a `db` service, reuse that
configuration instead of overwriting it.  Do not overwrite an
operator-edited `docker-compose.yml`.

`gseer doctor` reports an honest EMPTY index on a fresh install; it does
not claim PASS on an unconfigured environment.

## Source-only distribution

The **only** downloadable release artifact for v0.1.0 is the source
archive `GSEER-v0.1.0-source.zip`. There are no prebuilt platform
binaries, no binary packages, and no published Docker image for this
release. Docker images are built locally
(`docker build --build-arg VERSION=v0.1.0 -t gseer:local .`), not
pulled from a registry. The SHA-256 checksum published with v0.1.0
covers the **source archive** — there are no binary checksums in this
release.

## Signed updates — bring your own manifest

The signed-update *mechanism* is implemented and shipped in v0.1.0, but
GSEER does **not** host an official manifest or update feed for this
release. `gseer update` requires an operator-provided
`GSEER_UPDATE_MANIFEST_URL` pointing to a manifest signed with the Ed25519
key embedded in the binary. Without a configured channel it refuses with
`E_UPDATE_NO_CHANNEL`. A new installation does **not** automatically have
an active update channel; the operator supplies and verifies their own
manifest (key ceremony and recovery: `docs/UPDATE.md` §3–§4).

## What is not in v0.1.0

- Global internet index or hosted search service (no FOFA-scale coverage).
- Active scanning / service observation (`gseer scan`, `gseer asset` —
  planned for a later phase; both exit 64 in v0.1.0).
- Prebuilt binaries for Linux, macOS, or Windows.
- Published Docker image / container registry.
- Official hosted update feed or automatic update channel.

Platform support is limited to where the CI matrix runs; no platform is
officially verified as a binary target in v0.1.0. Claims beyond this are
not made.

## Documentation

- `docs/QUICKSTART.md` — canonical fresh-install workflow
- `docs/INSTALL.md` — build from source, local Docker image
- `docs/DOCKER.md` — full self-hosted stack: startup, upgrade, backup/restore
- `docs/UPDATE.md` — signed-update chain, key ceremony, recovery
- `docs/SOURCES.md` — passive data-source review register
- `docs/RULES.md` — fingerprint rule format and contributions
- `docs/ROADMAP.md` — release register
- `CHANGELOG.md` — per-release changes
