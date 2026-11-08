# GSEER v0.2.0 — Windows Regression Integration & CI Recovery Report

**Date:** 2026-11-08
**Wave:** 8 (v0.2.0-integrated)
**Repository baseline:** single commit `718a0bc Initial commit`, remote `origin = https://github.com/Lutfifakee-Project/GSEER.git` (branch `main`)
**Toolchain used for local verification:** Go 1.27.3 (`/home/z/go-sdk/go127`), PostgreSQL 17.11 (Debian 17.11-0+deb13u1, port 5433, user `gseer`, trust), govulncheck v1.9.0, golangci-lint v2.15.0.

---

## 1. Objective

Complete the missing Windows regression coverage and restore a modern CI workflow for v0.2.0, without redoing already-implemented Windows fixes (Wave 7: `seed.go` `path.Join`, `db_test.go` `filepath.ToSlash`, `filename.go` `FromSlash` normalization — all verified present and untouched this wave).

---

## 2. Exact changed files (this wave)

| File | Change | Mtime |
|---|---|---|
| `internal/export/filename_windows_test.go` | Expanded the Windows regression test: added control-character (`out\n.csv`, `out\t.csv`, `\x00bad.csv`) and leading flag-like (`-flag.csv`) refusal cases to the existing separator-style acceptance + traversal-refusal coverage. Now satisfies spec B in full. | Nov 8 12:07 |
| `.github/workflows/ci.yml` | Restored and modernized for v0.2.0 (full rewrite preserving the original structure): header → v0.2.0; PostgreSQL **16 + 17 matrix** for both `integration-test` and `nightly-integration`; integration step expanded from only `./internal/storage/postgres/` to **all seven integration packages**; named **Migration audit** step (`TestForceNotCallableFromOtherCommands`) in `build-test`; named **Privilege audit** step (`TestSuppressionAuditSurvivesRuleRemoval`) in `integration-test`; govulncheck pinned to **v1.9.0** (was `@latest`); documented least-privilege posture and fail-hard skip semantics. | Nov 8 12:11 |

**No other files were modified this wave.** Wave 7 fixes confirmed intact:

```
internal/fprule/seed.go       — path.Join    (2 occurrences, embed.FS keys)
internal/cli/db_test.go       — ToSlash      (1 occurrence, allowlist match)
internal/export/filename.go   — FromSlash    (3 occurrences, clean-form + segment scan)
```

---

## 3. Test coverage and commands

### 3.1 Windows regression coverage (`internal/export/filename_windows_test.go`)

`//go:build windows` — `TestValidateOutPathWindowsSemantics`:

**Accepted** (separator style is not uncleanliness on Windows):
- `/tmp/seer-out/export.csv` — POSIX-style absolute path
- `dir/sub/export.ndjson` — nested path using `/`
- `dir\sub\export.ndjson` — native Windows separators
- `C:\Temp\001/out.json` — mixed separators (TempDir + `/name` join shape)
- `export.csv`

**Refused** (security invariants hold across separator styles):
- `../escape.csv` — traversal with forward separators
- `..\escape.csv` — traversal with native separators
- `..\../evil.csv` — mixed-separator traversal
- `a/../b.csv` — mid-path traversal
- `-flag.csv` — leading flag-like name
- `out\n.csv` — control character (newline)
- `out\t.csv` — control character (tab)
- `\x00bad.csv` — control character (NUL)

### 3.2 Platform-independent security invariants

Already covered by `TestValidateOutPath` in `internal/export/export_test.go` (no build tag, runs on every platform): empty, `../escape.csv`, `a/../b.csv`, `-flag.csv`, `out\n.csv`, `out\t.csv`. Verified present — no new platform-independent test needed (spec B "add **or verify**").

### 3.3 Commands run locally (Linux, Go 1.27.3)

```
gofmt -l .                                    # clean
go test -count=1 ./internal/fprule            # ok  0.023s
go test -count=1 ./internal/export            # ok  0.003s
go test -count=1 ./internal/cli               # ok  0.124s
go test -count=1 ./internal/update            # ok  0.017s
go test -race -count=1 ./...                  # 18 packages, zero failures
go vet ./...                                  # clean
go build ./...                                # ok
govulncheck -show verbose ./...               # No vulnerabilities found
golangci-lint run --timeout 5m                # 0 issues
go test -run TestForceNotCallableFromOtherCommands -count=1 ./internal/cli     # migration audit
go test -run TestImportGraphSearchNotScan -count=1 ./internal/cli              # Search ≠ Scan
go test -run TestNoCollectorPackageExists -count=1 ./internal/cli              # active-observation absence
go test -race -tags integration -run TestSuppressionAuditSurvivesRuleRemoval -count=1 ./internal/storage/postgres/   # privilege audit
```

### 3.4 Windows cross-compilation evidence (NOT claimed as Windows PASS)

