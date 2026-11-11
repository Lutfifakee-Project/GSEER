# GSEER — Installation (v0.3.0)

## Install method: `go install` (recommended)

Install Go (latest version), then:

```bash
go install -v github.com/Lutfifakee-Project/gseer/cmd/gseer@latest
```

The binary lands in `$(go env GOPATH)/bin` — put that on your `PATH` if
it is not.  Nothing else is required for personal mode: the first
`gseer` run creates the single-file database and config automatically.

Pin a release instead of `@latest` when you want reproducibility, e.g.
`go install github.com/Lutfifakee-Project/gseer/cmd/gseer@v0.3.0`.
Note on `version` output: `go install` builds carry no VCS metadata, so
`commit`/`built_at` read "unknown" — the version itself is correct.

## Install method: build from source

| Component | Requirement |
| --- | --- |
| Go | 1.26+ (per `go.mod`) |
| PostgreSQL | **16+** only for the self-hosted profile (OD-7 floor enforced by `gseer setup` and `gseer doctor`; verified end-to-end on **16.15 and 17.2**; the compose files ship `postgres:17-alpine`) |
| OS | Linux / macOS / Windows (the CI matrix builds and tests all three) |

```bash
git clone https://github.com/Lutfifakee-Project/gseer.git
cd GSEER
make build VERSION=v0.2.0   # ./bin/gseer (version stamped via ldflags)
make tools                  # install golangci-lint + govulncheck
make check                  # build + vet + lint + test + vuln
./bin/gseer version         # expect: gseer v0.2.0
```

The resulting `./bin/gseer` binary's SHA-256 can be printed with
`gseer doctor` for diagnostic purposes.  It is a self-reported running
binary fingerprint and must NOT be compared against the source archive's
SHA-256 checksum — those are different artifacts.  The checksum published
with v0.2.0 covers only the **source archive**; there are no binary
checksums in this release because v0.2.0 is source-only.  `make build`
is reproducible: two builds from a clean clone with the same toolchain
and flags produce byte-identical binaries (verified this release).

## Verify the source archive

```bash
sha256sum -c GSEER-v0.2.0-source.zip.sha256   # Linux
shasum -a 256 -c GSEER-v0.2.0-source.zip.sha256   # macOS
# PowerShell: $expected = (Get-Content GSEER-v0.2.0-source.zip.sha256 -Raw).Split(' ')[0]; $actual = (Get-FileHash GSEER-v0.2.0-source.zip -Algorithm SHA256).Hash; if ($expected -ne $actual) { throw "Checksum mismatch: expected $expected, actual $actual" }
```

## Docker image (built locally, not pulled from a registry)

```bash
docker build --build-arg VERSION=v0.2.0 -t gseer:local .
# or let the full stack build it for you:
gseer setup --with-compose --full
docker compose -f docker-compose.full.yml up -d --build
```

There is no official GSEER image in any container registry for v0.2.0;
the image is built locally from the source. The full stack (db + server,
healthchecks, persistent volume, generated API token) and its lifecycle
— startup, upgrade, backup/restore, recovery — are documented in
`docs/DOCKER.md`.

## After install

Continue with `docs/QUICKSTART.md` (`gseer setup --self-hosted
--with-compose` → `docker compose up -d db` → `gseer setup --self-hosted`
→ export `GSEER_PROFILE` + `GSEER_DB_DSN` + `GSEER_DB_RUNTIME_DSN` →
`gseer doctor` → `gseer search 'port==443'`).  The credential model and
the hardened startup/privilege-audit contract: `docs/DEPLOYMENT.md`.
Upgrading an existing v0.1.0 deployment: `docs/UPDATE.md`.
