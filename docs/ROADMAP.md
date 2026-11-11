# GSEER — Roadmap & Release Register

> Source of truth for what is live per release.
> The "Coming later" pointer line in `gseer --help` links here.

## Release & Data Register

| Version | Phase | Label | Real data available | Still NOT available |
| --- | --- | --- | --- | --- |
| `v0.0.1` | 1 | **Search Engine Developer Preview** | **None.** Searches run against the labeled synthetic fixture dataset (banner-marked everywhere). No passive connectors, no observation capability, no public endpoint. | Real internet data; active observation; facets; `gseer update`; public service. |
| `v0.0.2` | 2 | **Passive Intelligence** | **SHIPPED (M13–M16).** First-party passive connectors with terms review: operator-domain DNS + Team Cymru IP-to-ASN enableable; RDAP + CT implemented but held disabled pending provider authorization (`docs/SOURCES.md`). Centralized suppression policy (ingest gate + DB backstop trigger + retroactive retraction). Collection bookkeeping: checkpoints, jobs, dead-letters. Hardened API/dashboard (security headers, trusted-proxy opt-in, PID identity verification). Real data is available the moment the operator enables a reviewed source and runs `gseer collect run` — nothing collects uninvoked. | Active observation; public search service; internet-wide anything; distributed collection; everything. |
| `v0.1.0` | 3 | **First Useful Self-Hosted** | **SHIPPED (M18–M24).** Fingerprint seed set (22 lint-gated rules across six families; `docs/RULES.md` = contribution format; ingestion enrichment classifies evidence the record already carries). Faceted search (port/protocol/technology/service/country/source/freshness) through the same SearchService on CLI/API/dashboard. Streamed JSON/CSV/NDJSON export (cursor-stable, RFC 4180, safe filenames, no credential leakage). Signed `gseer update` (embedded Ed25519 anchor, verification-first, atomic replace + `.bak` recovery; `docs/UPDATE.md`). Full Docker stack (db + server, healthchecks, persistent volume, generated token; `docs/DOCKER.md`). Dead-letter re-drive (`gseer collect redrive`) + documented backup/restore. | Active observation; public search service; community collection; asset detail view (`gseer asset`); dedicated host-inspection subcommand. |
| `v0.2.0` | 4 | **Hardened self-hosted readiness** | **SHIPPED.** The hardened deployment model lands in the binary: migration 0016 `gseer_policy` enforcement layer (pinned, hash-enforced) + migration 0017 search safe-casts (GAP-1); operator/runtime credential separation with the least-privilege `gseer_app` grant matrix (bootstrap → grants embedded and automated by `gseer setup --self-hosted`); the fail-closed effective-privilege audit runs IN-BINARY before `gseer server start` binds (exit 5 on refusal, no bypass — OD-5) and in `gseer doctor`; `gseer_app` runtime-credential identity enforcement; PostgreSQL 16+ minimum (OD-7). `gseer host <ip\|hostname\|domain>` read-only host inspection (v0.2.0). Collected assets carry source provenance (`sources` facet + `gseer host`). Malformed evidence can no longer break search (0017 safe-casts). Real data comes only from the operator's own reviewed sources — nothing collects uninvoked. | Operated public index (pilot-gated, below); active observation; `gseer scan`/`gseer asset`; community collector network; prebuilt binaries (EU-3). |
| `v0.2.1` | 4+ | **Compatibility layer, personal mode, release engineering** | **SHIPPED.** GQL compatibility layer (`*=`, free text, aliases, time filters, derived booleans); personal mode on embedded SQLite (`setup --personal`, `db backup/restore`, suppression parity); validated release config + `go install` support; draft-gated releases. PostgreSQL remains the primary self-hosted engine. | Personal-mode collection (`source`/`collect` refused on the personal profile in this line); zero-config first run. |
| `v0.2.2` | 4+ | **Personal-mode wire-up fixes** | **SHIPPED.** First real personal-mode walkthrough remediation: `go install` binaries report their real version (`debug.ReadBuildInfo` fallback, never fabricated); `doctor` runs real personal checks (file stat first — never creates); standalone `setup --load-fixtures`/`--purge-fixtures` follow the saved personal profile; `gseer export` streams through the personal service; honest `E_PROFILE_UNSUPPORTED` refusals where personal mode had no path yet. | Collection in personal mode (landed v0.3.0). |
| `v0.3.0` | 4+ | **Zero-config first run, personal collection, new identity** | **SHIPPED.** Bare `gseer` auto-initializes personal mode (one code path with `setup --personal`; later runs print an honest status panel); `--version` consumed; the collection surface (`source add/list/enable/disable`, `collect run/status/redrive`, fingerprint enrichment) works against the personal SQLite file through the typed storage `Backend` contract; new `ptr` reverse-DNS connector (reviewed-enabled, `docs/SOURCES.md` §E); README rebuilt user-first (install → first result); identity: **GSEER — internet-asset search engine**. | Active observation; `gseer scan`/`gseer asset`; operated public index; distributed collection. |
| `v0.3.1` | 4+ | **Install path & first-run correctness** | **SHIPPED (pending tag).** The documented lowercase `go install` command now matches the module declaration (the v0.3.0 artifact shipped the mismatch: `go.mod` declared the UPPERCASE path); every engine command (`search`, `source`, `collect`, `suppress`, `export`, `db backup`) runs the zero-config first-run initialization bare `gseer` uses when nothing is configured — the first engine command on a fresh machine can no longer dead-end in an unrelated client-profile error; `search --validate`/`--explain` need no configuration; `doctor` on a fresh home reports the zero-config state read-only. Docs: clone directory name corrected (`gseer`), build examples pinned to v0.3.1. | Active observation; `gseer scan`/`gseer asset`; operated public index; distributed collection. |
| `v1.0.0` | 5 | **API stability** | `/api/v1` frozen; operational runbooks mature; security review. | Distributed community network. |
| `v1.x` | 6 | **Distributed Community Network** | Opt-in community collectors under the full contribution protocol. | — |

