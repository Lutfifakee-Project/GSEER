# GSEER — Passive Data Source Review Register

> v0.0.2 Passive Intelligence (owner directive §2).  This document is the
> review register every connector's `Review()` verdict anchors to.  A
> connector may only carry the verdict `reviewed-enabled` when the
> provider's terms clearly permit automated collection AND indexing of
> the retrieved data; everything else ships `disabled` — the runner
> refuses it (`E_SOURCE_TERMS`) even if a database row says enabled.
>
> Review date: 2026-11-03.  Providers change terms; re-review before
> flipping any verdict, and after any provider policy update.

## Verdict summary

| Connector | Provider | Verdict | One-line reason |
| --- | --- | --- | --- |
| `dns` | operator-configured resolver (system default) | **reviewed-enabled** | standard DNS recursion; the operator names every target; answers are protocol facts |
| `ip2asn` | Team Cymru IP-to-ASN (DNS interface) | **reviewed-enabled** | explicitly free community service; DNS interface requested "at volume"; conservative rates + caching |
| `ptr` | operator-configured resolver (system default) | **reviewed-enabled** | standard reverse-DNS (PTR) recursion; same basis as `dns` — operator-named addresses, protocol facts |
| `rdap` | RIR registration data (ARIN/RIPE NCC et al.) | **disabled** | terms do not grant indexing/redistribution; needs written authorization |
| `ct` | crt.sh (Sectigo CT aggregation) | **disabled** | no published terms covering automated collection or indexing/reuse |

Rejected outright (documented, no connector code): bgp.tools (scraping
banned without a contract; bulk API is a paid product), RIPEstat
(redistribution prohibited by the service T&C), Censys (account +
commercial terms; indexing derived data not granted on free tiers).

---

## §A Certificate Transparency — crt.sh (connector `ct`)

1. **Source identity / endpoint**: crt.sh, operated by Sectigo Limited —
   https://crt.sh/ (JSON API: `https://crt.sh/?q=<name>&output=json`;
   direct PostgreSQL interface advertised for power users).
2. **Access method**: HTTP GET (JSON), plus an advertised public
   PostgreSQL endpoint (`crt.sh:5432`, user `guest`, db `certwatch`).
3. **API key requirements**: none.
4. **License / terms of use**: none published for the service or its
   data.  The site footer asserts "© Sectigo Limited … All rights
   reserved".  No licence for the aggregation is stated anywhere on the
   site (verified 2026-11-03).
5. **Permission for automated collection and indexing**: **not
   established**.  No terms document grants (or forbids) automated
   querying, caching, storing or re-publishing of the aggregation.
   Community tooling treats the JSON endpoint as open, but folklore is
   not permission.
6. **Rate limits**: none documented (the service is known to be
   slow/oversubscribed at times).
7. **Data retention**: not documented (the underlying certificate data
   is retained per CT log operator policy; crt.sh's own retention is
   unpublished).
8. **Privacy**: CT entries are public by design (RFC 6962/9162) and
   contain certificate fields (SANs, organization names) — no resolver
   or query-side data.
9. **Provenance**: Sectigo's aggregation over the public CT log ecosystem
   (certificates and precertificates submitted to publicly trusted logs).
10. **Freshness**: continuous ingestion from logs; visible lag is common
    (log batch timing, crt.sh backlog).
11. **Known limitations**: duplicate entries per cert (multi-log),
    precertificate/certificate duplication, wildcard names need
    normalization, no liveness or IP data, CT only covers
    publicly-trusted certificates.
12. **Verdict: `disabled`** — unclear permission for indexing/reuse
    (owner directive §2: keep disabled).  The connector is implemented
    and unit-tested; flipping `Review()` is a one-line, owner-visible
    change after the terms question is settled (or after switching to a
    provider with explicit API terms — e.g. an account-based CT API with
    a written licence; noted for the owner, not actioned).

## §B RDAP — RIR registration data (connector `rdap`)

