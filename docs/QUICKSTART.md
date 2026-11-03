# GSEER — Quickstart (v0.1.0)

> Canonical fresh-install workflow for the self-hosted release.  Every
> step below is implemented and exercised by the test suites;
> `gseer setup --with-compose` writes the compose + `.env` and prints
> the next commands verbatim (QS-2), and `--full` writes the complete
> db + server stack (`docs/DOCKER.md`).  A quickstart-linux CI job pins
> this flow (AC-5.11).

```bash
# 0. Install the binary (docs/INSTALL.md), then:
gseer version                              # prints version/commit/build

# 1. Provision the PostgreSQL dependency
gseer setup --self-hosted --with-compose   # writes docker-compose.yml + .env (random password, chmod 600)
docker compose up -d db                    # DB now running (healthcheck-gated)
docker compose ps db                       # expect: healthy

# 2. Initialize the schema (runs ALL migrations to head; zero manual SQL)
gseer setup --self-hosted --db-url "$(grep '^GSEER_DSN=' .env | cut -d= -f2-)"

# 3. Verify — every check must report PASS; the index is reported as EMPTY
gseer doctor

# 4. First search — honest empty state (exit code 0)
gseer search 'port==443'
#   → The index is empty — no assets have been ingested yet.

# 5. Start the API + dashboard (migrations run automatically, QS-7)
gseer server start                         # http://127.0.0.1:8443 (loopback by default)
#   Dashboard:  http://127.0.0.1:8443/
#   API:        http://127.0.0.1:8443/api/v1/health
#   Search API: curl 'http://127.0.0.1:8443/api/v1/search?q=port%3D%3D443'
#   Stop:       Ctrl-C, or `gseer server stop` from another terminal

# 6. OPTIONAL: load the labeled synthetic fixture dataset for evaluation
#    (QS-6 gate: needs policy.evaluation_fixtures: true in the config,
#    or an interactive y/yes confirmation; --purge-fixtures asks the
#    same way — piped scripts without the policy opt-in are refused)
gseer setup --load-fixtures
gseer search 'port==443 && title CONTAINS "login"'
#   Every surface labels the data: SYNTHETIC EVALUATION DATA — not real
#   internet observations.  Doctor reports it too (HINT, with counts).
gseer setup --purge-fixtures               # remove fixtures again
```

## The GQL one-minute tour

```bash
gseer search 'port=443'                          # `=` is sugar for `==`
gseer search 'title="phpMyAdmin"'                # strings are double-quoted
gseer search 'server="nginx" && port=443'        # boolean combine
gseer search 'ip IN_CIDR "203.0.113.0/24"'       # CIDR containment
gseer search 'domain IN_DOMAIN "example.net"'    # apex + subdomains
gseer search 'ANY(host){ http.status == 200 }'   # host has ANY service with 200
gseer search 'port==443' --mode asset            # one row per asset
gseer search 'transport=="tcp"' --limit 2        # pagination cursor in the output
gseer search 'port==443' --explain               # AST → branch → SQL plan
gseer search 'port==443' --validate              # validate only, no DB contact
```

> **Do not run steps 1–6 against a production database.** On a clean
> install the honest empty-search message (QS-5) is the only output
> until data exists, and fixture data is always labeled as synthetic.
> Search never scans — every search path is read-only against the local
> index.

## v0.1.0 surfaces — facets, export, fingerprints, updates

```bash
# Faceted search: aggregates over the SAME GQL filter and SearchService
gseer search 'port == 443' --facets
gseer search 'stale == "false"' --facets --facet port,protocol,country --max-values 5
#   → port 443 (2), protocol http (1), https (1), country DE (1), US (1), …

# Export: streamed JSON / CSV / NDJSON (cursor-stable, page-by-page)
gseer export 'stale == "false"' --format csv --out fixtures.csv
gseer export 'stale == "false"' --format ndjson --out -   # stdout (stats on stderr)
#   Large results stream page-by-page; --max caps a run (0 = unlimited).

# Fingerprint rules (technology classification over observed evidence)
gseer rules list                          # the lint-gated seed collection
gseer rules validate                      # lint + false-positive regression tests
gseer rules validate --dir my-rules/      # validate a contribution draft

# Dead-letter re-drive (the operator's second chance; explicit)
gseer collect status                      # shows pending dead letters
gseer collect redrive --all               # rebuild + re-ingest through the same pipeline
gseer collect redrive --source my-dns --discard

# Signed updates (docs/UPDATE.md — key ceremony, recovery)
GSEER_UPDATE_MANIFEST_URL=https://example.test/manifest.signed.json gseer update --check
gseer update --apply                      # verify → download → checksum → atomic replace
```

API parity: `GET /api/v1/facets` and `GET /api/v1/export` expose the
same SearchService semantics over HTTP (token-authed like `/search`).

## Passive collection — explicit, terms-reviewed, never automatic

Nothing contacts a provider unless YOU register a source, enable it, and
run it.  Every connector carries a terms verdict (`docs/SOURCES.md`);
`dns` and `ip2asn` are reviewed-enabled, `rdap` and `ct` are implemented
but held disabled pending provider authorization.

```bash
# 7. Register a passive source (targets = names you configured, or
#    assets already in your index — never a third-party target list)
gseer source add my-dns --type dns --seed-mode config \
    --seeds example.net,example.org --enable

# 8. Run it once (explicit; no daemon, no scheduler in GSEER)
gseer collect run --source my-dns

# 9. Search what landed — discovery facts are asset-level only
gseer search 'domain IN_DOMAIN "example.net"'
gseer search 'asn==64512'

# 10. Bookkeeping: job stats, checkpoints, dead-lettered records
gseer collect status

# 11. Opt out of a class of records BEFORE it enters the index
gseer suppress add domain_suffix internal.example --reason "our own space"
gseer suppress add cidr 10.0.0.0/8 --reason "RFC1918"
gseer suppress list
```

What a collector run guarantees:
- **Suppression is enforced before ingestion** — and the database itself
  refuses suppressed writes if any path ever forgets the gate.
- **Idempotency** — re-running is a no-op for identical facts (L1/L2
  identities); re-sightings age `last_seen` forward.
- **Provenance on every record** — `provenance=dns|ip2asn|...`, provider
  evidence, and a confidence label land in each observation.
- **Discovery ≠ observation** — passive facts never invent ports, HTTP,
  TLS or banner data; they appear as `row_kind=asset-only`.
