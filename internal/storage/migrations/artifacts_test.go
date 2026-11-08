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

package migrations

// artifacts_test.go — the AC-117 artifact-hash check (roadmap M-A) plus
// the M-H hash pinning for migration 0017.
//
// Every hash below is registered:
//   - 0016 up/down: Rev 6.7.2 §1 Table B (owner register; DEPLOYMENT.md
//     §5 re-verified them byte-for-byte during the documentation round).
//   - deploy/sql scripts: the same register table (bootstrap / grants /
//     audit — steps 1, 3, 4 of the hardened deployment order).
//   - 0017 up/down: pinned at M-H authoring time (this repository's
//     release evidence; CHANGELOG/RELEASE_NOTES_v0.2.0).
//
// A byte drift in ANY pinned artifact fails this test — the artifacts
// are governance objects, not editable source.  A new revision replaces
// them wholesale together with new register hashes.

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/Lutfifakee-Project/GSEER/deploy"
)

// pinnedArtifacts maps embedded file name → registered SHA-256.
var pinnedArtifacts = map[string]string{
	// Pinned Rev 6.7.2 migration (internal/storage/migrations).
	"0016_suppression_policy_hardening.up.sql":   "4871884f2a627711d54dad71e2cd4c2a9b26cd4ffbcca6584e6580db8ae1b99c",
	"0016_suppression_policy_hardening.down.sql": "096bf7b744d63d818381f3a0a0c46550dc232d4361f8249e68820beefbc3ab24",
	// M-H safe-cast migration (hash-pinned at authoring time).
	"0017_search_safe_casts.up.sql":   "723e2c786d62515b85248c19b15b29ec0a27ffafef79a79eb2ace4cd265c867d",
	"0017_search_safe_casts.down.sql": "1074545a68e48e324b1816117c40664d1e9e453ffcd1e154d9e0c074f40711b4",
}

// pinnedDeployArtifacts maps deploy/sql file name → registered SHA-256.
var pinnedDeployArtifacts = map[string]string{
	"gseer_v0672_bootstrap_roles.sql":           "ff31bdeadb4ffbf97600786248a2fb2d09919e635e1d3a2fd9dcaa4bd1e9e2f4",
	"gseer_v0672_runtime_grants_setup.sql":      "fd816c8ca8b6e86d1517d58971f32b19ebe903fe79841396b5eafdcc7f1ad979",
	"gseer_v0672_effective_privilege_audit.sql": "d11a4167c4b1905072c74f2f5d7234ee8010dc22245198c7f5a9435ddf84dffe",
}

func TestPinnedMigrationArtifactsMatchRegister(t *testing.T) {
	for name, want := range pinnedArtifacts {
		raw, err := FS.ReadFile(name)
		if err != nil {
			t.Fatalf("pinned artifact %s is not embedded: %v", name, err)
		}
		got := sha256.Sum256(raw)
		if hex.EncodeToString(got[:]) != want {
			t.Errorf("pinned artifact %s drifted from its register hash:\n got  %x\n want %s\n(the artifacts are governance objects — do not edit; replace via a new owner-approved revision)",
				name, got, want)
		}
	}
}

func TestPinnedDeploymentArtifactsMatchRegister(t *testing.T) {
	for name, want := range pinnedDeployArtifacts {
		raw := map[string]string{
			"gseer_v0672_bootstrap_roles.sql":           deploy.BootstrapRolesSQL,
			"gseer_v0672_runtime_grants_setup.sql":      deploy.RuntimeGrantsSQL,
			"gseer_v0672_effective_privilege_audit.sql": deploy.EffectivePrivilegeAuditSQL,
		}[name]
		got := sha256.Sum256([]byte(raw))
		if hex.EncodeToString(got[:]) != want {
			t.Errorf("pinned deployment artifact %s drifted from its register hash:\n got  %x\n want %s",
				name, got, want)
		}
	}
}

// TestReservedRangeNeverMaterialized pins OD-4: no migration 0007–0015
// may ever exist (they are permanently reserved).
func TestReservedRangeNeverMaterialized(t *testing.T) {
	entries, err := FS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if len(name) < 4 {
			continue
		}
		prefix := name[:4]
		if prefix >= "0007" && prefix <= "0015" {
			t.Errorf("reserved-range migration %s exists (OD-4: versions 0007–0015 are permanently reserved)", name)
		}
	}
}