1. **Source identity / endpoint**: the five RIR RDAP services, discovered
   via the IANA bootstrap registries —
   https://data.iana.org/rdap/ipv4.json, …/ipv6.json, …/dns.json
   (RFC 9224); e.g. https://rdap.arin.net/registry/,
   https://rdap.db.ripe.net/.
2. **Access method**: HTTP GET (RFC 9082/9083 JSON), no auth.
3. **API key requirements**: none (anonymous per-query access).
4. **License / terms of use**: each RIR applies its own registration-data
   terms — ARIN: https://www.arin.net/resources/registry/whois/tou/;
   RIPE NCC data-service terms (and RIPEstat T&C for RIPEstat);
   APNIC/LACNIC/AFRINIC publish analogous whois/RDAP terms.
5. **Permission for automated collection and indexing**: **not granted
   for GSEER's use**.  ARIN's ToU permits querying for enumerated
   purposes (routing policy evaluation, operational coordination,
   reverse-DNS, own-business use *without republishing*, scientific
   research into network operations, abuse processing) and states:
   "Unless ARIN provides specific prior written authorization, no other
   use of the Whois Service other than as expressly described in these
   Terms is implied or permitted", with an explicit prohibition on
   making Whois-database data "publicly available" outside those uses,
   and a commercial-service prohibition.  RIPE NCC's terms prohibit
   re-packaging, compiling and re-distributing the data.  Indexing the
   answers into a GSEER search index is redistribution the terms do not
   grant.
6. **Rate limits**: RIR-specific, enforced ad hoc (ARIN asks courtesy
   rates; RIPE documents daily limits for its services).  Not uniform —
   one more reason the verdict is not "enabled".
7. **Data retention**: registry-defined; not GSEER-controllable.
8. **Privacy**: RDAP responses can contain registrant/abuse contact
   objects (organizations, role mailboxes).  GDPR-sensitive fields are
   redacted by the registries to varying degrees; redistribution of the
   non-redacted remainder is exactly what the terms restrict.
9. **Provenance**: the registries' own registration objects (networks,
   ASNs, organizations).
10. **Freshness**: registration data changes slowly; no change-feed
    guarantees on anonymous RDAP.
11. **Known limitations**: coverage differs per RIR; in-registry data
    quality varies; bootstrap file is updated independently of the RIRs.
12. **Verdict: `disabled`** — needs the provider's written authorization
    before GSEER may index the data.  The connector (IANA bootstrap →
    RIR lookup → country/name/handle/range enrichment) is implemented
    and unit-tested for the day the owner secures authorization.

## §C Public DNS — operator-domain resolution (connector `dns`)

1. **Source identity / endpoint**: the DNS resolution system — the
   operator's configured resolver (default: the operating system's
   resolver; overridable per source via `resolver: host:port`).  GSEER
   deliberately does NOT hardcode any third-party resolver.
2. **Access method**: standard UDP/TCP 53 recursive queries (the same
   protocol every internet-connected system speaks).
3. **API key requirements**: none.
4. **License / terms of use**: DNS is a protocol, not a licensed
   dataset; recursive resolution is the resolver's normal purpose.
   Public resolvers that operators may configure publish acceptable-use
   expectations for standard usage (Cloudflare 1.1.1.1, Google Public
   DNS) — ordinary programmatic resolution is the service working as
   intended.  Operators who point GSEER at a specific public resolver
   are responsible for that provider's AUP; the default (system
   resolver) is the operator's own infrastructure.
5. **Permission for automated collection and indexing**: resolution
   answers (A/AAAA/CNAME/MX/NS records) are uncopyrighted protocol facts;
   GSEER records the factual answer with its lookup time.  Queries are
   bounded by the seed list — only names the operator configured or
   that are already in the operator's index are resolved.
6. **Rate limits**: self-imposed conservative caps (default client
   1 request/second; per-source `requests_per_second` /
   `limit_per_run`).  No third-party quota is consumed by default.
7. **Data retention**: GSEER keeps the answer as an asset fact with
   first_seen/last_seen (the index IS the cache); TTLs are honored by
   the resolver, not re-implemented.
