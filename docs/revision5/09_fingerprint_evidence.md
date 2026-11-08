# Section 9 — Fingerprinting Evidence Model

## 9.1 Seed rules enumeration (verified from `internal/fprule/seed/`)

The seed directory contains **22 rule files** (Revision 4's "22 rules" claim is correct):

| # | File | Rule ID | Family | Product |
|---|------|---------|--------|---------|
| 1 | web-server-apache.yaml | web-server-apache | web-server | Apache |
| 2 | web-server-caddy.yaml | web-server-caddy | web-server | Caddy |
| 3 | web-server-envoy.yaml | web-server-envoy | web-server | Envoy |
| 4 | web-server-gunicorn.yaml | web-server-gunicorn | web-server | Gunicorn |
| 5 | web-server-iis.yaml | web-server-iis | web-server | IIS |
| 6 | web-server-jetty.yaml | web-server-jetty | web-server | Jetty |
| 7 | web-server-kestrel.yaml | web-server-kestrel | web-server | Kestrel |
| 8 | web-server-lighttpd.yaml | web-server-lighttpd | web-server | Lighttpd |
| 9 | web-server-litespeed.yaml | web-server-litespeed | web-server | LiteSpeed |
| 10 | web-server-nginx.yaml | web-server-nginx | web-server | nginx |
| 11 | web-server-openresty.yaml | web-server-openresty | web-server | OpenResty |
| 12 | web-server-tomcat.yaml | web-server-tomcat | web-server | Tomcat |
| 13 | web-server-tornado.yaml | web-server-tornado | web-server | Tornado |
| 14 | web-server-werkzeug.yaml | web-server-werkzeug | web-server | Werkzeug |
| 15 | web-tech-cloudflare.yaml | web-tech-cloudflare | web-tech | Cloudflare |
| 16 | web-tech-varnish.yaml | web-tech-varnish | web-tech | Varnish |
| 17 | web-tech-vercel.yaml | web-tech-vercel | web-tech | Vercel |
| 18 | cms-wordpress.yaml | cms-wordpress | cms | WordPress |
| 19 | framework-django-admin.yaml | framework-django-admin | framework | Django Admin |
| 20 | language-php.yaml | language-php | language | PHP |
| 21 | language-python.yaml | language-python | language | Python |
| 22 | service-openssh.yaml | service-openssh | service | OpenSSH |

Per-rule detail (required evidence fields, fixtures, confidence) is asserted by each rule's own `Tests` block (≥1 positive, ≥1 negative) and linted by `fprule.Lint`. Positive/negative fixture coverage is encoded in the YAML `tests` blocks, not in a separate fixture manifest. **Revision 5 does not claim real-world precision or recall from synthetic fixtures** — coverage is regression-level only.

## 9.2 Where fingerprint results are stored / exposed

- **Ingestion:** `enrichEvidence` (fingerprint.go) merges `fingerprint` into the **stored evidence copy** when an engine is attached and healthy:
  ```json
  "fingerprint": {
    "technologies": ["nginx", ...],   // plain product names, confidence order
    "derived": [ {"rule_id","family","product","version","confidence"} ]
  }
  ```
  Stored in `observations.evidence` (JSONB). The caller's evidence map is never mutated; `PayloadHash` (L2 identity) is computed from the RAW record and never affected. A source-provided `fingerprint` key always wins (engine fills an absence only).
- **Search:** `search_index.technologies` is a `text[]` column; `technologies` is a queryable/list GQL field (CONTAINS element match) and a facet dimension. `Row.Technologies []string`.
- **The engine is NOT invoked at search time.** It only enriches at ingestion (when attached via config/fingerprint engine). `gseer collect run` attaches it.

## 9.3 Data boundaries (Revision 5 resolution)

| Concept | Definition | Where stored |
|---------|------------|--------------|
| Raw source evidence | What the source actually delivered | `observations.evidence` (verbatim, never overwritten) |
| Source-provided service name/product/version | Source's own identification | `services.name/product/version` (source-authoritative) |
| Source-provided fingerprint info | Source's own `fingerprint` key if present | `observations.evidence["fingerprint"]` (source wins) |
| GSEER-derived classification | `fprule` engine output over evidence | `observations.evidence["fingerprint.technologies"]` + `.derived` (derived, never overwrites source) |
| Rule version + provenance | Rule `ID`, `Family`, `Product`, `Version`, `Provenance` | In `fingerprint.derived` entries; rule registry itself |

### Integrity rules (do not violate)
1. **Do not overwrite source evidence.** Enrichment is copy-on-write; the raw evidence map is preserved.
2. **Do not overwrite authoritative source-provided identification with a weaker inferred result.** A source-provided `fingerprint` key wins; source-provided `product`/`version` on the service row are authoritative.
3. **Do not claim real-world precision/recall from synthetic fixtures.** Fixture tests are regression coverage only.

## 9.4 `fingerprint.technologies` storage resolution

Revision 4 did not sufficiently define how `fingerprint.technologies` is stored. Resolution:
- Stored as a nested JSON object under `observations.evidence["fingerprint"]["technologies"]` (a `[]string` of plain product names, confidence order) and `["fingerprint"]["derived"]` (detail array).
- Projected into `search_index.technologies` (text[]) so it is queryable/facetable. The projection populates `technologies` from the stored evidence's `fingerprint.technologies`.
- Derived results are attached to the observation's own row; they never create a service, port, or protocol.

## 9.5 Architectical options if derived results cannot be persisted without ambiguity

The existing schema supports derived fingerprint storage via `observations.evidence` JSONB + `search_index.technologies` text[] with no ambiguity, because evidence is extensible JSONB. **Recommended (least disruptive):** keep the current `evidence["fingerprint"]` structure; no new column or table needed. A separate `service_fingerprints` table is NOT proposed for v0.2.0 (would require a migration and is not justified by current usage).
