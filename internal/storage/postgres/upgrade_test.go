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

package postgres

// upgrade_test.go — the v0.1.0 → v0.2.0 upgrade-path rehearsal (roadmap
// M-B exit condition: "v0.1.0-binary rollback rehearsal passed"; EU-6
// upgrade workflow).  It builds the EXACT v0.1.0 state — migrations
// 0001–0006 only, no hardened roles, ledger (6, false) — and then runs
// the v0.2.0 hardened deployment order over it:
//
//      bootstrap → migrate to head (0016 + 0017) → runtime grants → audit
//
// and proves the upgraded deployment actually serves the runtime paths
// (suppression writes go through the 0016 policy functions; the audit
// passes under gseer_app itself).

import (
	"context"
	"testing"
	"testing/fstest"

	migrate "github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/Lutfifakee-Project/GSEER/internal/storage/migrations"
	"github.com/Lutfifakee-Project/GSEER/internal/suppress"
)

// v010OnlyFS filters the embedded migration set to the six v0.1.0 files
// (0001–0006).  It reproduces what a v0.1.0 binary ships — nothing more.
func v010OnlyFS(t *testing.T) fstest.MapFS {
	t.Helper()
	fmap := fstest.MapFS{}
	for _, name := range []string{
		"0001_core.up.sql", "0001_core.down.sql",
		"0002_helper_views.up.sql", "0002_helper_views.down.sql",
		"0003_search_index.up.sql", "0003_search_index.down.sql",
		"0004_search_indexes.up.sql", "0004_search_indexes.down.sql",
		"0005_suppressions.up.sql", "0005_suppressions.down.sql",
		"0006_collection.up.sql", "0006_collection.down.sql",
	} {
		raw, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatalf("embedded v0.1.0 migration %s missing: %v", name, err)
		}
		fmap[name] = &fstest.MapFile{Data: raw}
	}
	return fmap
}

// TestV010ToV020UpgradePath — a v0.1.0 database upgrades to the v0.2.0
// hardened head with the pinned deployment order and stays fully
// functional (suppression policy path included).
func TestV010ToV020UpgradePath(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := testDB(t)
	defer cleanup()

	// ---- Stage 1: the v0.1.0 state (0001–0006, no hardened roles). ----
	src, err := iofs.New(v010OnlyFS(t), ".")
	if err != nil {
		t.Fatalf("v0.1.0 source: %v", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, effectiveMigrateDSNForTest(t, dsn))
	if err != nil {
		t.Fatalf("v0.1.0 migrate init: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("v0.1.0 migrations failed: %v", err)
	}
	if _, cerr := m.Close(); cerr != nil {
		t.Fatalf("v0.1.0 migrate close: %v", cerr)
	}
	st, err := GetMigrationStatus(ctx, dsn)
	if err != nil {
		t.Fatalf("v0.1.0 status: %v", err)
	}
	// From the v0.2.0 binary's perspective a v0.1.0 database IS behind —
	// that is exactly the upgrade condition.  What matters: the ledger
	// reads (6, false) with all six files applied.
	if st.Version != 6 || st.Dirty {
		t.Fatalf("v0.1.0 ledger = (%d, %v), want (6, false)", st.Version, st.Dirty)
	}
	if st.State != StateBehind {
		t.Fatalf("v0.1.0 stage state = %s, want BEHIND (head is 17 for this binary)", st.State)
	}

	// ---- Stage 2: the v0.2.0 hardened deployment order. ----
	if _, err := BootstrapRoles(ctx, dsn); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	applied, head, err := RunMigrations(dsn, lockTimeoutForTest())
	if err != nil {
		t.Fatalf("upgrade migrations: %v", err)
	}
	if head != 17 || applied != 2 { // exactly 0016 + 0017 on top of (6, false)
		t.Fatalf("upgrade applied %d to head %d, want 2 applied to head 17", applied, head)
	}
	if _, err := ApplyRuntimeGrants(ctx, dsn); err != nil {
		t.Fatalf("runtime grants: %v", err)
	}
	report, err := RunPrivilegeAudit(ctx, dsn)
	if err != nil {
		t.Fatalf("audit could not run: %v", err)
	}
	if !report.Passed {
		t.Fatalf("audit failed after upgrade: %v", report.Failures)
	}

	// ---- Stage 3: the upgraded deployment serves the runtime paths. ----
	pool, err := NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	store := NewStore(pool)

	rule, existed, err := store.AddSuppression(ctx, SuppressInput{
		Kind: suppress.KindIP, Value: "198.51.100.220", Reason: "post-upgrade", CreatedBy: "upgrade-test",
	})
	if err != nil || existed {
		t.Fatalf("post-upgrade suppression insert: rule=%+v existed=%v err=%v", rule, existed, err)
	}
	again, existed2, err := store.AddSuppression(ctx, SuppressInput{
		Kind: suppress.KindIP, Value: "198.51.100.220", Reason: "post-upgrade", CreatedBy: "upgrade-test",
	})
	if err != nil || !existed2 || again.ID != rule.ID {
		t.Fatalf("post-upgrade re-add: existed=%v err=%v", existed2, err)
	}
	if ok, err := store.RemoveSuppression(ctx, rule.ID); err != nil || !ok {
		t.Fatalf("post-upgrade removal: ok=%v err=%v", ok, err)
	}
}

// effectiveMigrateDSNForTest wraps the DSN with the test lock timeout.
func effectiveMigrateDSNForTest(t *testing.T, dsn string) string {
	t.Helper()
	eff, err := effectiveMigrateDSN(dsn, lockTimeoutForTest())
	if err != nil {
		t.Fatalf("effective DSN: %v", err)
	}
	return eff
}