```
GOOS=windows GOARCH=amd64 go build ./...      # ok
GOOS=windows GOARCH=amd64 go vet ./...        # clean
GOOS=windows GOARCH=amd64 go test -c ./internal/fprule ./internal/export ./internal/cli ./internal/update ./internal/collect ./internal/search ./internal/gql/planner ./internal/fixture ./internal/storage/postgres   # all compile
```

---

## 4. Linux results

| Check | Result |
|---|---|
| `gofmt -l .` | clean |
| `go test -count=1 ./internal/fprule` | **PASS** (0.023s) |
| `go test -count=1 ./internal/export` | **PASS** (0.003s) |
| `go test -count=1 ./internal/cli` | **PASS** (0.124s) |
| `go test -count=1 ./internal/update` | **PASS** (0.017s) |
| `go test -race -count=1 ./...` | **PASS** (18 packages, zero failures) |
| `go vet ./...` | clean |
| `go build ./...` | ok |
| `govulncheck -show verbose ./...` | **No vulnerabilities found** |
| `golangci-lint run --timeout 5m` (v2.15.0) | **0 issues** |
| Migration audit (`TestForceNotCallableFromOtherCommands`) | **PASS** |
| Privilege audit (`TestSuppressionAuditSurvivesRuleRemoval`) | **PASS** (PG 17) |
| Search ≠ Scan (`TestImportGraphSearchNotScan` + `TestNoCollectorPackageExists`) | **PASS** |

---

## 5. Windows results

| Check | Result |
|---|---|
| `GOOS=windows go build ./...` | **ok** (cross-compile) |
| `GOOS=windows go vet ./...` | **clean** (cross-compile) |
| `GOOS=windows go test -c` (all 9 packages) | **all compile** (cross-compile) |
| `go test ./internal/fprule` on Windows runner | **REQUIRES GitHub-hosted runner** (no Windows host in this sandbox) |
| `go test ./internal/export` on Windows runner | **REQUIRES GitHub-hosted runner** |
| `go test ./internal/cli` on Windows runner | **REQUIRES GitHub-hosted runner** |
| `go test ./internal/update` on Windows runner | **REQUIRES GitHub-hosted runner** |

**Cross-compilation is not proof that Windows tests pass.** The `update-windows` CI job (windows-latest, Go 1.27.x) runs `go test -count=1 ./internal/update/...`, `-race -count=1 ./internal/update/...`, the **full unit suite** `go test -count=1 ./...`, `go vet ./...`, and `go build ./...` — covering all four spec-C packages plus the rest. The first real Windows-runner execution of this wave's expanded `filename_windows_test.go` happens on the next push.

---

## 6. PostgreSQL 16 and 17 results

### 6.1 Local (PG 17.11 only)

All seven integration packages run against PG 17.11 (`postgres://gseer@127.0.0.1:5433/postgres?sslmode=disable`):

```
GSEER_TEST_PG_ADMIN='postgres://gseer@127.0.0.1:5433/postgres?sslmode=disable' GSEER_TEST_REQUIRE=1 \
  go test -race -tags integration \
  ./internal/storage/postgres/ ./internal/collect/ ./internal/search/ \
  ./internal/gql/planner/ ./internal/fixture/ ./internal/cli/ ./internal/export/
```

| Package | Result | Time |
|---|---|---|
| `internal/storage/postgres` | **PASS** | 15.377s |
| `internal/collect` | **PASS** | 2.715s |
| `internal/search` | **PASS** | 4.675s |
| `internal/gql/planner` | **PASS** | 7.807s |
| `internal/fixture` | **PASS** | 1.329s |
| `internal/cli` | **PASS** | 2.610s |
| `internal/export` | **PASS** | 1.697s |

### 6.2 PG 16

**Not run locally** — no PostgreSQL 16 binary available in this sandbox (no Docker, no apt postgresql-16 package, no root to install). The CI workflow `integration-test` job runs a PG 16 + 17 matrix (`postgres:16-alpine` and `postgres:17-alpine` service containers) on GitHub-hosted runners. PG 16 execution is therefore **deferred to the GitHub-hosted runner**.

GSEER's migrations use only PostgreSQL 15+ core features (tables, indexes, views, triggers, advisory locks — no PG-16/17-only syntax); PG 16 compatibility is expected but not claimed verified until the CI matrix runs.

---

## 7. CI workflow structure

`.github/workflows/ci.yml` (v0.2.0) — 7 jobs, YAML-validated:

| Job | Runner | Trigger | Purpose |
|---|---|---|---|
| `build-test` | ubuntu-latest, Go 1.26.x + 1.27.x matrix | push/PR/schedule/dispatch | Build, vet, `-race ./...`, migration audit step |
| `update-windows` | windows-latest, Go 1.27.x | push/PR/schedule/dispatch | Updater suite + race + **full unit suite** + vet + build |
| `lint` | ubuntu-latest, Go 1.27.x | push/PR/schedule/dispatch | golangci-lint v2.15.0 via action v9.3.0 |
| `vuln` | ubuntu-latest, Go 1.27.x | push/PR/schedule/dispatch | govulncheck v1.9.0 (pinned) |
| `import-graph` | ubuntu-latest, Go 1.27.x | push/PR/schedule/dispatch | Search ≠ Scan + active-observation absence |
| `integration-test` | ubuntu-latest, **PG 16 + 17 matrix**, Go 1.27.x | push/PR/schedule/dispatch | All 7 integration packages + privilege audit step |
| `nightly-integration` | ubuntu-latest, **PG 16 + 17 matrix**, Go 1.27.x | **schedule + workflow_dispatch only** | Full battery with `-cover` |

