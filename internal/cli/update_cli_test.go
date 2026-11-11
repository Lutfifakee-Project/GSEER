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

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lutfifakee-Project/gseer/internal/update"
)

// update_cli_test.go — the `gseer update` command surface: usage
// contracts and the end-to-end happy path against a LOCAL manifest +
// local file... the artifact fetch is HTTP-only, so the E2E uses the
// exit-seam on the refusal paths and the package tests own the apply
// chain.

func TestUpdateUsageContracts(t *testing.T) {
	resetUpdateFlags(t)
	// No channel configured → clear error, exit 2.
	t.Setenv("GSEER_UPDATE_MANIFEST_URL", "")
	out := ""
	code, called := captureExit(t, func() {
		out, _ = executeRoot(t, "update", "--check")
	})
	if !called || code != ExitInvalidInput || !strings.Contains(out, "E_UPDATE_NO_CHANNEL") {
		t.Errorf("want E_UPDATE_NO_CHANNEL exit %d, called=%v code=%d\n%s", ExitInvalidInput, called, code, out)
	}

	// --check and --apply are mutually exclusive.
	code, called = captureExit(t, func() {
		out, _ = executeRoot(t, "update", "--check", "--apply", "--manifest", "/tmp/whatever.json")
	})
	if !called || code != ExitUsagePlanned || !strings.Contains(out, "E_USAGE") {
		t.Errorf("want E_USAGE exit 64, called=%v code=%d\n%s", called, code, out)
	}
}

func TestUpdateCheckAgainstSignedLocalManifest(t *testing.T) {
	resetUpdateFlags(t)
	// Build a signed manifest whose version is NEWER than the test
	// binary's honest "dev"... wait — "dev" is unparseable by design
	// (fail-closed version comparison), so --check against dev reports
	// E_UPDATE_VERSION.  That IS the contract: an un-injected binary
	// cannot prove its version, so the updater refuses to act.  Pin it.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	update.SetTestPublicKey(pub)
	t.Cleanup(func() { update.SetTestPublicKey(nil) })

	payload := []byte("fake-artifact")
	manifest := update.Manifest{
		Version:   "v99.0.0",
		Artifacts: []update.Artifact{{OS: update.RuntimeGOOS(), Arch: update.RuntimeGOARCH(), URL: "https://localhost:1/nope", SHA256: sha256HexCli(payload), Size: int64(len(payload))}},
	}
	signed, err := update.Sign(manifest, priv)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, signed, 0o600); err != nil {
		t.Fatal(err)
	}

	out := ""
	code, called := captureExit(t, func() {
		out, _ = executeRoot(t, "update", "--check", "--manifest", path)
	})
	// "dev" current version → E_UPDATE_VERSION refusal (honest floor).
	if !called || code != ExitInvalidInput || !strings.Contains(out, "E_UPDATE_VERSION") {
		t.Errorf("want E_UPDATE_VERSION refusal for dev builds, called=%v code=%d\n%s", called, code, out)
	}
}

func TestUpdateCheckUnsignedManifestRefused(t *testing.T) {
	resetUpdateFlags(t)
	update.SetTestPublicKey(nil) // embedded anchor (no test key)
	manifest := update.Manifest{
		Version:   "v99.0.0",
		Artifacts: []update.Artifact{{OS: update.RuntimeGOOS(), Arch: update.RuntimeGOARCH(), URL: "https://localhost:1/nope", SHA256: strings.Repeat("a", 64)}},
	}
	raw, err := jsonMarshalForTest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "unsigned.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	out := ""
	code, called := captureExit(t, func() {
		out, _ = executeRoot(t, "update", "--check", "--manifest", path)
	})
	if !called || code != ExitInvalidInput || !strings.Contains(out, "E_UPDATE_UNSIGNED") {
		t.Errorf("want E_UPDATE_UNSIGNED refusal, called=%v code=%d\n%s", called, code, out)
	}
}

// resetUpdateFlags clears cobra's sticky per-command flags between
// invocations (flags persist on the shared RootCmd across Execute
// calls — see resetSetupFlags for the same pattern).
func resetUpdateFlags(t *testing.T) {
	t.Helper()
	c, _, err := RootCmd.Find([]string{"update"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"check", "apply", "force"} {
		if err := c.Flags().Set(f, "false"); err != nil {
			t.Fatalf("reset --%s: %v", f, err)
		}
	}
	if err := c.Flags().Set("manifest", ""); err != nil {
		t.Fatalf("reset --manifest: %v", err)
	}
}

func sha256HexCli(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func jsonMarshalForTest(v any) ([]byte, error) {
	return json.Marshal(v)
}
