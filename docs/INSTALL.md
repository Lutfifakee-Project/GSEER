# GSEER — Installation (v0.1.0)

> The three-OS install matrix.  `gseer doctor` prints the running
> binary's SHA-256 for comparison against the release SHA256SUMS.

## Install methods (per OS)

| OS | v0.1.0 methods | Later |
| --- | --- | --- |
| Linux | Release tarball + `SHA256SUMS` verification; build from source; Docker image | deb/rpm, Homebrew/winget/scoop channels |
| macOS | Universal tarball + checksums; build from source | Homebrew |
| Windows | Release zip + PowerShell checksum instructions; build from source | winget/scoop |

v0.1.0 ships **checksums plus the signed-update channel** (`gseer update`
verifies an Ed25519-signed manifest against a key embedded in the
binary — `docs/UPDATE.md`).  Release tarballs carry SHA256SUMS;
`gseer doctor` prints the running binary's SHA-256 for comparison.

## Verify a release tarball

```bash
sha256sum -c SHA256SUMS          # Linux
shasum -a 256 -c SHA256SUMS      # macOS
# PowerShell: Get-FileHash gseer.exe -Algorithm SHA256
```

## Requirements

- **Go 1.26+** (per `go.mod`) when building from source; the release
  binary has no runtime dependency beyond PostgreSQL.
- **PostgreSQL 15+** (v0.1.0 is verified against PG 17).  The compose
  files ship `postgres:17-alpine`, so a Docker install needs nothing
  else pre-installed.

## Build from source (developer)

```bash
git clone https://github.com/Lutfifakee-Project/GSEER.git
cd GSEER
make build VERSION=v0.1.0   # ./bin/gseer (version stamped via ldflags)
make tools                  # install golangci-lint + govulncheck
make check                  # build + vet + lint + test + vuln
./bin/gseer version         # expect: gseer v0.1.0
```

## Docker image

```bash
docker build --build-arg VERSION=v0.1.0 -t gseer:local .
# or let the full stack build it for you:
gseer setup --with-compose --full
docker compose -f docker-compose.full.yml up -d --build
```

The full stack (db + server, healthchecks, persistent volume, generated
API token) and its lifecycle — startup, upgrade, backup/restore,
recovery — are documented in `docs/DOCKER.md`.

## After install

Continue with `docs/QUICKSTART.md` (`gseer setup --self-hosted
--with-compose` → `docker compose up -d db` → `gseer setup --self-hosted`
→ `gseer doctor` → `gseer search 'port==443'`).