**Triggers:** `push` (main), `pull_request` (main), `schedule` (cron `0 18 * * *` = 01:00 Asia/Jakarta), `workflow_dispatch` (manual).

**Least-privilege:** `permissions: contents: read`.

**Pinned action versions:** `actions/checkout@v4`, `actions/setup-go@v5`, `golangci/golangci-lint-action@v9.3.0` with `version: v2.15.0`.

**No silent skips:** integration test helpers (`adminConnect`, `testDB`, `cliTestDB`, `colTestDB`) call `t.Fatalf` on any PG-reachability failure — a missing `GSEER_TEST_PG_ADMIN` or down service fails the build rather than reporting a pass with skipped suites. `GSEER_TEST_REQUIRE=1` is set in CI as documentation of the fail-hard posture (the code is unconditionally fatal regardless).

**Integration packages actually executed** (previously only `./internal/storage/postgres/` ran — six packages were silently absent from CI):

```
./internal/storage/postgres/  ./internal/collect/  ./internal/search/
./internal/gql/planner/       ./internal/fixture/  ./internal/cli/
./internal/export/
```

---

## 8. Build tags and DSN conventions (inspected before writing CI commands)

- **Build tag:** `//go:build integration` on all 22 integration-tagged test files across 7 packages.
- **Admin DSN env:** `GSEER_TEST_PG_ADMIN` (superuser DSN to an admin database used to create/drop per-test databases).
- **Default DSN:** `postgres://gseer@127.0.0.1:5433/postgres?sslmode=disable` (matches the standalone PG 17 used for local development).
- **CI service mapping:** GitHub `services.postgres` maps host port `5433` → container `5432`, so the default DSN works unchanged in CI.
- **Skip semantics:** `t.Fatalf` (not `t.Skip`) — fail-hard, never silent. `GSEER_TEST_REQUIRE` is referenced only in comments, not read by code; the fail-hard behavior is unconditional.

---

## 9. Remaining limitations

1. **Windows runner execution** — no Windows host in this sandbox. Cross-compilation passes as compile-time evidence; the four spec-C packages' real Windows PASS is deferred to the `update-windows` GitHub-hosted runner job on the next push.
2. **PostgreSQL 16** — not available locally (no Docker, no apt package, no root). PG 16 verification deferred to the CI matrix (`postgres:16-alpine`).
3. **`GSEER_TEST_REQUIRE` is documentation-only** — the env var is referenced in test comments but never read by code; integration tests already fail unconditionally on PG-unreachable. Kept in CI env for intent-documentation; not a defect (the fail-hard contract is stronger than the comment implies).
4. **Binary version string unchanged** — `cmd/gseer-release/main.go` still references v0.1.0. Bumping the product version string is out of scope (would be an unrelated product change); only the CI workflow metadata (header comment) references v0.2.0.
5. **Single-commit git history** — the working tree is entirely untracked relative to `718a0bc Initial commit` (pre-existing condition from prior waves). No commit/tag/push performed this wave per restrictions.

---

## 10. Final git status

```
HEAD:    718a0bc Initial commit    (UNCHANGED)
remote:  origin = https://github.com/Lutfifakee-Project/GSEER.git (fetch/push)   (UNCHANGED)
branch:  main

Working tree (untracked, as in prior waves):
  .github/workflows/ci.yml              — modified this wave (v0.2.0 modernization)
  internal/export/filename_windows_test.go — modified this wave (spec B completion)
  all other files                       — unchanged from Wave 7 state

git diff --stat HEAD:  LICENSE | 0 (pre-existing mode-only artifact, not from this wave)
```

**No commit, tag, push, GitHub Release, or deployment performed.**
**No migration changes, no database grant changes, no security weakening, no unrelated product changes.**

---

## 11. Confirmation: original HEAD and remote unchanged

- `git log --oneline -1` → `718a0bc Initial commit` (same as wave start)
- `git remote -v` → `origin https://github.com/Lutfifakee-Project/GSEER.git` (same as wave start)
- No `git config` modifications, no ref changes, no stash, no force operations.

---

## 12. Status

**READY WITH WARNINGS** — Linux full battery green (unit + race + vet + build + govulncheck + lint + PG 17 integration across all 7 packages + migration/privilege/Search≠Scan audits); Windows cross-compilation green (compile-time evidence only); YAML validated with structural assertions. The two warnings are honest: (1) Windows runner PASS and (2) PG 16 verification both require GitHub-hosted runners and will complete on the next push.
