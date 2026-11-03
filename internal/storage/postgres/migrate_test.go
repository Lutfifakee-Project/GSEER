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

package postgres

import "testing"

// wantHead is the pinned migration head.  A new migration file is a
// conscious schema change: bump this constant in the same commit (the
// M13 suppression migration took the head from 4 to 5).
const wantHead = 6

// TestHeadVersion — the embedded migration set's head version is
// parsed from the highest NNNN prefix (unit; no database).
func TestHeadVersion(t *testing.T) {
	head, err := HeadVersion()
	if err != nil {
		t.Fatalf("HeadVersion error: %v", err)
	}
	if head != wantHead {
		t.Errorf("HeadVersion = %d, want %d (0001_core, 0002_helper_views, 0003_search_index, 0004_search_indexes, 0005_suppressions)", head, wantHead)
	}
}

func TestMigrationFiles(t *testing.T) {
	names, err := MigrationFiles()
	if err != nil {
		t.Fatalf("MigrationFiles error: %v", err)
	}
	if len(names) != wantHead {
		t.Fatalf("got %d migration files, want %d: %v", len(names), wantHead, names)
	}
	want := []string{"0001_core.up.sql", "0002_helper_views.up.sql", "0003_search_index.up.sql", "0004_search_indexes.up.sql", "0005_suppressions.up.sql", "0006_collection.up.sql"}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("names[%d] = %s, want %s", i, names[i], w)
		}
	}
}

func TestFileVersion(t *testing.T) {
	cases := map[string]uint{
		"0001_core.up.sql":           1,
		"0002_helper_views.up.sql":   2,
		"0004_search_indexes.up.sql": 4,
		"0012_something.up.sql":      12,
		"garbage.sql":                0,
	}
	for name, want := range cases {
		if got := fileVersion(name); got != want {
			t.Errorf("fileVersion(%q) = %d, want %d", name, got, want)
		}
	}
}

func TestPendingFiles(t *testing.T) {
	got := pendingFiles(1, 4)
	if len(got) != 3 || got[0] != "0002_helper_views.up.sql" ||
		got[1] != "0003_search_index.up.sql" || got[2] != "0004_search_indexes.up.sql" {
		t.Errorf("pendingFiles(1,4) = %v", got)
	}
	if all := pendingFiles(0, 4); len(all) != 4 {
		t.Errorf("pendingFiles(0,4) = %v, want all four files", all)
	}
	if behind := pendingFiles(4, 4); len(behind) != 0 {
		t.Errorf("pendingFiles(4,4) = %v, want empty", behind)
	}
}
