# Section 8 — Virtual Hosting and Endpoint Identity

## 8.1 Existing endpoint uniqueness constraint (verified)

`services` table:
```sql
CONSTRAINT services_asset_port_transport_unique UNIQUE (asset_id, port, transport)
```
- Network endpoint identity = `(asset_id, port, transport)`.
- There is **no** hostname / SNI / HTTP Host column on `services` or `observations`.
- `search_index`/`Row` has no `sni`, `http_host`, or `hostname`-of-service column.

## 8.2 The virtual-hosting problem

Several hostnames can share the same IP and port (e.g. `a.example.com:443` and `b.example.com:443` on the same IP). The current schema represents this as ONE service row `(asset=IP, port=443, transport=tcp)`. Different hostnames/SNI/Host cannot be distinguished as separate services.

## 8.3 Distinctions (Revision 5 must not collapse these)

| Concept | Where it lives (v0.1.0) | Notes |
|---------|------------------------|-------|
| Network endpoint identity | `services.(asset_id, port, transport)` | The unique service identity. |
| DNS name | `assets.value` (kind=hostname/domain) or observation evidence | A separate asset, not part of service identity. |
| TLS SNI | Not stored | Absent from schema. |
| HTTP Host authority | Not stored | Absent from schema. |
| Observation context | `observations.evidence` JSONB | Authority-specific details (SNI, Host, server cert) can live here. |

## 8.4 Policy decision (Revision 5)

**Preserve endpoint identity and record authority-specific details in observation evidence.** Do NOT create duplicate services merely to represent different hostnames. Do NOT silently change the `(asset_id, port, transport)` uniqueness constraint.

- Different hostnames resolving to the same IP:port map to ONE service row. The hostname is a separate `kind='hostname'` asset (if observed) and/or recorded in `observations.evidence`.
- SNI / HTTP Host observed during a scan/collection are stored in `observations.evidence` (raw source evidence), not as new service columns.
- Search can filter by `domain`/`hostname` asset and by `port`/`transport` service independently; it cannot (yet) filter "service on IP:443 with SNI x" because SNI is not a service column.

## 8.5 Limitation and migration proposal

**Limitation:** The current schema cannot represent SNI-specific or Host-specific service distinctions as separate service rows. This is a documented limitation, not a bug.

**Migration proposal (separate from v0.2.0 no-migration baseline):** If SNI/Host distinction is required, add a nullable `sni` / `http_host` column to `services` and extend the uniqueness to `(asset_id, port, transport, COALESCE(sni,''), COALESCE(http_host,''))`. This is a **post-v0.2.0 migration proposal**, requires separate owner approval, and is NOT part of Revision 5's implementation baseline.

## 8.6 Do not
- Do not create duplicate services for different hostnames sharing an endpoint.
- Do not silently change the existing uniqueness constraint.
- Do not claim the schema represents virtual-host-specific services when it does not.
