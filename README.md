# GSEER

![GSEER banner](docs/assets/banner.png)

> **GSEER — internet-asset search engine**
> Search internet assets. Collect authorized data. Store locally. Search easily.

[![CI](https://github.com/Lutfifakee-Project/gseer/actions/workflows/ci.yml/badge.svg)](https://github.com/Lutfifakee-Project/gseer/actions/workflows/ci.yml)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL--3.0-blue.svg)](LICENSE)

---

## 1. What is GSEER?

GSEER is an open-source, **self-hosted search engine for internet assets**.
You tell it which identifiers you care about, its passive collectors gather
public facts about them (DNS answers, reverse-DNS names, ASN ownership), and
you search everything it has indexed with a simple query language — from a
CLI, a REST API, and a built-in dashboard.

**Everything runs on your machine**: your database, your data, your rules.
No account, no API key, no hosted service. A query can only ever READ —
searching never collects, and nothing collects unless you run it.

Product principles: Easy to Use · Fully Self-Hosted · Local-First ·
Accurate Search · Transparent Data Collection · Secure by Default ·
Production-Ready · Independent Brand.

## 2. Installation

Install Go (latest version), then one command:

```bash
go install -v github.com/Lutfifakee-Project/gseer/cmd/gseer@latest
```

That is the whole install. The binary lands in `$(go env GOPATH)/bin` — make
sure that directory is on your `PATH`.

Requirements for the default (personal) mode: **none beyond Go-built
binary**. No database server, no Docker, no config files to write.

## 3. Quick Start

Copy-paste, in order:

```bash
# 1. First run — GSEER initializes personal mode automatically:
gseer
#   → creates ~/.gseer/gseer.db (one SQLite file, 0600)
#   → writes ~/.gseer/config.yaml
#   → prints exactly what to do next

# 2. Verify the installation:
gseer doctor
#   → 4 PASS, 0 FAIL (the index is reported EMPTY — that is honest)

# 3. Try search with the labeled SYNTHETIC evaluation corpus:
gseer setup --load-fixtures          # asks for a y/yes confirmation
gseer search 'port==443 && title CONTAINS "login"'
#   Every surface labels this data: SYNTHETIC EVALUATION DATA.

# 4. (Optional) consistent backup of your local index:
gseer db backup --out backup.db
```

Your first REAL data comes from collection — see section 6.

Prefer a bigger, multi-user setup? The self-hosted profile runs on
PostgreSQL 16+ with a hardened deployment order — see
[`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) and section 7.

## 4. Search examples

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
gseer search 'port==443' --mode asset              # one row per asset
gseer search 'port==443' --explain                 # print the compiled query plan
gseer search 'port==443' --validate                # validate only, no DB contact
gseer search '"wordpress"'                         # bare-keyword free text
gseer search 'ip*="203.0.113.*"'                   # wildcard fuzzy match (* = any run, ? = one)
gseer search 'after="2026-01-01" && status_code=200'
```

## 5. Search Syntax

The full field registry is in `gseer search --help`; the complete grammar,
operator list, and the deliberate NOT-supported list (and why — Search ≠
Scan) live in [`docs/SEARCH-SYNTAX.md`](docs/SEARCH-SYNTAX.md).

Core shape:

| Feature | Example |
| --- | --- |
| Equality | `port=443`, `title="Login"` |
| Comparison | `status_code>=200`, `after="2026-01-01"` |
| Boolean | `&&`, `\|\|`, `!`, parentheses |
| Wildcards | `ip*="203.0.113.*"` (fuzzy `*=`) |
| Containment | `ip IN_CIDR "…/24"`, `domain IN_DOMAIN "…"` |
| Free text | `"wordpress"` (quoted terms across stored text surfaces) |
| Field aliases | `host`, `org`, `status_code`, `cert.subject.cn` |
| Derived booleans | `is_domain`, `is_ipv6`, `cert.is_expired` |

Wrong field names get helpful errors ("Unknown field: prt / Did you mean:
port"), and `--validate` checks a query without touching the database.

## 6. Data Collection

**Nothing collects uninvoked.** GSEER stores only what you collect or
import — there is no preloaded global index. Collection is always one
explicit command against sources YOU registered and enabled, targeting
only identifiers you named or assets already in your index. Searching
never triggers collection.

```bash
# Register a passive reverse-DNS source over the IPs in your index:
gseer source add my-ptr --type ptr --seed-mode index --enable

# Run it once (explicit; no daemon, no scheduler):
gseer collect run --source my-ptr

# What landed, when, and how much:
gseer collect status
gseer search 'hostname*="*.example.test"' --facets

# Bookkeeping and recovery:
gseer collect redrive --all                # re-ingest dead-lettered records
gseer suppress add cidr 10.0.0.0/8 --reason "RFC1918"   # keep ranges out
```

Connector register (verdicts enforced in code — see
[`docs/SOURCES.md`](docs/SOURCES.md) for the full terms review):

| Connector | What it gathers | Status |
| --- | --- | --- |
| `dns` | A/AAAA/CNAME/MX/NS answers for operator-named domains | **Operational** |
| `ptr` | reverse-DNS (PTR) names for operator-named addresses | **Operational** (v0.3.0) |
| `ip2asn` | ASN / network-owner enrichment (Team Cymru free community service, DNS interface) | **Operational** |
| `rdap` | RIR registration data | **Disabled** — provider terms do not grant indexing/redistribution yet |
| `ct` | certificate-transparency aggregation | **Disabled** — no published terms covering indexing/reuse |

Personal mode (SQLite) supports the full collection surface since v0.3.0:
register, enable, run, status, re-drive, suppression — all against your
single local file.

## 7. Database and local storage

**Personal mode (default).** One embedded SQLite file at
`~/.gseer/gseer.db` (created `0600`, parent directory `0700`). No server,
no Docker, no account. Everything — assets, observations, evidence,
collection bookkeeping, suppression rules — lives in that one file, so:

```bash
gseer db backup --out backup.db     # consistent snapshot (VACUUM INTO), 0600
gseer db restore backup.db          # atomic replace, old file kept aside
# Move to another machine: copy gseer.db (and config.yaml), done.
```

**Self-hosted mode.** PostgreSQL 16+ as the engine, credential separation
(operator vs. least-privilege `gseer_app` runtime), a fail-closed privilege
audit before the server binds, and Docker compose generation:

```bash
gseer setup --self-hosted --with-compose   # writes compose + .env, then follows docs/QUICKSTART.md
```

Upgrades never delete your data: migrations are versioned, idempotent, and
recoverable (`gseer db migrate status|diagnose|force`).

## 8. Configuration

Zero configuration is required for the basic flow. When you do want to
configure: precedence is CLI flags > environment variables > YAML config
file > defaults; the file lives at `~/.gseer/config.yaml` (override with
`--config` or `GSEER_CONFIG`). See `.env.example` for the annotated set.

| Variable | Meaning |
| --- | --- |
| `GSEER_PROFILE` | `personal` (default after first run), `client`, or `self-hosted` |
| `GSEER_DB_DSN` | PostgreSQL DSN (operator credential) for the self-hosted profile |
| `GSEER_DB_RUNTIME_DSN` | Least-privilege runtime DSN (`gseer_app`; required by `server start`) |
| `GSEER_LISTEN` | Server bind (default `127.0.0.1:8443`; non-loopback requires `GSEER_TOKEN`) |
| `GSEER_TOKEN` | API token for the REST API |
| `GSEER_POLICY_EVALUATION_FIXTURES` | Standing consent for the fixture load/purge gate |
| `GSEER_FINGERPRINTS_RULES_DIR` / `GSEER_FINGERPRINTS_DISABLED` | Operator fingerprint rules / kill switch |
| `GSEER_UPDATE_MANIFEST_URL` | Signed-update channel |

Every command has `--help`; `gseer --help` lists the live command set
(including `gseer version`, `gseer doctor`, and shell completion).

## 9. Troubleshooting

| Symptom | What it means / what to do |
| --- | --- |
| `commit: unknown` / `built_at: unknown` in `gseer version` | Normal for `go install` builds — module downloads carry no VCS metadata. Version is still correct; release binaries from GitHub Releases carry both fields. |
| `E_CONFIG_REQUIRED: client.endpoint …` | Your shell resolves to the client profile. Run `gseer` once to initialize personal mode, or `gseer setup --personal`. |
| `E_CLIENT_ONLY` on `server start` / `host` | That command needs a local profile. Personal mode keeps the search engine local; `server start` is a self-hosted-profile command. |
| `E_SOURCE_TERMS` when enabling/running a source | That connector's provider terms are not cleared for automated collection (`docs/SOURCES.md`). This is a policy gate, not a bug. |
| `E_QUERY_*` on search/facets | The GQL did not validate. The error names the position and a `Try:` fix; `gseer search '<q>' --validate` checks without touching the DB. |
| Doctor reports `HINT index: EMPTY` | Normal on a fresh install. Load fixtures for evaluation, or register a source and run `gseer collect run`. |
| `E_DB_BACKUP: …` | The backup target must differ from the live database file. |
| Migration failed mid-setup | `gseer db migrate status`, then `gseer db migrate diagnose`; `force` exists for recovery and asks for confirmation. |

## 10. Uninstall and backup

```bash
gseer db backup --out ~/gseer-backup.db   # 1. take a snapshot first
rm "$(command -v gseer)"                  # 2. remove the binary (GOPATH/bin)
rm -rf ~/.gseer                           # 3. remove data + config (this deletes your index!)
```

To start over but keep your data: keep `gseer.db`, delete only
`~/.gseer/config.yaml`, and run `gseer` again — it re-initializes around
the existing database.

## Security, limitations, license

- **Search ≠ Scan** — no code path in this repository port-scans,
  banner-grabs, or enumerates the internet. A query can only read.
  Planned active observation (`gseer scan`) does not exist; invoking it
  exits 64.
- **Nothing runs uninvoked** — no daemon, no scheduler. Every collection
  run is an explicit operator command, gated by the terms register.
- **Passive facts are asset-level** — a DNS/PTR answer proves a name was
  published; it never invents ports, banners, or HTTP titles.
- **Personal index only** — you search exactly what your sources
  collected. GSEER is the engine, not a hosted service.
- **RDAP and CT stay disabled** until their providers' terms clear; the
  enforcement is in code, not just docs.
- **Signed updates** — `gseer update` verifies an Ed25519-signed manifest
  before anything is downloaded or replaced.

GNU Affero General Public License v3.0 — see [`LICENSE`](LICENSE).
Contributions welcome: fingerprint rules ([`docs/RULES.md`](docs/RULES.md))
are the easiest starting point.

## Documentation

- [`docs/QUICKSTART.md`](docs/QUICKSTART.md) — the canonical fresh-install workflow
- [`docs/INSTALL.md`](docs/INSTALL.md) — install methods, checksums, build from source
- [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) — self-hosted credential model, hardened startup
- [`docs/DOCKER.md`](docs/DOCKER.md) — full Docker stack
- [`docs/UPDATE.md`](docs/UPDATE.md) — the signed-update chain
- [`docs/SOURCES.md`](docs/SOURCES.md) — data-source review register (where collection data comes from)
- [`docs/RULES.md`](docs/RULES.md) — fingerprint rule format and contributions
- [`docs/ROADMAP.md`](docs/ROADMAP.md) — release register (what is live per release)
- [`CHANGELOG.md`](CHANGELOG.md) — per-release changes
- [`docs/releases/`](docs/releases/) — per-release notes
