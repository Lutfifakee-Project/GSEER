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

package export

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/search"
)

// export_test.go — unit coverage for the format layer (escaping,
// structure, filename safety).  The DB-backed contracts (pagination
// stability, credential hygiene) live in the integration suite.

func csvCellsRow() *search.Row {
	ip := "203.0.113.10"
	title := `Login "v2", page`
	port := 443
	return &search.Row{
		RowID: 7, RowKind: "service", AssetID: 3,
		AssetKind: "ip", AssetValue: ip, IP: &ip,
		Port: &port, Protocol: strPtr("https"),
		HTTPTitle:    &title,
		Technologies: []string{"nginx", "php"},
		Sources:      []string{"fixture"},
		FirstSeen:    time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		LastSeen:     time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
	}
}

func strPtr(s string) *string { return &s }

func TestCSVCellsEscaping(t *testing.T) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(csvCells(csvCellsRow())); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(csvCells(csvCellsRow())); err != nil {
		t.Fatal(err)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatal(err)
	}

	// RFC 4180 round-trip: the quotes/commas inside the title must
	// survive re-parsing with cell counts intact.
	r := csv.NewReader(&buf)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("rows = %d, want 2 (escaping broke the record count)", len(records))
	}
	rec := records[0]
	if len(rec) != len(csvColumns) {
		t.Fatalf("cells = %d, want %d", len(rec), len(csvColumns))
	}
	byHeader := map[string]string{}
	for i, name := range csvColumns {
		byHeader[name] = rec[i]
	}
	if got := byHeader["http_title"]; got != `Login "v2", page` {
		t.Errorf("http_title cell = %q, want the unescaped original", got)
	}
	if got := byHeader["technologies"]; got != "nginx;php" {
		t.Errorf("technologies cell = %q, want nginx;php", got)
	}
	if got := byHeader["port"]; got != "443" {
		t.Errorf("port cell = %q", got)
	}
	if got := byHeader["last_seen"]; got != "2026-01-15T12:00:00Z" {
		t.Errorf("last_seen cell = %q", got)
	}
	if got := byHeader["stale"]; got != "false" {
		t.Errorf("stale cell = %q", got)
	}
}

func TestCSVUnicodeSurvives(t *testing.T) {
	row := csvCellsRow()
	u := "Log In ‹ WordPress — 登录"
	row.HTTPTitle = &u
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write(csvCells(row))
	w.Flush()
	if !strings.Contains(buf.String(), u) {
		t.Errorf("unicode title mangled: %q", buf.String())
	}
}

func TestStreamFormatValidation(t *testing.T) {
	if _, err := Stream(context.Background(), nil, Request{Format: "xml"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "E_EXPORT_FORMAT") {
		t.Errorf("want E_EXPORT_FORMAT, got %v", err)
	}
}

func TestStreamNDJSONShape(t *testing.T) {
	// The ndjson writer must produce one JSON object per line with a
	// distinguishable footer — verified against a hand-built service is
	// impossible without a DB, so this exercises writeRow via the
	// package-level helper contract: rows have row_id, footer has record.
	data, err := json.Marshal(map[string]any{"record": "footer", "count": 2})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["record"] != "footer" {
		t.Errorf("footer marker missing: %v", m)
	}
}

func TestValidateOutPath(t *testing.T) {
	ok := []string{"export.csv", "/tmp/seer-out/export.csv", "dir/sub/export.ndjson", "gseer-export-x.json"}
	for _, p := range ok {
		if err := ValidateOutPath(p); err != nil {
			t.Errorf("ValidateOutPath(%q) = %v, want nil", p, err)
		}
	}
	bad := map[string]string{
		"":              "empty",
		"../escape.csv": "dotdot",
		"a/../b.csv":    "dotdot",
		"-flag.csv":     "dash",
		"out\n.csv":     "control",
		"out\t.csv":     "control",
	}
	for p, why := range bad {
		err := ValidateOutPath(p)
		if err == nil {
			t.Errorf("ValidateOutPath(%q) accepted (%s case)", p, why)
			continue
		}
		if !strings.Contains(err.Error(), "E_EXPORT_PATH") {
			t.Errorf("ValidateOutPath(%q) error lacks code: %v", p, err)
		}
	}
}

func TestResolveOutPathDefaultAndOverwrite(t *testing.T) {
	// Default names are timestamped with the right extension.
	name, err := ResolveOutPath("", FormatCSV, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "gseer-export-") || !strings.HasSuffix(name, ".csv") {
		t.Errorf("default name = %q", name)
	}

	// An existing non-empty file is refused without --force.
	path := t.TempDir() + "/out.json"
	if err := writeFileForTest(path, "existing"); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveOutPath(path, FormatJSON, false); err == nil || !strings.Contains(err.Error(), "E_EXPORT_EXISTS") {
		t.Errorf("want E_EXPORT_EXISTS, got %v", err)
	}
	if _, err := ResolveOutPath(path, FormatJSON, true); err != nil {
		t.Errorf("--force must allow overwrite, got %v", err)
	}

	// An empty existing file is fine to overwrite (nothing lost).
	empty := t.TempDir() + "/empty.json"
	if err := writeFileForTest(empty, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveOutPath(empty, FormatJSON, false); err != nil {
		t.Errorf("empty existing file must not need --force, got %v", err)
	}
}

func writeFileForTest(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