## The four planned-but-absent commands in v0.0.1

The `gseer --help` "Coming later" pointer line lists four commands that
are planned but not implemented in v0.0.1. Invoking any of them in v0.0.1
prints an honest pointer and exits 64.

| Command | Arrives | Notes |
| --- | --- | --- |
| `gseer update` | **shipped in v0.1.0** | Signed manifest → verify → checksum → atomic replace → rollback (`docs/UPDATE.md`). |
| `gseer scan` | later phase | Authorized service observation. Full public-readiness criteria + security review must close first. v0.0.2 shipped the passive connector framework (`gseer collect`), NOT active observation. |
| `gseer asset` | later release | Asset detail view + history. |
| `gseer export` | **shipped in v0.1.0** | Streamed JSON/CSV/NDJSON export (CLI + `/api/v1/export`). |

## What is never promised

- Whole-internet coverage.
- Non-web protocol observations before a per-family approval.
- Public search as a present repository capability.
- Fixture data as real observations.
- Exactly-once ingestion.
- Total audit preservation after storage exhaustion.
- Personal liability protection via entity separation.

## GPIM Activation Gate

GPIM activation is **blocked** until all requirements G-1 through G-8 are satisfied and review gates pass (gates, not approvals). G-1 through G-8 enforce:

- **Explicit owner approval** (go/no-go decision) before activation.
- **Legal and provider terms review** before any collection begins.
- **Defined collection scope** — what may be observed, from where, and for how long.
- **Opt-out mechanism** for operators and asset owners.
- **Operational policies** governing collection, retention, and use.
- **Pilot program** with published telemetry and report.
- **Evaluation** of pilot results before launch.
- **Security review** (PAC and related requirements) before activation.

PAC (Passive/Active Collection) and security review requirements remain in force and are not subsumed by public-readiness criteria. Operational policies and sensitive configuration remain in the operating entity's private infrastructure repository, never in this public repository.

The initial GPIM profile is web-service focused.
