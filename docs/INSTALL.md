# GSEER — Installation (v0.3.1)

## Install method: `go install` (recommended)

Install Go (latest version), then:

```bash
go install -v github.com/Lutfifakee-Project/gseer/cmd/gseer@latest
```

> **Until the v0.3.1 tag is published**, that `@latest` form errors out:
> tag resolution picks the **v0.3.0** tag, whose `go.mod` still declares
> the UPPERCASE module path — exactly the mismatch v0.3.1 fixes
> ("module declares its path as `…/GSEER` but was required as `…/gseer`").
> Interim installs that work today (verified): install the current
> source with
> `go install -v github.com/Lutfifakee-Project/gseer/cmd/gseer@main`,
> or build from source (below).  Once v0.3.1 is the latest tag, `@latest`
> resolves it — the same resolution rule that today picks v0.3.0 — and
> the documented command works.

The binary lands in `$(go env GOPATH)/bin` — put that on your `PATH` if
it is not.  Nothing else is required for personal mode: the first
`gseer` run creates the single-file database and config automatically
(and so does the first engine command — `search`, `source add`,
`collect run` — when no configuration exists yet).

Pin a release instead of `@latest` when you want reproducibility, e.g.
`go install -v github.com/Lutfifakee-Project/gseer/cmd/gseer@v0.3.1`.
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
cd gseer
make build VERSION=v0.3.1   # ./bin/gseer (version stamped via ldflags)
make tools                  # install golangci-lint + govulncheck
make check                  # build + vet + lint + test + vuln
./bin/gseer version         # expect: gseer v0.3.1
```

The resulting `./bin/gseer` binary's SHA-256 can be printed with
`gseer doctor` for diagnostic purposes.  It is a self-reported running
binary fingerprint and must NOT be compared against a source-archive
SHA-256 checksum — those are different artifacts.  Release artifacts are
published with a `checksums.txt` manifest (SHA-256,
artifact-per-artifact); verify any artifact you download against it.
`make build` is reproducible: two builds from a clean clone with the
same toolchain and flags produce byte-identical binaries (verified this
release).

## Verify the source archive

```bash
sha256sum -c GSEER-v0.3.1-source.zip.sha256   # Linux
shasum -a 256 -c GSEER-v0.3.1-source.zip.sha256   # macOS
# PowerShell: $expected = (Get-Content GSEER-v0.3.1-source.zip.sha256 -Raw).Split(' ')[0]; $actual = (Get-FileHash GSEER-v0.3.1-source.zip -Algorithm SHA256).Hash; if ($expected -ne $actual) { throw "Checksum mismatch: expected $expected, actual $actual" }
```

## Docker image (built locally, not pulled from a registry)

```bash
docker build --build-arg VERSION=v0.3.1 -t gseer:local .
# or let the full stack build it for you:
gseer setup --with-compose --full
docker compose -f docker-compose.full.yml up -d --build
```

There is no official GSEER image in any container registry for v0.3.1;
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
Upgrading an existing deployment: `docs/UPDATE.md`.
