// GSEER — Internet Asset Intelligence & Search Engine.
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

//go:build integration

package cli

import (
	"bytes"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// export_cli_integration_test.go — `gseer export` E2E against the
// fixture corpus: file output lands on disk with the right shape, the
// overwrite guard holds, and unsafe paths are refused.

func TestExportCLIE2E(t *testing.T) {
	t.Cleanup(func() { flags.output = "" })
	dsn, cleanup := cliTestDB(t)
	defer cleanup()

	cfgFile := writeFixtureCLIConfig(t, dsn, true)
	runSetupFixtureAction(t, "load-fixtures", dsn, cfgFile, "")

	outDir := t.TempDir()
	outFile := filepath.Join(outDir, "fixtures.csv")

	out, err := executeRoot(t, "--config", cfgFile, "export", `stale == "false"`, "--format", "csv", "--out", outFile)
	if err != nil {
		t.Fatalf("export csv: %v\n%s", err, out)
	}
	if !strings.Contains(out, "7 row(s)") || !strings.Contains(out, outFile) {
		t.Errorf("export stats line wrong:\n%s", out)
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil {
		t.Fatalf("csv re-parse: %v", err)
	}
	if len(records) != 8 { // header + 7 fixture rows
		t.Fatalf("csv records = %d, want 8", len(records))
	}
	// Credential hygiene: the DSN never lands in the file.
	if strings.Contains(string(data), "5433") || strings.Contains(string(data), dsn) {
		t.Error("export file contains connection-string material")
	}

	// Overwrite refusal: same --out again without --force exits 2.
	code, called := captureExit(t, func() {
		out, _ = executeRoot(t, "--config", cfgFile, "export", `stale == "false"`, "--format", "csv", "--out", outFile)
	})
	if !called || code != ExitInvalidInput || !strings.Contains(out, "E_EXPORT_EXISTS") {
		t.Errorf("overwrite guard: want exit %d + E_EXPORT_EXISTS, called=%v code=%d\n%s", ExitInvalidInput, called, code, out)
	}

	// --force succeeds and the dot-dot refusal fires before any DB work.
	code, called = captureExit(t, func() {
		out, _ = executeRoot(t, "--config", cfgFile, "export", `stale == "false"`, "--format", "csv", "--out", outFile, "--force")
	})
	if called || code != 0 {
		t.Errorf("--force export should exit cleanly, called=%v code=%d\n%s", called, code, out)
	}
	code, called = captureExit(t, func() {
		out, _ = executeRoot(t, "--config", cfgFile, "export", `stale == "false"`, "--out", "../escape.csv")
	})
	if !called || code != ExitInvalidInput || !strings.Contains(out, "E_EXPORT_PATH") {
		t.Errorf("path traversal: want exit %d + E_EXPORT_PATH, called=%v code=%d\n%s", ExitInvalidInput, called, code, out)
	}

	// NDJSON to stdout: 8 lines (7 rows + footer); the stats line must
	// go to STDERR, not into the piped stream (executeRoot merges the
	// buffers, so this invocation captures them separately).
	outBuf, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	resetSetupFlags(t)
	withConfigPath(t, cfgFile)
	RootCmd.SetOut(outBuf)
	RootCmd.SetErr(errBuf)
	RootCmd.SetArgs([]string{"--config", cfgFile, "export", `stale == "false"`, "--format", "ndjson", "--out", "-"})
	if err := RootCmd.Execute(); err != nil {
		t.Fatalf("ndjson stdout: %v", err)
	}
	RootCmd.SetArgs([]string{})
	lines := strings.Split(strings.TrimSpace(outBuf.String()), "\n")
	if len(lines) != 8 || !strings.Contains(lines[len(lines)-1], `"record":"footer"`) {
		t.Errorf("ndjson stdout lines = %d (want 8), tail: %s", len(lines), lines[len(lines)-1])
	}
	if !strings.Contains(errBuf.String(), "exported: 7 row(s)") {
		t.Errorf("stats line missing from stderr: %s", errBuf.String())
	}
}
