# GSEER — Roadmap & Release Register

> Source of truth for what is live per release. Mirrors `GSEER_PHASE0_SPEC/09_ROADMAP.md` §1.
> The "Coming later" pointer line in `gseer --help` links here.

## Release & Data Register

| Version | Phase | Label | Real data available | Still NOT available |
| --- | --- | --- | --- | --- |
| `v0.0.1` | 1 | **Search Engine Developer Preview** | **None.** Searches run against the labeled synthetic fixture dataset (banner-marked everywhere). No passive connectors, no observation capability, no public endpoint. | Real internet data; active observation (no P2 code reachable); facets; `gseer update`; public service. |
| `v0.0.2` | 2 | **Passive Intelligence** | **SHIPPED (M13–M16).** First-party passive connectors with terms review: operator-domain DNS + Team Cymru IP-to-ASN enableable; RDAP + CT implemented but held disabled pending provider authorization (`docs/SOURCES.md`). Centralized suppression policy (ingest gate + DB backstop trigger + retroactive retraction). Collection bookkeeping: checkpoints, jobs, dead-letters. Hardened API/dashboard (security headers, trusted-proxy opt-in, PID identity verification). Real data is available the moment the operator enables a reviewed source and runs `gseer collect run` — nothing collects uninvoked. | Active observation (P2/PAC, D-9 open); public search service; internet-wide anything; distributed collection; `ALL`. |
| `v0.1.0` | 3 | **First Useful Self-Hosted** | **SHIPPED (M18–M24).** Fingerprint seed set (22 lint-gated rules across six families; `docs/RULES.md` = contribution format; ingestion enrichment classifies evidence the record already carries). Faceted search (port/protocol/technology/service/country/source/freshness) through the same SearchService on CLI/API/dashboard. Streamed JSON/CSV/NDJSON export (cursor-stable, RFC 4180, safe filenames, no credential leakage). Signed `gseer update` (embedded Ed25519 anchor, verification-first, atomic replace + `.bak` recovery; `docs/UPDATE.md`). Full Docker stack (db + server, healthchecks, persistent volume, generated token; `docs/DOCKER.md`). Dead-letter re-drive (`gseer collect redrive`) + documented backup/restore. | Active observation (P2/PAC, D-9 open); public search service; community collection; asset detail view. |
| `v0.2.0` | 4 | **Public Search Service readiness** | An operated public index **may** launch — capability gated by GPIM G-1…G-7 complete + 30-day pilot (single /24, published telemetry/report) reviewed + owner go/no-go (G-8/D-17). Launch without a completed pilot is prohibited. | Community collector network. |
| `v1.0.0` | 5 | **API stability** | `/api/v1` frozen; operational runbooks mature; security review. | Distributed community network. |
| `v1.x` | 6 | **Distributed Community Network** | Opt-in community collectors under the full contribution protocol. | — |

## The four planned-but-absent commands in v0.0.1

The `gseer --help` "Coming later" pointer line lists four commands that
are planned but not implemented in v0.0.1. Invoking any of them in v0.0.1
prints an honest pointer and exits 64 (AC-5.7–5.8).

| Command | Arrives | Notes |
| --- | --- | --- |
| `gseer update` | **shipped in v0.1.0** | Signed manifest → verify → checksum → atomic replace → rollback (`docs/UPDATE.md`). |
| `gseer scan` | later phase (D-9 must close first; ADR-0017) | Authorized service observation (P2/PAC). Full PAC regime (SPEC-06 §3) + SEC-1…8.  v0.0.2 shipped the passive connector framework (`gseer collect`), NOT active observation. |
| `gseer asset` | later release | Asset detail view + history. |
| `gseer export` | **shipped in v0.1.0** | Streamed JSON/CSV/NDJSON export (CLI + `/api/v1/export`). |

## What is never promised

- Whole-internet coverage (DA-5).
- Non-web protocol observations before a per-family approval (DA-8, SPEC-06 §5.6).
- Public search as a present repository capability (DA-7).
- Fixture data as real observations (AC-9.7.x).
- Exactly-once ingestion (AC-10.13).
- Total audit preservation after storage exhaustion (ER-7).
- Personal liability protection via entity separation (DA-9).

## GPIM activation gate

GPIM activation is **blocked** until D-16…D-19 are decided and gates
G-1…G-8 pass (gates, not approvals). The initial GPIM profile is
web-service focused (K-2/K-3; DA-8). GPIM is never a default of any code
release; its policy/config lives in the operating entity's private
infrastructure repo, never in this public repository (DA-1).