8. **Privacy**: DNS queries reveal which names the operator resolves to
   the resolver operator — the standard privacy property of recursive
   DNS.  Using the default system resolver keeps that with the
   operator's own infrastructure.  Resolved data is about domains, not
   persons.
9. **Provenance**: `provenance = "dns"` on every observation; evidence
   carries the query name and record type.
10. **Freshness**: the observation time IS the lookup time; re-running
    the connector ages last_seen forward (stale flag clears).
11. **Known limitations**: recursive answers may come from cache (hence
    confidence `medium`); negative answers (NXDOMAIN) are not facts and
    are not stored; MX/NS lookups are best-effort per seed (a lookup
    miss on a seed whose A lookup succeeded is ordinary DNS — an apex
    without MX is normal — and never fails the pass); TXT records are
    deliberately NOT collected (free-form strings with no typed,
    searchable column — collecting them would be evidence-only noise,
    not searchable observations); DNS data says nothing about
    reachability of services (discovery ≠ observation — no
    port/HTTP/TLS/banner data is invented).
12. **Verdict: `reviewed-enabled`** — clear terms (standard protocol,
    operator-named targets, self-imposed rates), no third-party
    dependency by default.

## §D ASN/BGP — Team Cymru IP-to-ASN (connector `ip2asn`)

1. **Source identity / endpoint**: Team Cymru's IP-to-ASN mapping
   community service — official page
   https://www.team-cymru.com/ip-asn-mapping; DNS interfaces
   `origin.asn.cymru.com` (IPv4), `origin6.asn.cymru.com` (IPv6), and
   `AS<n>.asn.cymru.com` (AS names); whois interface
   `whois.cymru.com` port 43 (bulk mode supported — GSEER uses the DNS
   interfaces only).
2. **Access method**: DNS TXT queries, one per identifier, plus one AS
   name lookup per hit.
3. **API key requirements**: none.
4. **License / terms of use**: published on the service page: "Free,
   forever.  IP to ASN mapping is part of Team Cymru's community
   services, underwritten by Team Cymru."  No redistribution licence is
   stated in terms; the answers are derived from the RIRs' public
   delegation data (routing publication facts: origin AS, prefix,
   country, registry, allocation date).
5. **Permission for automated collection and indexing**: the service is
   published FOR automated use — the page's stated expectation is that
   volume users take the bulk or DNS interface ("The only ask is that
   you use bulk mode or DNS at volume").  GSEER uses the DNS interface
   (the lighter one), answers are cached in the index (each identifier
   is asked once, then ages via last_seen), and runs are capped by
   `limit_per_run`.
6. **Rate limits**: no numeric quota published for the DNS interface;
   GSEER self-imposes the client default (1 req/s) and a per-run budget.
   The bulk interface exists precisely for higher volumes — if GSEER
   ever needs more, switching to documented bulk mode is the
   terms-compliant path.
7. **Data retention**: not documented; answers are point-in-time
   routing data retained by GSEER with observation timestamps.
8. **Privacy**: AS/prefix answers are network facts; no personal data.
   GSEER's queries reveal which IPs the operator enriches (standard
   DNS-protocol property; queries go to Team Cymru's authoritative
   servers).
9. **Provenance**: `provenance = "ip2asn"`; evidence keeps the raw TXT
   line, prefix, registry and allocation date; ASN/AS-name/country land
   in the asset's typed columns.
10. **Freshness**: routing data is point-in-time (`confidence:
    medium`); re-runs refresh last_seen.
11. **Known limitations**: the DNS interface does not vouch for live
    routing state at query time; multi-origin (MOAS) prefixes return
    one answer line (the first); AS-name parse is defensive — an
    unexpected format degrades to an empty name without blocking the
    ASN enrichment.
12. **Verdict: `reviewed-enabled`** — explicitly free community service
    published for this access method, conservative self-limits, cached
    answers.

## §E Reverse DNS — PTR records (connector `ptr`)

