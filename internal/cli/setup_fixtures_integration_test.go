//go:build integration

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

package cli

// setup_fixtures_integration_test.go — E2E proof of the M4 CLI fixture
// wiring (build tag `integration`): the real `gseer setup` RunE with
// --load-fixtures / --purge-fixtures against a live PostgreSQL, through
// the QS-6 confirmation gate and the full provisioning core.
//
// Go test files are never importable across packages, so this file
// carries its own private database harness with the same shape as the
// other integration files (admin connection → throwaway database →
// drop-with-terminate cleanup).  UNLIKE the store-level round trip
// (internal/fixture/roundtrip_test.go), the database starts EMPTY and
// UNMIGRATED: the CLI itself must provision it (reachability → version
// → migrations) before the fixture action runs — that is part of what
// this test proves.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Lutfifakee-Project/GSEER/internal/fixture"
	"github.com/Lutfifakee-Project/GSEER/internal/storage"
	"github.com/Lutfifakee-Project/GSEER/internal/storage/postgres"
)

// cliAdminDSN matches the standalone PG 17 used for local development
// (scripts/ensure_pg.sh); override with GSEER_TEST_PG_ADMIN.
const cliAdminDSN = "postgres://gseer@127.0.0.1:5433/postgres?sslmode=disable"

var (
	cliDBMu      sync.Mutex
	cliDBCounter int
)

