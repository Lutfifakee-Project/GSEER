# Section 13 — Beginner Workflow

## 13.1 Verified actual setup implementation (v0.1.0)

| Step | Command | What it does |
|------|---------|--------------|
| Build | `make build VERSION=v0.1.0` | Compiles `./bin/gseer` with version ldflags (source-only release; no prebuilt binaries, no published Docker image). |
| Compose | `gseer setup --self-hosted --with-compose` | Writes `docker-compose.yml` (db-only) + generated `.env` (random password, chmod 600). Prints next commands verbatim. Files written FIRST, then stops. |
| Start DB | `docker compose up -d db` | Operator starts PostgreSQL. |
| Schema | `gseer setup --self-hosted --db-url "$(grep '^GSEER_DSN=' .env | cut -d= -f2-)"` | Reachability → PG version (15+) → migrations to head. Zero manual SQL. |
| Verify | `gseer doctor` | Config validity, binary identity, DB checks, index contents (EMPTY/fixture/live), server reachability. Read-only. |
| Search | `gseer search 'port==443'` | Honest empty state (exit 0) when index empty. |
| Server | `gseer server start` | API + dashboard on `http://127.0.0.1:8443` (loopback by default); migrations run automatically on start (QS-7). |
| Fixtures (opt-in) | `gseer setup --load-fixtures` | QS-6 gate; labeled synthetic evaluation data. |

## 13.2 What the default workflow must NOT require (v0.2.0 goal)

- Read or edit SQL. (Verified: setup runs migrations; zero manual SQL.)
- Understand migrations. (Verified: `gseer setup --self-hosted` runs them to head.)
- Extract database credentials from `.env`. (The quickstart shows `grep GSEER_DSN .env`, but the goal is to minimize manual DSN handling; `--with-compose` writes `.env` and the operator passes it via `--db-url`.)
- Repeat setup just to initialize the database. (Setup is idempotent: migrations to head.)
- Understand AST or query planner internals. (Not required for setup.)
- Manually assemble PostgreSQL connection strings. (`Database.EffectiveDSN()` assembles from components with proper percent-encoding; operator can set component fields or a single DSN.)

## 13.3 Executable path after `make build`

The binary is `./bin/gseer`. v0.1.0 distributes source, not official prebuilt binaries. Revision 5 must reference `./bin/gseer` (or `make run`), not bare `gseer`, unless installation/PATH behavior is actually provided. `make build VERSION=v0.1.0` → `./bin/gseer version` reports `gseer v0.1.0`.

## 13.4 Docker availability and PostgreSQL provisioning

- Docker: available for local builds (`docker build --build-arg VERSION=v0.1.0 -t gseer:local .`) or via `gseer setup --with-compose --full` → `docker compose -f docker-compose.full.yml up -d --build`.
- **No official GSEER image in any container registry for v0.1.0.** Image built locally from source.
- PostgreSQL: operator-provisioned (own PG 15+ or via compose `postgres:17-alpine`). **No automatic provisioning** that the current implementation cannot perform. `gseer setup --with-compose` writes files; `docker compose up -d db` is the operator's next command.

## 13.5 Recommended v0.2.0 beginner journey (no new commands beyond existing)

1. Install/build: `git clone ... ; cd GSEER ; make build VERSION=v0.2.0 ; ./bin/gseer version`
2. One guided self-hosted setup: `./bin/gseer setup --self-hosted --with-compose` → `docker compose up -d db` → `./bin/gseer setup --self-hosted --db-url <DSN>`
3. Clear health diagnostics: `./bin/gseer doctor` (all checks PASS; index reported EMPTY honestly).
4. Honest empty-index explanation: `./bin/gseer search 'port==443'` → empty, exit 0, with empty-index pointer.
5. First operator-provided service-data import: via the new `operator-import`/JSONL import path (Section 5) — register source, enable, import JSONL.
6. First successful service search: `./bin/gseer search 'port==443'`

Avoid hardcoding a fixed number of `PASS` checks unless guaranteed by the actual `doctor` implementation (doctor reports PASS/FAIL/HINT per check; the count varies by profile).

## 13.6 Recovery

- Dirty migration state → `gseer db migrate diagnose` (doctor reports FAIL with fix pointer).
- Index empty → `gseer setup --load-fixtures` (QS-6) or first real import.
- Fixture labeled → doctor reports HINT with counts; purge with `gseer setup --purge-fixtures`.