1. **Source identity / endpoint**: the DNS resolution system — the
   operator's configured resolver (default: the operating system's
   resolver; overridable per source via `resolver: host:port`),
   querying the standard `in-addr.arpa` / `ip6.arpa` reverse trees.
   GSEER deliberately does NOT hardcode any third-party resolver.
2. **Access method**: standard UDP/TCP 53 recursive PTR queries (the
   same protocol, and the same resolver, as the `dns` connector).
3. **API key requirements**: none.
4. **License / terms of use**: identical basis to §C — DNS is a
   protocol, not a licensed dataset; reverse resolution is the
   resolver's normal purpose.  Operators who point GSEER at a specific
   public resolver are responsible for that provider's AUP; the
   default (system resolver) is the operator's own infrastructure.
5. **Permission for automated collection and indexing**: PTR answers
   are uncopyrighted protocol facts (a name published for an address
   by its operator, via the address's delegation).  Queries are
   bounded by the seed list — only addresses the operator configured
   or that are already in the operator's index are resolved.  NO CIDR
   sweeps, NO neighbor enumeration: a seed list of one address is one
   query.
6. **Rate limits**: self-imposed conservative caps (default client
   1 request/second; per-source `requests_per_second` /
   `limit_per_run`).  No third-party quota is consumed by default.
7. **Data retention**: GSEER keeps the answer as a hostname sighting
   with first_seen/last_seen (the index IS the cache).
8. **Privacy**: reverse queries reveal which addresses the operator
   enriches to the resolver operator — the standard privacy property
   of recursive DNS, kept with the operator's own infrastructure by
   default.  PTR answers are about host naming, not persons.
9. **Provenance**: `provenance = "ptr"` on every observation; evidence
   carries the queried address and the raw answer name.
10. **Freshness**: the observation time IS the lookup time; re-running
    the connector ages last_seen forward (stale flag clears).
11. **Known limitations**: many addresses publish no PTR record (the
    empty answer is a non-fact and is not stored); recursive answers
    may come from cache (confidence `medium`); a PTR name says nothing
    about reachability of services (discovery ≠ observation — no
    port/HTTP/TLS/banner data is invented).
12. **Verdict: `reviewed-enabled`** — clear terms (standard protocol,
    operator-named targets, self-imposed rates), no third-party
    dependency by default.  Introduced in v0.3.0.

## Rejected / not implemented (watch list)

- **bgp.tools** — HTML scraping "may be banned with no notice"; bulk
  API is a paid product (starts £500/mo).  No connector.  Revisit only
  with a contract.
- **RIPEstat Data API** — T&C: "A User may not re-package, compile,
  re-distribute the RIPEstat Service and any (or all) of the RIPEstat
  Data"; commercial derivative use needs written permission.  Indexing
  into GSEER = redistribution risk.  No connector.
- **Censys** — account/API-key required; free tier terms do not grant
  indexing of derived data into third-party search products.  No
  connector.
- **VirusTotal / Farsight DNSDB / CIRCL pDNS** (passive DNS databases) —
  API keys and restrictive derived-data terms.  No connector.
- **Cert Spotter (SSLMate) CT API** — free tier requires signup and
  caps 100 queries/hour; commercial API terms.  Viable fallback if the
  owner accepts account-based access; not actioned in v0.0.2.

## How verdicts are enforced (architecture cross-reference)

- Every connector carries `Review()` (internal/collect/doc.go, contract 2);
  `collect.Runner.Run` refuses non-`reviewed-enabled` connectors with
  `E_SOURCE_TERMS` BEFORE opening a job row — a hand-flipped
  `enabled=true` database row cannot outrun it.
- `gseer source add --enable` and `gseer source enable` refuse at the
  CLI for the same reason.
- Registration stamps `data_sources.terms_ref` with the review reference
  so the registry row is self-describing.
- The unit test `TestBuiltinConnectorVerdicts` pins every verdict to this
  register — a verdict change without a register update fails CI.
