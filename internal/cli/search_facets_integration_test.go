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
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// search_facets_integration_test.go — `gseer search --facets` E2E
// against the fixture corpus: the CLI renders the facet section computed
// by the SAME SearchService over the SAME filtered rows.

func TestSearchFacetsCLIE2E(t *testing.T) {
	t.Cleanup(func() {
		flags.output = ""
		searchFlagsReset() // --facet/--facets/--max-values/--collapse must not leak into later tests
	})
	dsn, cleanup := cliTestDB(t)
	defer cleanup()

	cfgFile := writeFixtureCLIConfig(t, dsn, true)
	// Provision + load the labeled corpus (standing consent in config).
	runSetupFixtureAction(t, "load-fixtures", dsn, cfgFile, "")

	out, err := executeRoot(t, "--config", cfgFile, "search", `port == 443`, "--facets", "--facet", "port,protocol,country")
	if err != nil {
		t.Fatalf("search --facets: %v\noutput:\n%s", err, out)
	}
	for _, want := range []string{
		"Facets (same filter, same SearchService):",
		"port",
		"443 (2)",
		"protocol",
		"http (1)",
		"https (1)",
		"country",
		"US (1)",
		"DE (1)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("facets output missing %q\ngot:\n%s", want, out)
		}
	}
	// A dimension not requested must not appear.
	if strings.Contains(out, "freshness (") || strings.Contains(out, "source (") {
		t.Errorf("unrequested facet dimension rendered:\n%s", out)
	}

	// JSON output carries a facets array.
	out, err = executeRoot(t, "--config", cfgFile, "search", `port == 443`, "--facets", "-o", "json")
	if err != nil {
		t.Fatalf("search --facets json: %v\n%s", err, out)
	}
	for _, want := range []string{`"facets"`, `"facet": "port"`, `"value": "443"`, `"count": 2`} {
		if !strings.Contains(out, want) {
			t.Errorf("json facets output missing %q\ngot:\n%s", want, out)
		}
	}
	flags.output = "" // reset before the table-mode invocations below

	// max-values propagates (2 values max, truncated marker).
	out, err = executeRoot(t, "--config", cfgFile, "search", `stale == "false"`, "--facets", "--facet", "port", "--max-values", "2")
	if err != nil {
		t.Fatalf("search --facets max-values: %v\n%s", err, out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("truncation marker missing:\n%s", out)
	}

	// An invalid facet dimension surfaces the engine error via the
	// exit seam (exit code 2, ExitInvalidInput).
	code, called := captureExit(t, func() {
		out, _ = executeRoot(t, "--config", cfgFile, "search", `port == 443`, "--facets", "--facet", "asn")
	})
	if !called || code != ExitInvalidInput || !strings.Contains(out, "E_QUERY_FACET") {
		t.Errorf("want exit %d + E_QUERY_FACET for unknown dimension, called=%v code=%d out=\n%s", ExitInvalidInput, called, code, out)
	}
}

func TestCollectRedriveCLIE2E(t *testing.T) {
	t.Cleanup(func() { flags.output = "" })
	searchFlagsReset() // defense in depth: never inherit a prior test's --facet value
	dsn, cleanup := cliTestDB(t)
	defer cleanup()
	cfgFile := writeFixtureCLIConfig(t, dsn, true)

	// Provision + register a source (via the real CLI).
	runSetupFixtureAction(t, "load-fixtures", dsn, cfgFile, "")
	if out, err := executeRoot(t, "--config", cfgFile, "source", "add", "redrive-dns", "--type", "dns", "--seeds", "localhost", "--enable", "--yes"); err != nil {
		t.Fatalf("source add: %v\n%s", err, out)
	}

	// Seed a dead letter with a valid, rebuildable fact payload.
	ctx := context.Background()
	store, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var srcID int64
	if err := store.QueryRow(ctx, `SELECT id FROM data_sources WHERE name='redrive-dns'`).Scan(&srcID); err != nil {
		t.Fatal(err)
	}
	factJSON := `{"asset":{"kind":"ip","value":"203.0.113.203","ip":"203.0.113.203","sources":["redrive-eval"]},
                      "observation":{"source_cursor":"orig-3","payload_hash":"orig-hash-3","observed_at":"2026-01-15T00:00:00Z","provenance":"collection"}}`
	if _, err := store.Exec(ctx,
		`INSERT INTO ingestion_dead_letter (source_id, payload, reason) VALUES ($1, $2::jsonb, 'ingest failed: synthetic')`,
		srcID, `{"raw":{"fact":`+factJSON+`}}`); err != nil {
		t.Fatal(err)
	}
	_ = store.Close(ctx)

	out, err := executeRoot(t, "--config", cfgFile, "collect", "redrive", "--all")
	if err != nil {
		t.Fatalf("collect redrive: %v\n%s", err, out)
	}
	for _, want := range []string{"considered 1", "resolved 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("redrive output missing %q:\n%s", want, out)
		}
	}

	// The re-driven asset is searchable.
	searchFlagsReset() // a prior `search` in this test set nothing sticky, but be safe
	out, err = executeRoot(t, "--config", cfgFile, "search", `ip == "203.0.113.203"`, "-o", "table")
	if err != nil {
		t.Fatalf("search: %v\n%s", err, out)
	}
	if !strings.Contains(out, "203.0.113.203") {
		t.Errorf("re-driven asset not searchable:\n%s", out)
	}

	// Usage contract: a re-drive without scope is refused (exit 64).
	// (Reset the sticky cobra flags first — earlier invocations set
	// --all on the shared command tree.)
	collectRunReset()
	redriveReset()
	code, called := captureExit(t, func() {
		out, _ = executeRoot(t, "--config", cfgFile, "collect", "redrive")
	})
	if !called || code != ExitUsagePlanned || !strings.Contains(out, "E_USAGE") {
		t.Errorf("want E_USAGE exit 64, called=%v code=%d\n%s", called, code, out)
	}
}
