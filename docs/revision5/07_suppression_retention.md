# Section 7 — Suppression and Retention Policy

## 7.1 Suppression is a matching/enforcement policy (not an Asset → Suppression N:1 relation)

Revision 4 incorrectly described an "Asset → Suppression N:1" relationship. The verified truth (Section 3):
- There is **no foreign key** from `suppressions` to `assets` or from `assets` to `suppressions`. `suppression_applications.asset_id` is a PLAIN BIGINT, not a FK.
- Suppression is a policy evaluated at write time:
  - **Go gate:** `internal/suppress.Matcher` consulted by the store's ingestion paths (`IngestFact`/`IngestFacts`/`UpsertAsset`) BEFORE any write. Suppressed facts are skipped cleanly, counted in `IngestStats.Suppressed`, and audited as an ingest-block row in `suppression_applications`.
  - **DB backstop:** trigger `trg_assets_suppression` → `fn_check_asset_suppression()` BEFORE INSERT OR UPDATE ON `assets`. Mirrors the Go matcher byte-for-byte (canonical values, dot-boundary suffix test, inet containment). Raises `GSUP1` (mapped to `DB_SUPPRESSED`) on violation. Fail-closed by construction.
- One suppression rule can match many assets (e.g. `domain_suffix example.com` matches all `*.example.com` assets). One asset can match multiple rules (different kinds/values). Winner: exact match beats suffix; among CIDRs/suffixes, most specific wins; tie → smallest rule id (deterministic audit).
- `suppression_applications` is the audit/application record: one row per enforced application (ingest-block, retroactive-retract, retract-source), with `detail` JSONB self-describing what was blocked/retracted. Rule identity denormalized into `detail` so rows survive rule removal.
- Suppression applies to **assets** (asset-level). Service ingestion shares the suppression boundary only **indirectly**: the asset is gated when written; a suppression retraction deletes the asset row, cascading services and observations via ON DELETE CASCADE. There is no separate service-level suppression gate in v0.1.0; v0.2.0 must not invent one without a migration decision.

### Suppression kinds
| Kind | Matches | Canonical form |
|------|---------|----------------|
| `ip` | `kind='ip'` assets | netip-canonical IP |
| `cidr` | `kind='ip'` assets in range | masked CIDR |
| `domain` | `kind='domain'` exact | lowercase, no trailing dot, punycode |
| `domain_suffix` | `kind='domain'`/`'hostname'` exact + label-boundary descendants | lowercase, no trailing dot, punycode |
| `source` | `data_sources.name` exclusion | exact source name |

- Cross-type propagation (a domain rule also blocking its resolved IPs) is deliberately NOT automatic — a policy decision, not syntactic.
- Expired rules (`expires_at <= now()`) stop matching immediately but remain listed for audit; never auto-deleted.

## 7.2 Retention policy

- **No automatic retention deletion.** No background job, no TTL, no `expires_at` on observations. v0.1.0 has none; v0.2.0 adds none.
- **No automatic observation expiration.** Observations are never expired based on age.
- **Explicit administrative deletion is allowed.** An asset or service row can be explicitly deleted; `ON DELETE CASCADE` deletes its observations (services→observations, assets→services→observations). This is schema behavior, not automatic retention.
- **Suppression retraction** deletes records by suppression match (not by provenance), and is orthogonal to the fixture purge (which is scoped by `provenance='fixture'` + fixture source id).

## 7.3 v0.2.0 schema preservation

Preserve the existing schema in v0.2.0 unless a justified change is separately approved. No retention migration is proposed in Revision 5.

## 7.4 What happens to observations when an asset/service is explicitly deleted

- Asset deleted → `ON DELETE CASCADE` deletes its services, and services' observations; also the asset's direct observations. All observations referencing the asset (directly or via its services) are cascade-deleted by PostgreSQL.
- Service deleted → its observations cascade-deleted.
- This is explicit administrative deletion, not automatic expiration.

## 7.5 Asset relationships (long-term vision, deferred)

- No explicit relationship/edge table in v0.1.0. Deferred from v0.2.0 scope.
- Future requirement (if approved): relationship table with `type`, `source_asset_id`, `dest_asset_id`, `provenance`, `observed_at`, `evidence`, optional `confidence`. A shared IP / FK / common provider / matching attribute does NOT automatically establish a semantic relationship.
- No graph database.
