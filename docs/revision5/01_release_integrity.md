# Section 1 — Correct Release Integrity Report

## 1.1 Git diff evidence for `3432d32..d84b291583bf2f0ec8d3499faa62031239e2681b`

The exact `git diff --name-status` output was executed at the baseline `d84b291` and produced:

```
M	CHANGELOG.md
M	README.md
A	RELEASE_NOTES_v0.1.0.md
A	banner.png
M	docs/INSTALL.md
M	docs/QUICKSTART.md
M	docs/ROADMAP.md
M	docs/UPDATE.md
A	internal/cli/banner.go
A	internal/cli/banner_test.go
M	internal/cli/doctor.go
M	internal/cli/no_phantom_commands_test.go
M	internal/cli/root.go
M	internal/cli/root_test.go
M	internal/cli/source_collect_test.go
```

### Actual counts (Git name-status)

| Status | Count | Files |
|--------|-------|-------|
| Modified (`M`) | 11 | CHANGELOG.md, README.md, docs/INSTALL.md, docs/QUICKSTART.md, docs/ROADMAP.md, docs/UPDATE.md, internal/cli/doctor.go, internal/cli/no_phantom_commands_test.go, internal/cli/root.go, internal/cli/root_test.go, internal/cli/source_collect_test.go |
| Added (`A`) | 4 | RELEASE_NOTES_v0.1.0.md, banner.png, internal/cli/banner.go, internal/cli/banner_test.go |
| **Total changed** | **15** | — |

### Verification of Revision 4's count

Revision 4 stated "11 modified files, 4 added files, 15 total changed files." **This count is correct.** Git name-status confirms exactly 11 `M` and 4 `A` for the diff `3432d32..d84b291`.

## 1.2 Tag relationship

- `git rev-list -n 1 v0.1.0` returns `d84b291583bf2f0ec8d3499faa62031239e2681b`.
- The current HEAD (`d84b291`) **is** the `v0.1.0` release tag. Nothing in the working tree is "after" the release except one uncommitted formatting change.
- The uncommitted change (`internal/cli/banner_test.go`, formatting-only per restriction) must remain untouched and is NOT part of the `3432d32..d84b291` diff.

## 1.3 Working-tree difference

| Item | State |
|------|-------|
| Uncommitted change | `internal/cli/banner_test.go` — modified, not staged |
| Restriction | Formatting-only; must remain untouched; not part of any release diff |
| Is it "untracked"? | No — it is a tracked file with a working-tree modification |

The uncommitted change is tracked (`banner_test.go` was added in commit `3432d32` as part of the diff above), so it must NOT be described as untracked.

## 1.4 Distinctions per restriction

| Category | Determination | Evidence |
|----------|---------------|----------|
| Files included in release tag `v0.1.0` | The full tree at `d84b291` | `git rev-list -n 1 v0.1.0 == HEAD` |
| Files modified after the release | None committed; only the uncommitted `banner_test.go` formatting change | `git status` shows only `banner_test.go` modified |
| Current working-tree difference | `internal/cli/banner_test.go` only (formatting-only, must remain untouched) | `git status` |
| Release verification | 15 changed files in `3432d32..d84b291`: 11 modified + 4 added | `git diff --name-status 3432d32..d84b291` |

## 1.5 Test-result reporting discipline

No test results are claimed in this section without identifying environment and execution. This section reports only git metadata (name-status output, tag resolution, working-tree status), which was observed directly against the repository at baseline `d84b291` on the local workstation.
