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

// artifacts_lf_test.go — platform-determinism regression for the
// artifact-hash pin (AC-117).  Detects the original Windows failure
// mode directly: if .gitattributes ever stops forcing LF for *.sql, a
// Windows checkout with core.autocrlf=true embeds CRLF bytes via
// go:embed, and TestPinnedMigrationArtifactsMatchRegister /
// TestPinnedDeploymentArtifactsMatchRegister drift from the register.
//
// This test fails BEFORE the hash comparison by asserting no CR (\r)
// byte is present in the embedded content — so the failure message
// points unambiguously at the line-ending normalization rather than at
// an opaque hash mismatch.  It runs on every platform (no build tag):
// Linux passes trivially (LF checkout); Windows passes only while
// .gitattributes enforces eol=lf.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Lutfifakee-Project/GSEER/deploy"
)

func TestPinnedArtifactsAreLFOnAllPlatforms(t *testing.T) {
	for name := range pinnedArtifacts {
		raw, err := FS.ReadFile(name)
		if err != nil {
			t.Fatalf("pinned artifact %s is not embedded: %v", name, err)
		}
		if bytes.IndexByte(raw, '\r') >= 0 {
			t.Errorf("pinned artifact %s contains CR (\\r) bytes — .gitattributes *.sql eol=lf is not enforced on this checkout; "+
				"a Windows core.autocrlf=true checkout would embed CRLF and drift the register hash", name)
		}
	}
	for name, sql := range map[string]string{
		"gseer_v0672_bootstrap_roles.sql":           deploy.BootstrapRolesSQL,
		"gseer_v0672_runtime_grants_setup.sql":      deploy.RuntimeGrantsSQL,
		"gseer_v0672_effective_privilege_audit.sql": deploy.EffectivePrivilegeAuditSQL,
	} {
		if strings.IndexByte(sql, '\r') >= 0 {
			t.Errorf("deploy artifact %s contains CR (\\r) bytes — .gitattributes *.sql eol=lf is not enforced on this checkout", name)
		}
	}
}
