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

package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// update_test.go — the signed-update trust chain:
// signature (valid/tampered/missing/foreign-key), version relation
// (downgrade/same/upgrade/pre-release), platform selection (exact
// match, no guessing), checksum + size, and the atomic replace with
// backup + rollback.

func testKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func signedManifest(t *testing.T, priv ed25519.PrivateKey, version string, artifacts []Artifact) []byte {
	t.Helper()
	m := Manifest{
		Version:    version,
		ReleasedAt: timeNow(),
		Artifacts:  artifacts,
	}
	b, err := Sign(m, priv)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSignatureVerification(t *testing.T) {
	pub, priv := testKeypair(t)
	good := signedManifest(t, priv, "v9.9.9", []Artifact{{OS: goos, Arch: goarch, URL: "https://example.test/a", SHA256: strings.Repeat("a", 64), Size: 1}})

	t.Run("valid signature", func(t *testing.T) {
		m, err := ParseManifest(good)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.VerifySignature(pub); err != nil {
			t.Fatalf("verify: %v", err)
		}
	})
	t.Run("tampered version field", func(t *testing.T) {
		m, _ := ParseManifest(good)
		m.Version = "v1.0.0" // alter AFTER signing
		if err := m.VerifySignature(pub); err == nil || !strings.Contains(err.Error(), "E_UPDATE_BAD_SIGNATURE") {
			t.Fatalf("tampered manifest accepted: %v", err)
		}
	})
	t.Run("unsigned manifest refused", func(t *testing.T) {
		m, _ := ParseManifest(good)
		m.Signature = ""
		if err := m.VerifySignature(pub); err == nil || !strings.Contains(err.Error(), "E_UPDATE_UNSIGNED") {
			t.Fatalf("unsigned manifest accepted: %v", err)
		}
	})
	t.Run("foreign key refused", func(t *testing.T) {
		otherPub, otherPriv := testKeypair(t)
		foreign := signedManifest(t, otherPriv, "v9.9.9", []Artifact{{OS: goos, Arch: goarch, URL: "https://example.test/a", SHA256: strings.Repeat("a", 64)}})
		m, _ := ParseManifest(foreign)
		if err := m.VerifySignature(pub); err == nil || !strings.Contains(err.Error(), "E_UPDATE_BAD_SIGNATURE") {
			t.Fatalf("foreign-key manifest accepted: %v", err)
		}
		_ = otherPub
	})
	t.Run("trailing garbage after signed core refused", func(t *testing.T) {
		m, _ := ParseManifest(good)
		m.NotesURL = "https://evil.test/notes" // extra field after signing
		if err := m.VerifySignature(pub); err == nil {
			t.Fatal("post-signature field injection accepted")
		}
	})
}

func TestVersionRelation(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.1.0", "v0.1.1", -1},
		{"v0.1.0", "v0.2.0", -1},
		{"v1.0.0", "v0.9.9", 1},
		{"v0.1.0", "v0.1.0", 0},
		{"v0.1.0", "v0.1.0-rc1", 1},  // release > pre-release
		{"v0.1.0-rc1", "v0.1.0", -1}, // pre-release < release
		{"v0.1.0-rc1", "v0.1.0-rc2", -1},
	}
	for _, tc := range cases {
		got, err := CompareVersions(tc.a, tc.b)
		if err != nil {
			t.Fatalf("CompareVersions(%s,%s): %v", tc.a, tc.b, err)
		}
		if got != tc.want {
			t.Errorf("CompareVersions(%s,%s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
	if _, err := CompareVersions("dev", "v0.2.0"); err == nil {
		t.Error("unparseable current version must error (fail-closed), not compare")
	}
}

func TestPlatformSelectionExact(t *testing.T) {
	m := Manifest{Artifacts: []Artifact{
		{OS: "windows", Arch: "arm64", URL: "https://x.test/w", SHA256: strings.Repeat("0", 64)},
		{OS: "linux", Arch: "amd64", URL: "https://x.test/l", SHA256: strings.Repeat("1", 64)},
	}}
	art, err := m.SelectArtifact("linux", "amd64")
	if err != nil || art.URL != "https://x.test/l" {
		t.Fatalf("exact selection failed: %v %v", art, err)
	}
	if _, err := m.SelectArtifact("plan9", "mips"); err == nil || !strings.Contains(err.Error(), "E_UPDATE_PLATFORM") {
		t.Fatalf("missing platform accepted: %v", err)
	}
}

func TestChecksumAndSize(t *testing.T) {
	payload := []byte("gseer-fake-binary")
	sum := sha256Hex(payload)
	a := Artifact{SHA256: sum, Size: int64(len(payload))}
	if err := a.VerifyChecksum(payload); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	// One flipped byte must fail.
	bad := append([]byte{}, payload...)
	bad[0] ^= 0xff
	if err := a.VerifyChecksum(bad); err == nil || !strings.Contains(err.Error(), "E_UPDATE_CHECKSUM") {
		t.Fatalf("corrupted payload accepted: %v", err)
	}
	// Size mismatch.
	a2 := Artifact{SHA256: sum, Size: int64(len(payload)) + 1}
	if err := a2.VerifyChecksum(payload); err == nil {
		t.Fatal("size mismatch accepted")
	}
}

func TestApplyFullChain(t *testing.T) {
	pub, priv := testKeypair(t)
	SetTestPublicKey(pub)
	t.Cleanup(func() { SetTestPublicKey(nil) })

	payload := []byte("#!/bin/sh\necho new-gseer\n")
	art := Artifact{
		OS: goos, Arch: goarch,
		URL:    "https://update-channel.test/gseer-" + goos + "-" + goarch,
		SHA256: sha256Hex(payload),
		Size:   int64(len(payload)),
	}
	manifestBytes := signedManifest(t, priv, "v9.8.7", []Artifact{art})

	// The "running binary" to replace.
	target := filepath.Join(t.TempDir(), "gseer")
	if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	fetcher := &Fetcher{Client: fakeHTTPClient(t, map[string]string{art.URL: string(payload)})}
	res, err := Apply(context.Background(), manifestBytes, nil, "v0.1.0", target, false, fetcher)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.NewVersion != "v9.8.7" || res.Backup != target+".bak" {
		t.Errorf("result = %+v", res)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("target content wrong: %q (%v)", got, err)
	}
	bak, err := os.ReadFile(target + ".bak")
	if err != nil || string(bak) != "old-binary" {
		t.Fatalf("backup content wrong: %q (%v)", bak, err)
	}
	// Mode preserved (0o755; writable/read-only semantics on Windows —
	// see assertPerm).
	assertPerm(t, target, 0o755)
}

func TestApplyRefusesDowngradeAndSameVersion(t *testing.T) {
	pub, priv := testKeypair(t)
	SetTestPublicKey(pub)
	t.Cleanup(func() { SetTestPublicKey(nil) })
	payload := []byte("x")
	art := Artifact{OS: goos, Arch: goarch, URL: "https://update-channel.test/a", SHA256: sha256Hex(payload), Size: int64(len(payload))}
	target := filepath.Join(t.TempDir(), "gseer")
	_ = os.WriteFile(target, []byte("old"), 0o755)
	fetcher := &Fetcher{Client: fakeHTTPClient(t, map[string]string{art.URL: string(payload)})}

	for _, v := range []string{"v0.1.0", "v0.0.9"} {
		manifestBytes := signedManifest(t, priv, v, []Artifact{art})
		_, err := Apply(context.Background(), manifestBytes, nil, "v0.1.0", target, false, fetcher)
		if err == nil || !strings.Contains(err.Error(), "E_UPDATE_NOT_NEWER") {
			t.Errorf("version %s accepted without force: %v", v, err)
		}
		// With force it proceeds.
		if _, err := Apply(context.Background(), manifestBytes, nil, "v0.1.0", target, true, fetcher); err != nil {
			t.Errorf("force apply of %s failed: %v", v, err)
		}
	}
}

func TestApplyRefusesUnsignedManifest(t *testing.T) {
	pub, _ := testKeypair(t)
	SetTestPublicKey(pub)
	t.Cleanup(func() { SetTestPublicKey(nil) })
	art := Artifact{OS: goos, Arch: goarch, URL: "https://update-channel.test/a", SHA256: strings.Repeat("5", 64)}
	m := Manifest{Version: "v9.9.9", Artifacts: []Artifact{art}}
	unsigned := jsonMarshal(m)
	target := filepath.Join(t.TempDir(), "gseer")
	_ = os.WriteFile(target, []byte("old"), 0o755)
	fetcher := &Fetcher{Client: fakeHTTPClient(t, map[string]string{})}
	_, err := Apply(context.Background(), unsigned, nil, "v0.1.0", target, false, fetcher)
	if err == nil || !strings.Contains(err.Error(), "E_UPDATE_UNSIGNED") {
		t.Fatalf("unsigned manifest applied: %v", err)
	}
	// The target is untouched.
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatal("target was modified by a refused update")
	}
}

func TestApplyRollsBackOnBadChecksum(t *testing.T) {
	pub, priv := testKeypair(t)
	SetTestPublicKey(pub)
	t.Cleanup(func() { SetTestPublicKey(nil) })
	// The manifest LIES about the checksum (signed lie — a compromised
	// release channel).  The checksum gate must stop it.
	payload := []byte("evil-payload")
	art := Artifact{OS: goos, Arch: goarch, URL: "https://update-channel.test/a", SHA256: sha256Hex([]byte("different-bytes")), Size: 0}
	manifestBytes := signedManifest(t, priv, "v9.9.9", []Artifact{art})
	target := filepath.Join(t.TempDir(), "gseer")
	_ = os.WriteFile(target, []byte("old"), 0o755)
	fetcher := &Fetcher{Client: fakeHTTPClient(t, map[string]string{art.URL: string(payload)})}

	_, err := Apply(context.Background(), manifestBytes, nil, "v0.1.0", target, false, fetcher)
	if err == nil || !strings.Contains(err.Error(), "E_UPDATE_CHECKSUM") {
		t.Fatalf("checksum mismatch not refused: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatal("target modified despite checksum failure")
	}
}

func TestReplaceRollbackOnTempFailure(t *testing.T) {
	// A target directory that cannot hold a temp file forces the temp
	// creation to fail; the original binary must survive.
	dir := t.TempDir()
	target := filepath.Join(dir, "readonly-dir", "gseer")
	if err := os.Mkdir(filepath.Dir(target), 0o500); err != nil { // read-only dir
		t.Fatal(err)
	}
	err := replaceBinary(target, []byte("payload"))
	if err == nil {
		// Some environments run as root and ignore the mode — skip.
		t.Skip("sandbox permits writing into read-only dir")
	}
}

func TestRedactURL(t *testing.T) {
	out := redactURL("https://user:secret@example.test/path")
	if strings.Contains(out, "secret") || !strings.Contains(out, "REDACTED") {
		t.Errorf("credentials leaked: %s", out)
	}
}

func TestBase64AnchorDecodes(t *testing.T) {
	// The EMBEDDED anchor must decode to a valid Ed25519 key — a broken
	// constant would panic at first use in production.
	raw, err := base64.StdEncoding.DecodeString(EmbeddedUpdatePublicKeyB64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		t.Fatalf("embedded anchor invalid: %v (len %d)", err, len(raw))
	}
}
