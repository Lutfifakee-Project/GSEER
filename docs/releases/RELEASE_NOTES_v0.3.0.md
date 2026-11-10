# GSEER v0.3.0 — Zero-Config First Run, Personal Collection, New Identity

> GSEER — internet-asset search engine
> Search internet assets. Collect authorized data. Store locally. Search easily.

v0.3.0 is the **easy-to-use release**: the first run needs one command,
personal mode can finally collect real data, and the product carries
its new identity everywhere.  No security boundary is weakened; the
hardened self-hosted PostgreSQL paths are behavior-identical.

## New identity

GSEER is now positioned as the **internet-asset search engine**.  The
former intelligence-platform tagline is retired from the README, CLI,
dashboard, and documentation.  External data providers are
named only where data provenance must be transparent — the data-source
review register, `docs/SOURCES.md` (unchanged policy, one exception by
design).

## Zero-configuration first run

```bash
go install -v github.com/Lutfifakee-Project/gseer/cmd/gseer@latest
gseer
```

The bare command now:

1. initializes **personal mode automatically** when no config exists —
   one embedded SQLite file (`~/.gseer/gseer.db`, `0600`), migrations
   applied, config written — through the SAME code path as
   `gseer setup --personal` (no second initialization to drift);
2. tells the user exactly how data enters the index (synthetic fixtures
   for evaluation, registered sources for real data);
3. on later runs prints an honest status panel: profile, database,
   index counts, and the next useful commands;
4. fixes `gseer --version` — the flag existed since M1 but was never
   consumed.

## Collection works in personal mode

Before v0.3.0, personal-mode users could only load synthetic fixtures:
the collector engine was wired to the concrete PostgreSQL store.  The
engine now runs against a typed storage `Backend` contract satisfied by
BOTH engines, so the full collection surface works against the single
SQLite file:

```bash
gseer source add my-dns --type dns --seed-mode config \
    --seeds example.net,example.org --enable --yes
gseer collect run --source my-dns
gseer collect status
gseer collect redrive --all
```

Suppression, checkpoints, job bookkeeping, dead letters, and
fingerprint enrichment behave identically on both engines.  The client
profile (no local engine) is still refused with `E_CLIENT_ONLY`.

## New connector: `ptr` (reverse DNS)

`ptr` resolves PTR records for addresses the operator named (or that
are already in the index) through the operator's resolver, recording
hostname sightings with provenance and evidence.  Terms verdict:
**reviewed-enabled** on the same basis as the `dns` connector —
standard protocol, operator-named targets, uncopyrighted protocol
facts (`docs/SOURCES.md` §E).  No CIDR sweeps, no neighbor enumeration,
no third-party resolver dependency by default.

The reviewed connector set is now: `dns`, `ptr`, `ip2asn` (enableable);
`rdap`, `ct` (disabled until their providers' terms clear — enforced in
code).

## README rebuilt user-first

The README now follows the reader's order: what GSEER is → install →
quick start → search examples → syntax → data collection → storage and
backup → configuration → troubleshooting → uninstall.  The fastest
path from `go install` to a first search result is the top of the
document.

## Upgrade notes

- From v0.2.x: nothing to migrate by hand.  Personal databases keep
  their schema (head v1); PostgreSQL deployments stay at schema v17.
- The CLI's `source`/`collect` commands on a personal profile used to
  refuse with `E_PROFILE_UNSUPPORTED`; they now work.  Scripts relying
  on the refusal must adapt (the refusal was a limitation, not a
  contract).
- `gseer collect status` output format is unchanged; it is served by
  the store contract now instead of ad-hoc SQL.

## Verification

- Full unit suite green across all packages; `-race` clean.
- Personal-mode collection proven end-to-end on the real engine
  (registry, terms gate, job bookkeeping, fact pipeline, re-drive).
- Fresh-install acceptance replayed against the built binary:
  bare `gseer` → doctor → load-fixtures → search → backup/restore.
- Brand scan: no external engine brands outside `docs/SOURCES.md`; the
  retired tagline is absent from the working tree.
