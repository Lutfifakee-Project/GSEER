# GSEER — Signed Update System

`gseer update` upgrades a GSEER binary through a **signed update
manifest**.  Trust is anchored in an Ed25519 public key **embedded in
the binary** — never in the URL a manifest came from.  An unsigned or
wrongly-signed manifest is refused no matter how familiar the download
URL looks (owner directive §5).

## 1. The update chain (in enforcement order)

| Step | Gate | Failure code |
| --- | --- | --- |
| 1 | Manifest Ed25519 signature verifies against the embedded key | `E_UPDATE_UNSIGNED` / `E_UPDATE_BAD_SIGNATURE` |
| 2 | Manifest version is strictly newer than the running build (semver, pre-release aware; `--force` overrides) | `E_UPDATE_NOT_NEWER` / `E_UPDATE_VERSION` |
| 3 | Manifest carries an artifact for the exact running GOOS/GOARCH | `E_UPDATE_PLATFORM` |
| 4 | Downloaded payload SHA-256 and size match the manifest | `E_UPDATE_CHECKSUM` |
| 5 | Atomic rename over the running binary, mode preserved, previous binary kept as `<binary>.bak`; any failure restores the backup | `E_UPDATE_REPLACE` (with automatic rollback) |
| 6 | Post-rename durability barrier: directory fsync on POSIX; the replacement itself is write-through on Windows | `E_UPDATE_DIRSYNC` (warning — see below) |

The download is capped (512 MiB), HTTP(S)-only, timeout-bounded, and
credential-redacting (userinfo URLs are never echoed into errors).
Everything the updater writes lives next to the target binary; nothing
runs automatically afterwards — `gseer server stop` + `server start`
picks up the new build.

If the rename itself succeeded but the directory fsync failed, the
updater reports `E_UPDATE_DIRSYNC`: the binary WAS replaced and nothing
was rolled back — `gseer update --apply` prints the success line plus a
`[warn]` with the fsync error and exits successfully.  The new binary's
data was already fsynced before the rename; only the directory entry is
in doubt, so after a crash or power loss the previous binary may
reappear.  Re-running the update makes the change durable again.  On
Windows the durability barrier is built into the replacement itself:
the updater replaces the binary via `MoveFileExW` with
`MOVEFILE_REPLACE_EXISTING|MOVEFILE_WRITE_THROUGH`, which does not
return until the moved file is actually on disk (Go's own `os.Rename`
calls plain `MoveFileW` with NO flags — never write-through — so the
updater performs the replace itself and does not use `os.Rename`).
Windows has no directory-handle flush; the `E_UPDATE_DIRSYNC` warning
therefore cannot occur there.

## 2. Commands

```bash
gseer update --check                  # verify manifest + report only (no download)
gseer update --apply                  # full chain: verify → download → checksum → replace
gseer update --apply --force          # allow same/older versions (re-install)
gseer update --manifest URL_OR_PATH   # override the configured channel
```

The manifest location comes from `update.manifest_url` (config file) or
`GSEER_UPDATE_MANIFEST_URL`.  Without a configured channel the command
fails with `E_UPDATE_NO_CHANNEL` and a pointer here.

**Update channel note (v0.3.1).** The signed-update *mechanism* is
implemented and shipped (since v0.1.0), but GSEER does **not** host an
official manifest or update feed: there is no default
`GSEER_UPDATE_MANIFEST_URL` and no `gseer update --check` success out of
the box: a fresh install has no active channel until the operator
provides and configures their own manifest URL (signed with the key
embedded in the binary, per §3–§4).
Until then, `gseer update` refuses with `E_UPDATE_NO_CHANNEL` — which is
intentional: GSEER never downloads or applies updates from an unconfigured,
unverified source.

Recovery: the previous binary is always kept at `<binary>.bak`.  If a
new build misbehaves, stop the server, move the old file back, start
again:

```bash
gseer server stop
mv gseer.bak gseer
gseer server start
```

## 3. Manifest format

```json
{
  "version": "v0.3.1",
  "released_at": "2026-11-03T00:00:00Z",
  "artifacts": [
    {"os": "linux", "arch": "amd64",
     "url": "https://download.example/gseer-v0.3.1-linux-amd64",
     "sha256": "<64 hex>", "size": 38777344}
  ],
  "notes_url": "https://github.com/.../releases/tag/v0.3.1",
  "signature": "<base64 Ed25519 over the canonical core bytes>"
}
```

The signature covers the **canonical core** — the manifest JSON without
the `signature` field, marshaled by Go's `encoding/json` (struct field
order is deterministic).  `gseer-release sign` performs exactly this;
the updater re-marshals the received core and verifies — any byte
changed after signing (including added fields) breaks verification.

## 4. Release process (maintainer)