// cliTestDB creates a unique, empty, UNMIGRATED database and returns
// its DSN plus a drop-with-terminate cleanup func.
func cliTestDB(t *testing.T) (string, func()) {
	t.Helper()
	dsn := os.Getenv("GSEER_TEST_PG_ADMIN")
	if dsn == "" {
		dsn = cliAdminDSN
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("cannot connect to admin PG (%s): %v — start the test PostgreSQL and set GSEER_TEST_PG_ADMIN", dsn, err)
	}

	cliDBMu.Lock()
	cliDBCounter++
	name := fmt.Sprintf("gseer_m4_cli_%d_%d", os.Getpid(), cliDBCounter)
	cliDBMu.Unlock()

	if _, err := admin.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("cannot create test database %s: %v", name, err)
	}
	_ = admin.Close(ctx)

	testDSN := strings.Replace(dsn, "/postgres?", "/"+name+"?", 1)
	if testDSN == dsn {
		testDSN = strings.TrimSuffix(dsn, "/postgres") + "/" + name
	}

	cleanup := func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer ccancel()
		admin2, err := pgx.Connect(cctx, dsn)
		if err != nil {
			t.Logf("cleanup: cannot reconnect to drop %s: %v", name, err)
			return
		}
		defer func() { _ = admin2.Close(cctx) }()
		_, _ = admin2.Exec(cctx,
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()`, name)
		if _, err := admin2.Exec(cctx, `DROP DATABASE IF EXISTS `+name); err != nil {
			t.Logf("cleanup: drop database %s failed: %v", name, err)
		}
	}
	return testDSN, cleanup
}

// writeFixtureCLIConfig writes the config file the setup RunE reads:
// self-hosted profile, the test DSN, and the QS-6 policy switch.
func writeFixtureCLIConfig(t *testing.T, dsn string, evalFixtures bool) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	policy := "false"
	if evalFixtures {
		policy = "true"
	}
	content := "profile: self-hosted\ndatabase:\n  dsn: " + dsn +
		"\npolicy:\n  evaluation_fixtures: " + policy + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// runSetupFixtureAction invokes the real setup RunE with one fixture
// flag set and the given stdin, returning the captured output.
func runSetupFixtureAction(t *testing.T, action, dsn, cfgFile, stdin string) string {
	t.Helper()
	resetSetupFlags(t)
	withConfigPath(t, cfgFile)

	out := &bytes.Buffer{}
	cmd := findSetup(t)
	cmd.SetOut(out)
	cmd.SetIn(strings.NewReader(stdin))
	if err := cmd.Flags().Set(action, "true"); err != nil {
		t.Fatalf("set --%s: %v", action, err)
	}
	if err := cmd.Flags().Set("db-url", dsn); err != nil {
		t.Fatalf("set --db-url: %v", err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("setup --%s RunE: %v\noutput:\n%s", action, err, out.String())
	}
	return out.String()
}

// cliQueryCount runs a scalar count query through a fresh connection.
func cliQueryCount(t *testing.T, dsn, label, sql string, args ...any) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool for count (%s): %v", label, err)
	}
	defer pool.Close()
	var n int64
	if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("count query %s: %v", label, err)
	}
	return n
}

// TestSetupFixtureCLIRoundTrip — the full M4 CLI story on an empty,
// unmigrated database:
//
//  1. `setup --load-fixtures` (policy false, stdin "y") passes the QS-6
//     gate, provisions the schema (migrations 0001–0004 from the CLI),
//     and loads the corpus; the honesty banner and full-count line print.
//  2. The database holds exactly 4 assets / 6 services / 7 observations
//     and 7 search_index rows — all provenance=fixture.
//  3. A replay (second load) is idempotent: nothing inserted, counts
//     unchanged.
//  4. A REAL (non-fixture) observation lands next to the corpus and
//     merges into the already-fixture-tagged asset.
//  5. `setup --purge-fixtures` (stdin "y") removes exactly the fixture
//     rows; the real observation, its service and the merged asset all
//     survive, and the surviving asset's sources[] no longer claims
//     'fixture'.
//  6. A second purge is a zero-count no-op.
//  7. A purge with policy false and EMPTY stdin is refused with
//     E_FIXTURE_CONFIRM and changes nothing.
func TestSetupFixtureCLIRoundTrip(t *testing.T) {
	dsn, cleanup := cliTestDB(t)
	defer cleanup()

	// --- 1. First load through the CLI (provisions the empty database). ---
	cfgFile := writeFixtureCLIConfig(t, dsn, false)
	out := runSetupFixtureAction(t, "load-fixtures", dsn, cfgFile, "y\n")

	for _, want := range []string{
		"QS-6 fixture gate",
		"Proceed? [y/N]",
		"DB reachable at",
		"migrations: 8 applied, schema at head v17", // v0.2.0 set: 0001–0006 + pinned 0016 + 0017 as FILES (0007–0015 reserved, OD-4)
		"fixture source registered (data_sources id=",
		// Per-fact counters: 7 facts upsert 4 distinct assets (3 facts hit
		// an asset a sibling fact already inserted), 6 service-bearing facts
		// insert 6 services, all 7 observations are fresh.
		"fixtures loaded: 4 assets inserted, 3 updated; 6 services inserted, 0 updated; 7 observations inserted, 0 deduped",
		fixture.Banner,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("load output missing %q\ngot:\n%s", want, out)
		}
	}

	// --- 2. Row-level truth after the first load. --------------------------
	checks := map[string]struct {
		sql  string
		want int64
	}{
		"assets":             {`SELECT count(*) FROM assets`, 4},
		"services":           {`SELECT count(*) FROM services`, 6},
		"observations":       {`SELECT count(*) FROM observations`, 7},
		"fixture provenance": {`SELECT count(*) FROM observations WHERE provenance = 'fixture'`, 7},
		"search_index":       {`SELECT count(*) FROM search_index`, 7},
	}
	for label, c := range checks {
		if got := cliQueryCount(t, dsn, label, c.sql); got != c.want {
			t.Errorf("%s = %d, want %d", label, got, c.want)
		}
	}

	// --- 3. Replay is idempotent. ------------------------------------------
	// The replay counters are per-fact, not per-row: 7 asset upserts all
	// land on the 4 corpus assets (updated), 6 service upserts on the 6
	// services (updated), and all 7 observations dedupe on L1 identity.
	out2 := runSetupFixtureAction(t, "load-fixtures", dsn, cfgFile, "y\n")
	if !strings.Contains(out2, "0 assets inserted, 7 updated; 0 services inserted, 6 updated; 0 observations inserted, 7 deduped") {
		t.Errorf("replay output should report dedupe-not-insert, got:\n%s", out2)
	}
	if got := cliQueryCount(t, dsn, "observations after replay", `SELECT count(*) FROM observations`); got != 7 {
		t.Errorf("observations after replay = %d, want 7 (L1/L2 idempotency)", got)
	}

	// --- 4. A real observation lands next to the corpus. --------------------
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	var ctEvalID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO data_sources (name, connector, terms_ref, enabled)
VALUES ('ct-eval-cli', 'ct', 'test-eval-source', false) RETURNING id`).Scan(&ctEvalID); err != nil {
		t.Fatalf("register ct-eval-cli source: %v", err)
	}
	ip := "203.0.113.10" // the fixture corpus asset — must merge, not duplicate
	real := postgres.Fact{
		Asset: storage.Asset{
			Kind:    storage.AssetKindIP,
			Value:   ip,
			IP:      &ip,
			Sources: []string{"ct-eval-cli"},
		},
		Service: &storage.Service{Port: 444, Transport: "tcp", Protocol: "banner"},
		Observation: storage.Observation{
			SourceID:     ctEvalID,
			SourceCursor: "ct://real-cli-1",
			ObservedAt:   time.Now().UTC(),
			PayloadHash:  "real-hash-cli-1",
			Provenance:   "ct",
			ID:           987654321,
			Evidence:     map[string]any{"banner": map[string]any{"raw": "REAL"}},
		},
	}
	store := postgres.NewStore(pool)
	if _, err := store.IngestFact(ctx, &real); err != nil {
		t.Fatalf("IngestFact (real observation): %v", err)
	}

	// --- 5. Purge through the CLI: exactly the fixture rows go. -------------
	out3 := runSetupFixtureAction(t, "purge-fixtures", dsn, cfgFile, "y\n")
	for _, want := range []string{
		"fixtures purged: 7 observations, 6 services, 3 assets removed (one transaction)",
		fixture.Banner,
	} {
		if !strings.Contains(out3, want) {
			t.Errorf("purge output missing %q\ngot:\n%s", want, out3)
		}
	}

	postPurge := map[string]struct {
		sql  string
		want int64
	}{
		"observations after purge":       {`SELECT count(*) FROM observations`, 1},
		"fixture provenance after purge": {`SELECT count(*) FROM observations WHERE provenance = 'fixture'`, 0},
		"real observation survives":      {`SELECT count(*) FROM observations WHERE source_cursor = 'ct://real-cli-1'`, 1},
		"real service survives":          {`SELECT count(*) FROM services WHERE port = 444`, 1},
		"merged asset survives":          {`SELECT count(*) FROM assets WHERE value = '203.0.113.10'`, 1},
		"fixture stripped from sources":  {`SELECT count(*) FROM assets WHERE value = '203.0.113.10' AND NOT ('fixture' = ANY(sources))`, 1},
		"registry row remains":           {`SELECT count(*) FROM data_sources WHERE name = 'fixture'`, 1},
	}
	for label, c := range postPurge {
		if got := cliQueryCount(t, dsn, label, c.sql); got != c.want {
			t.Errorf("%s = %d, want %d", label, got, c.want)
		}
	}

	// --- 6. Second purge is a zero-count no-op. ------------------------------
	out4 := runSetupFixtureAction(t, "purge-fixtures", dsn, cfgFile, "y\n")
	if !strings.Contains(out4, "0 observations, 0 services, 0 assets removed") {
		t.Errorf("second purge should be a zero-count no-op, got:\n%s", out4)
	}

	// --- 7. Non-interactive purge is refused; nothing changes. ---------------
	resetSetupFlags(t)
	withConfigPath(t, cfgFile)
	outBuf := &bytes.Buffer{}
	cmd := findSetup(t)
	cmd.SetOut(outBuf)
	cmd.SetIn(strings.NewReader(""))
	if err := cmd.Flags().Set("purge-fixtures", "true"); err != nil {
		t.Fatalf("set --purge-fixtures: %v", err)
	}
	if err := cmd.Flags().Set("db-url", dsn); err != nil {
		t.Fatalf("set --db-url: %v", err)
	}
	err = cmd.RunE(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "E_FIXTURE_CONFIRM") {
		t.Errorf("want E_FIXTURE_CONFIRM refusal, got: %v", err)
	}
	if got := cliQueryCount(t, dsn, "observations after refused purge", `SELECT count(*) FROM observations`); got != 1 {
		t.Errorf("observations after refused purge = %d, want 1 (refusal must not touch the database)", got)
	}
}
