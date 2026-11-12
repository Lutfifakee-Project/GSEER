# GSEER — Fingerprint Rules

The fingerprint rule engine classifies technology evidence that a stored
observation **already carries** into products with confidence. It is the
machinery behind `search_index.technologies` for real data: rules match
over `http.server`, `http.title`, `tls.*` and `ssh.banner` payloads —
never over the network, never over an asset they did not observe.

This document is the **contribution format** and the confidence
contract. The embedded seed collection lives in
`internal/fprule/seed/*.yaml`; every rule there passes the gates below
enforced by CI (`internal/fprule/seed_test.go`) and by
`gseer rules validate`.

## 1. Honesty constraints (non-negotiable)

- **Identification only.** A rule names software it sees. Rules that
  encode exploitation, bypass, credential or exfiltration payloads are
  rejected by the linter (`internal/fprule/rule.go` →
  `lintExploitationPatterns`) and by review. No CVE signatures, no
  "vulnerable version" markers — GSEER is not a vulnerability scanner.
- **Evidence-only matching.** The matchable field set is closed:
  `http.server`, `http.title`, `tls.version`, `tls.cert_subject_cn`,
  `tls.cert_issuer_cn`, `ssh.banner`. A field the storage layer does not
  store cannot be matched — proposals to extend the set must extend the
  observation schema first.
- **The source always wins.** If a record already carries a
  `fingerprint` payload, the engine never overwrites it.
- **Discovery ≠ Observation.** A technology hit never creates a port, a
  service, or a protocol row. It attaches to the observation that
  carries the evidence.
- **Fail-closed.** A registry with any compile problem classifies
  nothing (`Registry.Apply` returns nil). A broken contribution can
  never silently half-run.

## 2. Rule format (one file per rule: `<id>.yaml`)

```yaml
id: web-server-nginx              # kebab-case, stable, = file name
family: web-server                # web-server | cms | language |
                                  # framework | web-tech | service
product: nginx                    # canonical name = technologies[] entry
confidence: 0.95                  # see §4 tiers
provenance: |                     # REQUIRED — the audit story
  nginx sends "Server: nginx/<version>" by default (nginx docs,
  http core module).  Anchored pattern; derivatives that keep a
  different token ("nginx-proxy") do not match.
matches:                          # ≥1 alternative; a rule fires on ANY hit
  - field: http.server
    pattern: '^nginx(?:/(\d+(?:\.\d+)*))?(\s*\(|$)'   # Go RE2
    version_group: 1              # capture group → version (0 = none)
    weight: 0.9                   # optional, (0,1]; default 1.0
tests:                            # ≥2: ≥1 positive + ≥1 false-positive guard
  - evidence: {http: {server: "nginx/1.24.0"}}
    match: true
    version: "1.24.0"             # optional assertion
  - evidence: {http: {server: "openresty/1.21.4.1"}}
    match: false                  # the guard that keeps confidence honest
```

Field notes:

- `pattern` is Go RE2 (no lookarounds). Prefer anchored patterns with
  explicit boundaries — RE2 cannot express "not followed by", so encode
  the boundary positively (e.g. `(\s*\(|$)`).
- `weight` scales confidence per alternative: the specific match that
  hit supplies `confidence × weight`.
- `version_group` is 1-based over the pattern's capture groups.
- `tests[].evidence` is the same shape the storage layer stores.

## 3. The engine

- A rule fires when **any** of its `matches` hits (OR). Within one rule
  the best hit (highest `weight`, spec order breaking ties) supplies
  version and confidence.
- Across rules one product survives: the highest-confidence hit wins;
  ties break by rule id (deterministic).
- Output (`Registry.Apply`) is sorted by confidence desc, then product
  asc. At ingestion, `fingerprint.technologies` stores the plain
  product names (the search `CONTAINS` operator is array membership:
  `technologies CONTAINS "nginx"`), and `fingerprint.derived` keeps the
  full `{rule_id, family, product, version, confidence}` records for
  audit.

## 4. Confidence tiers

| Tier | Range | Evidence basis | Examples |
| --- | --- | --- | --- |
| Header-is-product | ≥ 0.90 | The Server/banner token IS the product | nginx 0.95, OpenSSH 0.95, cloudflare 0.95 |
| Header-embedded / conditional | 0.70–0.89 | Product token embedded in or emitted by another component | PHP 0.85, Werkzeug 0.85, Caddy 0.8 |
| Weak title signal | 0.50–0.69 | Documented default page titles only | Tomcat 0.7, Django admin 0.6 |

Rules below 0.5 are rejected in review: if the evidence is weaker than
a documented default title, it is not evidence. Title rules must anchor
on the exact default string — a page *mentioning* a product is not the
product.

## 5. Validation gates

```
gseer rules list                  # the loaded collection (seed + rules_dir)
gseer rules validate              # lint + regression tests, exit 0 clean / 2 problems
gseer rules validate --dir DIR    # validate a contribution draft before the PR
gseer rules validate -o json      # machine-readable problems
```

`rules validate` fails on: duplicate ids, unknown families/fields,
non-compiling patterns, version groups out of range, missing
provenance, missing positive or false-positive tests, and every test
failure. CI runs the same gates on the embedded seed
(`TestSeedLintsCleanAndPassesItsTests`), so a regression to the seed set
cannot merge.

## 6. Contributing

1. Copy a seed rule as the template; keep one rule per file named
   `<id>.yaml`.
2. Write the provenance: which observable the pattern encodes and where
   its format is documented. No provenance, no merge.
3. Include the false-positive guard: the near-miss that shares the
   prefix (nginx↔openresty, Apache↔Apache-Coyote, gunicorn↔unicorn are
   the canonical examples — every seed rule pins its neighbor).
4. `gseer rules validate --dir your-rules/` must pass.
5. Open the PR with a sample of the real evidence shape (redacted) that
   motivated the rule.

The seed set is deliberately conservative. CMS/framework identification
usually needs page-body or generator signals that GSEER's passive
evidence model does not carry (Discovery ≠ Observation); the families
grow as observation-capable authorized sources (P2) land — not before.
