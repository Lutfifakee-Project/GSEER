# GSEER — Signed Update System (v0.1.0)

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

The download is capped (512 MiB), HTTP(S)-only, timeout-bounded, and
credential-redacting (userinfo URLs are never echoed into errors).
Everything the updater writes lives next to the target binary; nothing
runs automatically afterwards — `gseer server stop` + `server start`
picks up the new build.

## 2. Commands

```
gseer update --check                  # verify manifest + report only (no download)
gseer update --apply                  # full chain: verify → download → checksum → replace
gseer update --apply --force          # allow same/older versions (re-install)
gseer update --manifest URL_OR_PATH   # override the configured channel
```

The manifest location comes from `update.manifest_url` (config file) or
`GSEER_UPDATE_MANIFEST_URL`.  Without a configured channel the command
fails with `E_UPDATE_NO_CHANNEL` and a pointer here.

Recovery: the previous binary is always kept at `<binary>.bak`.  If a
new build misbehaves, stop the server, move the old file back, start
again:

```
gseer server stop
mv gseer.bak gseer
gseer server start
```

## 3. Manifest format

```json
{
  "version": "v0.1.1",
  "released_at": "2026-11-03T00:00:00Z",
  "artifacts": [
    {"os": "linux", "arch": "amd64",
     "url": "https://download.example/gseer-v0.1.1-linux-amd64",
     "sha256": "<64 hex>", "size": 38777344}
  ],
  "notes_url": "https://github.com/.../releases/tag/v0.1.1",
  "signature": "<base64 Ed25519 over the canonical core bytes>"
}
```

The signature covers the **canonical core** — the manifest JSON without
the `signature` field, marshaled by Go's `encoding/json` (struct field
order is deterministic).  `gseer-release sign` performs exactly this;
the updater re-marshals the received core and verifies — any byte
changed after signing (including added fields) breaks verification.

## 4. Release process (maintainer)

```
go run ./cmd/gseer-release keygen -pub gseer-update.pub -priv gseer-update.key
#   → embed the PUBLIC key base64 into internal/update/keys.go
#     (EmbeddedUpdatePublicKeyB64) and release binaries built from it.
#   → the PRIVATE key (0600) stays with the release machine.  NEVER
#     commit it, never put it in CI secrets the whole team can read.
```

Per release:

```
# 1. compute artifact hashes
sha256sum gseer-v0.1.1-linux-amd64 gseer-v0.1.1-darwin-arm64 ...
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
- `gseer update` on an un-injected `dev` build refuses to act
  (`E_UPDATE_VERSION`): a binary that cannot prove its version cannot
  prove an update is an upgrade.  Release builds stamp the version via
  `-ldflags` (Makefile).