```bash
go run ./cmd/gseer-release keygen -pub gseer-update.pub -priv gseer-update.key
#   → embed the PUBLIC key base64 into internal/update/keys.go
#     (EmbeddedUpdatePublicKeyB64) and release binaries built from it.
#   → the PRIVATE key (0600) stays with the release machine.  NEVER
#     commit it, never put it in CI secrets the whole team can read.
```

Per release:

```bash
# 1. compute artifact hashes
sha256sum gseer-v0.3.1-linux-amd64 gseer-v0.3.1-darwin-arm64 ...
# 2. write manifest.json (version, released_at, artifacts, notes_url)
# 3. sign
go run ./cmd/gseer-release sign -key gseer-update.key -in manifest.json -out manifest.signed.json
# 4. pre-release self-check (same verification the updater runs)
go run ./cmd/gseer-release verify -pub gseer-update.pub -in manifest.signed.json
# 5. publish manifest.signed.json + artifacts; point update.manifest_url at it
```

**Key rotation**: generate a new pair, embed the new public anchor, and
publish the new signed manifest; announce the rotation in the release
notes.  The `.bak` recovery path is independent of key state, so an
anchor rotation can never strand an installation mid-update.

## 5. Invariants (pinned by tests)

- `internal/update/update_test.go` — tampered/unsigned/foreign-key
  manifests are refused; a signed-but-wrong checksum stops the apply;
  version ordering (including `-rcN` pre-releases) is exact; platform
  selection never guesses; the replace is atomic with backup +
  rollback; URLs with credentials are redacted.
- The embedded anchor constant must decode to a valid Ed25519 public
  key (`TestBase64AnchorDecodes`) — a broken constant is a build error,
  not a runtime surprise.
- `internal/update/apply_test.go` + `apply_windows_test.go` — the
  durability contract: backup copy/sync/close error precedence, backup
  permission preservation, no temp-file leftovers, rollback (including
  failed-rollback reporting) and the dir-sync warning contract
  (replaced ≠ rolled back).  The Windows suite pins the replacement
  flags (`MOVEFILE_REPLACE_EXISTING|MOVEFILE_WRITE_THROUGH`) and runs
  on the `update-windows` CI job (windows-latest).
- `gseer update` on an un-injected `dev` build refuses to act
  (`E_UPDATE_VERSION`): a binary that cannot prove its version cannot
  prove an update is an upgrade.  Release builds stamp the version via
  `-ldflags` (Makefile).

## 6. Upgrading a v0.1.0 deployment to v0.2.0 (bare-metal, EU-6)

The signed-update channel is for BINARY updates; the v0.1.0 → v0.2.0
upgrade is a source + schema upgrade.  The path is automated by the
v0.2.0 `gseer setup --self-hosted` (it runs the full hardened order and
is idempotent — re-running is always safe):

1. **Backup** — `pg_dump -Fc` the v0.1.0 database (migrations are not
   auto-reversed; the rollback is restore + previous binary).
2. **Build v0.2.0** — `make build VERSION=v0.2.0`.
3. **Run the hardened setup once** with the existing operator DSN:

   ```bash
   gseer setup --self-hosted --db-url "$GSEER_DB_DSN"
   #   [ ok  ] PG version: 17 (minimum 16, OD-7)      ← PostgreSQL 16+ required (OD-7)
   #   [boot] bootstrap: created role gseer_policy_owner / gseer_app …
   #   [ ok  ] migrations: 2 applied, schema at head v17   ← exactly 0016 + 0017
   #   [ ok  ] runtime credential: password set for gseer_app …
   #   [ ok  ] privilege audit: PASSED (fail-closed gate, OD-5)
   ```

   The ledger goes `(6, false)` → `(17, false)`; 0007–0015 are
   permanently reserved (OD-4) and are never created.  When the audit
   fails, the output names every failure — repair the deployment
   (`docs/DEPLOYMENT.md` §3.4), never the check.
4. **Export the runtime credential** — the DSN printed in step 3
   (`GSEER_DB_RUNTIME_DSN`).  The v0.2.0 server refuses to start
   without it and refuses to serve as anything other than `gseer_app`.
5. **Restart under v0.2.0** — stop the v0.1.0 process, start the v0.2.0
   binary (`gseer server start`), verify `gseer doctor`
   (`6 PASS, 0 FAIL`) and the health endpoint.

**Rollback** = restore the backup + run the v0.1.0 binary on the
unchanged `GSEER_DB_*` configuration (the v0.1.0 binary is unaffected
by the new roles; its single-credential model keeps working).  The
upgrade path is exercised automatically by
`TestV010ToV020UpgradePath` (build tag `integration`): a real
`(6, false)` ledger is upgraded through the exact pinned order and the
runtime paths (suppression via the policy functions) are proven
afterwards.
