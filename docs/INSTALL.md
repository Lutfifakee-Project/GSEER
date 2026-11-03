# GSEER — Installation (v0.1.0)

> v0.1.0 is a **source-only self-hosted release**. The artifact is the
> source archive `GSEER-v0.1.0-source.zip` together with its SHA-256
> checksum sidecar `GSEER-v0.1.0-source.zip.sha256`. This source archive
> is the **only** downloadable release artifact for v0.1.0. There are no
> prebuilt platform binaries, no binary packages, and no published
> Docker image for this release. Build
> from source (below), or build the Docker image locally via the
> `Dockerfile`.

## Install method: build from source

| Component | Requirement |
| --- | --- |
| Go | 1.26+ (per `go.mod`) |
| PostgreSQL | 15+ if you run your own (v0.1.0 is verified against PG 17; the compose files ship `postgres:17-alpine`) |
| OS | Linux / macOS / Windows (build-tested where the CI matrix runs; no platform is officially verified as a binary target in v0.1.0) |

```bash
git clone https://github.com/Lutfifakee-Project/GSEER.git
cd GSEER
make build VERSION=v0.1.0   # ./bin/gseer (version stamped via ldflags)
make tools                  # install golangci-lint + govulncheck
make check                  # build + vet + lint + test + vuln
./bin/gseer version         # expect: gseer v0.1.0
```

The resulting `./bin/gseer` binary's SHA-256 can be printed with
`gseer doctor` for diagnostic purposes.  It is a self-reported running
binary fingerprint and must NOT be compared against the source archive's
SHA-256 checksum — those are different artifacts.  The checksum published
with v0.1.0 covers only the **source archive**; there are no binary
checksums in this release because v0.1.0 is source-only.

## Verify the source archive

```bash
sha256sum -c GSEER-v0.1.0-source.zip.sha256   # Linux
shasum -a 256 -c GSEER-v0.1.0-source.zip.sha256   # macOS
# PowerShell: $expected = (Get-Content GSEER-v0.1.0-source.zip.sha256 -Raw).Split(' ')[0]; $actual = (Get-FileHash GSEER-v0.1.0-source.zip -Algorithm SHA256).Hash; if ($expected -ne $actual) { throw "Checksum mismatch: expected $expected, actual $actual" }
```

## Docker image (built locally, not pulled from a registry)

```bash
docker build --build-arg VERSION=v0.1.0 -t gseer:local .
# or let the full stack build it for you:
gseer setup --with-compose --full
docker compose -f docker-compose.full.yml up -d --build
```

There is no official GSEER image in any container registry for v0.1.0;
the image is built locally from the source. The full stack (db + server,
healthchecks, persistent volume, generated API token) and its lifecycle
— startup, upgrade, backup/restore, recovery — are documented in
`docs/DOCKER.md`.

## After install

Continue with `docs/QUICKSTART.md` (`gseer setup --self-hosted
--with-compose` → `docker compose up -d db` → `gseer setup --self-hosted`
→ `gseer doctor` → `gseer search 'port==443'`).
