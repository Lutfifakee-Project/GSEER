// GSEER — internet-asset search engine.
// Copyright (C) 2026  The GSEER Authors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package cli

// module_path_test.go — the public install-path guard (v0.3.1).
//
// The documented install command is
//
//      go install -v github.com/Lutfifakee-Project/gseer/cmd/gseer@latest
//
// and a Go module path must match the go.mod declaration BYTE FOR BYTE
// (module paths are case-sensitive; the Go toolchain refuses otherwise:
// "module declares its path as … but was required as …").  The v0.3.0
// release shipped with the module declared in UPPERCASE ("G" + "SEER")
// while every doc taught the lowercase form — the exact documented
// command failed on a clean machine.  These tests keep the module
// declaration, the Go imports, and the documentation on the one
// lowercase path forever.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	wantModulePath = "module github.com/Lutfifakee-Project/gseer"
	// staleModulePath is concatenated so this file's own source does not
	// contain the banned literal.
	staleModulePath = "Lutfifakee-Project/" + "GSEER"
)

// repoRoot resolves the module root from this package's location
// (internal/cli → two levels up).
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("go.mod not found at %s: %v", root, err)
	}
	return root
}

func TestGoModDeclaresLowercasePublicPath(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	// The first line must equal the documented path byte for byte —
	// except for the one artifact a Windows `git clone` (core.autocrlf)
	// adds to EVERY text file: a trailing carriage return on the CRLF
	// line ending.  Only that "\r" is stripped; any other difference
	// (BOM, leading whitespace, a different path, wrong case) still
	// fails, so the guard keeps detecting a genuinely incorrect module
	// declaration.
	head := strings.TrimSuffix(strings.SplitN(string(raw), "\n", 2)[0], "\r")
	if head != wantModulePath {
		t.Fatalf("go.mod must declare the documented public path\n got: %s\nwant: %s", head, wantModulePath)
	}
}

func TestNoStaleUppercaseModulePathAnywhere(t *testing.T) {
	root := repoRoot(t)
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "dist" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		// Text artifacts only — skip anything the Go toolchain or the
		// release process generates.
		ext := filepath.Ext(path)
		switch ext {
		case ".go", ".mod", ".sum", ".md", ".yml", ".yaml", ".toml", ".json", ".sh", "Makefile":
		default:
			if filepath.Base(path) != "Makefile" && filepath.Base(path) != "Dockerfile" {
				return nil
			}
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(raw), staleModulePath) {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("stale uppercase module path %q found in:\n%s",
			staleModulePath, strings.Join(offenders, "\n"))
	}
}

func TestDocumentedInstallCommandUsesTheModulePath(t *testing.T) {
	root := repoRoot(t)
	docs := []string{"README.md", filepath.Join("docs", "INSTALL.md"), filepath.Join("docs", "QUICKSTART.md")}
	for _, rel := range docs {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !strings.Contains(string(raw), "go install") ||
			!strings.Contains(string(raw), "github.com/Lutfifakee-Project/gseer/cmd/gseer") {
			t.Errorf("%s must teach the exact lowercase install command (github.com/Lutfifakee-Project/gseer/cmd/gseer)", rel)
		}
	}
}
