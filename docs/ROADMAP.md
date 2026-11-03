# GSEER — Roadmap & Release Register

> Source of truth for what is live per release.
> The "Coming later" pointer line in `gseer --help` links here.

## Release & Data Register

| Version | Phase | Label | Real data available | Still NOT available |
| --- | --- | --- | --- | --- |
| `v0.0.1` | 1 | **Search Engine Developer Preview** | **None.** Searches run against the labeled synthetic fixture dataset (banner-marked everywhere). No passive connectors, no observation capability, no public endpoint. | Real internet data; active observation; facets; `gseer update`; public service. |
| `v0.0.2` | 2 | **Passive Intelligence** | **SHIPPED (M13–M16).** First-party passive connectors with terms review: operator-domain DNS + Team Cymru IP-to-ASN enableable; RDAP + CT implemented but held disabled pending provider authorization (`docs/SOURCES.md`). Centralized suppression policy (ingest gate + DB backstop trigger + retroactive retraction). Collection bookkeeping: checkpoints, jobs, dead-letters. Hardened API/dashboard (security headers, trusted-proxy opt-in, PID identity verification). Real data is available the moment the operator enables a reviewed source and runs `gseer collect run` — nothing collects uninvoked. | Active observation; public search service; internet-wide anything; distributed collection; everything. |
| `v0.1.0` | 3 | **First Useful Self-Hosted** | **SHIPPED (M18–M24).** Fingerprint seed set (22 lint-gated rules across six families; `docs/RULES.md` = contribution format; ingestion enrichment classifies evidence the record already carries). Faceted search (port/protocol/technology/service/country/source/freshness) through the same SearchService on CLI/API/dashboard. Streamed JSON/CSV/NDJSON export (cursor-stable, RFC 4180, safe filenames, no credential leakage). Signed `gseer update` (embedded Ed25519 anchor, verification-first, atomic replace + `.bak` recovery; `docs/UPDATE.md`). Full Docker stack (db + server, healthchecks, persistent volume, generated token; `docs/DOCKER.md`). Dead-letter re-drive (`gseer collect redrive`) + documented backup/restore. | Active observation; public search service; community collection; asset detail view. |
| `v0.2.0` | 4 | **Public Search Service readiness** | An operated public index **may** launch — capability gated by completion of required public-readiness criteria + 30-day pilot (single /24, published telemetry/report) reviewed + owner go/no-go decision. Launch without a completed pilot is prohibited. | Community collector network. |
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
