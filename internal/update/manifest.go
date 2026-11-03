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

// Package update implements the v0.1.0 signed update system (owner
// directive §5).
//
// Trust chain (in enforcement order — verification is FIRST, download
// is LAST):
//
//  1. SIGNATURE: the update manifest carries an Ed25519 signature over
//     its canonical core bytes; the public key is EMBEDDED in the
//     binary.  A manifest from any URL is worthless without a valid
//     signature — "never trust an unsigned update simply because it
//     comes from a familiar download URL".
//  2. VERSION: the manifest version must be strictly newer than the
//     running binary (semver, pre-release aware).  Downgrades and
//     same-version re-writes are refused without --force.
//  3. PLATFORM: the manifest must name an artifact for the running
//     GOOS/GOARCH pair (no best-effort guesses).
//  4. CHECKSUM: the downloaded artifact must match the manifest's
//     SHA-256 exactly, and its size must match.
//  5. REPLACE: the payload is written to a temp file NEXT TO the
//     target (same filesystem → rename is atomic), fsynced, mode-
//     preserved, then renamed over the running binary; the previous
//     binary is kept as <target>.bak for recovery.  A failure at any
//     step restores the .bak and removes temps.
//
// The HTTP fetcher caps the body size, enforces timeouts, redacts any
// credential-looking URL in errors (the httpx pattern), and refuses
// non-HTTP(S) schemes.
package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Manifest is the signed update manifest.  The SIGNATURE covers the
// canonical JSON of ManifestCore (the struct below minus Signature),
// marshaled by encoding/json (field order is declaration order →
// deterministic).  The signed file is the Manifest with signature set;
// verification re-marshals the core and checks the Ed25519 signature
// over those exact bytes.
type Manifest struct {
	Version    string     `json:"version"`
	ReleasedAt time.Time  `json:"released_at"`
	Artifacts  []Artifact `json:"artifacts"`
	NotesURL   string     `json:"notes_url,omitempty"`
	Signature  string     `json:"signature,omitempty"` // base64 Ed25519 over the canonical core
}

// Artifact is one platform payload inside a manifest.
type Artifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"` // hex
	Size   int64  `json:"size"`
}

// CanonicalCore renders the exact bytes the signature covers.
func (m Manifest) CanonicalCore() ([]byte, error) {
	m.Signature = ""
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Sign produces the signed manifest bytes from the core and a private
// key (the release tool's operation; exported for tests and
// cmd/gseer-release).
func Sign(m Manifest, priv ed25519.PrivateKey) ([]byte, error) {
	core, err := m.CanonicalCore()
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(priv, core)
	m.Signature = base64.StdEncoding.EncodeToString(sig)
	return json.MarshalIndent(m, "", "  ")
}

// VerifySignature checks the manifest's Ed25519 signature over its
// canonical core.
func (m Manifest) VerifySignature(pub ed25519.PublicKey) error {
	if m.Signature == "" {
		return &Error{
			Code: "E_UPDATE_UNSIGNED",
			What: "the update manifest carries no signature",
			Why:  "an unsigned manifest is never applied — the download URL proves nothing",
			Next: "obtain a signed manifest from the release channel (docs/UPDATE.md)",
		}
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return &Error{
			Code: "E_UPDATE_BAD_SIGNATURE",
			What: "the manifest signature is not a valid Ed25519 signature encoding",
			Why:  fmt.Sprintf("decode error: %v", err),
			Next: "refuse this manifest; fetch the official one",
		}
	}
	core, err := m.CanonicalCore()
	if err != nil {
		return &Error{Code: "E_UPDATE_MALFORMED", What: "manifest cannot be canonicalized", Why: err.Error()}
	}
	if !ed25519.Verify(pub, core, sig) {
		return &Error{
			Code: "E_UPDATE_BAD_SIGNATURE",
			What: "the manifest signature does not verify against the embedded public key",
			Why:  "the manifest was altered, forged, or signed by an unknown key",
			Next: "refuse this manifest; never apply updates that fail signature verification",
		}
	}
	return nil
}

// VerifyChecksum reports whether the artifact's recorded SHA-256 and
// size match the downloaded payload.
func (a Artifact) VerifyChecksum(payload []byte) error {
	sum := sha256.Sum256(payload)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, a.SHA256) {
		return &Error{
			Code: "E_UPDATE_CHECKSUM",
			What: "the downloaded artifact failed SHA-256 verification",
			Why:  fmt.Sprintf("got %s, manifest says %s", got[:16]+"…", a.SHA256[:16]+"…"),
			Next: "the artifact was corrupted or tampered with; do not apply it",
		}
	}
	if a.Size > 0 && int64(len(payload)) != a.Size {
		return &Error{
			Code: "E_UPDATE_CHECKSUM",
			What: "the downloaded artifact size does not match the manifest",
			Why:  fmt.Sprintf("got %d bytes, manifest says %d", len(payload), a.Size),
			Next: "refuse the artifact; the manifest is authoritative",
		}
	}
	return nil
}

// SelectArtifact returns the artifact for the running platform.  No
// guessing: a missing os/arch pair is a hard error.
func (m Manifest) SelectArtifact(goos, goarch string) (*Artifact, error) {
	for i := range m.Artifacts {
		if m.Artifacts[i].OS == goos && m.Artifacts[i].Arch == goarch {
			return &m.Artifacts[i], nil
		}
	}
	return nil, &Error{
		Code: "E_UPDATE_PLATFORM",
		What: fmt.Sprintf("the manifest has no artifact for %s/%s", goos, goarch),
		Why:  fmt.Sprintf("available: %s", m.artifactList()),
		Next: "wait for a release for this platform; never apply a foreign binary",
	}
}

func (m Manifest) artifactList() string {
	parts := make([]string, 0, len(m.Artifacts))
	for _, a := range m.Artifacts {
		parts = append(parts, a.OS+"/"+a.Arch)
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
}

// ParseManifest decodes and structurally validates manifest bytes.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, &Error{
			Code: "E_UPDATE_MALFORMED",
			What: "the update manifest is not valid JSON",
			Why:  err.Error(),
			Next: "fetch the official manifest",
		}
	}
	if m.Version == "" {
		return nil, &Error{Code: "E_UPDATE_MALFORMED", What: "manifest has no version"}
	}
	if len(m.Artifacts) == 0 {
		return nil, &Error{Code: "E_UPDATE_MALFORMED", What: "manifest has no artifacts"}
	}
	for i, a := range m.Artifacts {
		if a.URL == "" || len(a.SHA256) != 64 {
			return nil, &Error{
				Code: "E_UPDATE_MALFORMED",
				What: fmt.Sprintf("artifact %d (%s/%s) lacks a URL or a 64-hex sha256", i, a.OS, a.Arch),
				Next: "the manifest is broken; fetch the official one",
			}
		}
	}
	return &m, nil
}

// ParseVersion parses "vMAJOR.MINOR.PATCH[-PRERELEASE]" into comparable
// parts.  Errors name the offending version.
func ParseVersion(v string) (major, minor, patch int, pre string, err error) {
	s := strings.TrimSpace(v)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return 0, 0, 0, "", fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", v)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, e := strconv.Atoi(p)
		if e != nil || n < 0 {
			return 0, 0, 0, "", fmt.Errorf("version %q has a non-numeric component %q", v, p)
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], pre, nil
}

// CompareVersions returns -1/0/+1: a<b → -1.  A pre-release (-rc1)
// sorts BEFORE its release.  Unparseable versions are NEVER treated as
// newer (fail-closed comparison).
func CompareVersions(a, b string) (int, error) {
	amaj, amin, apat, apre, err := ParseVersion(a)
	if err != nil {
		return 0, fmt.Errorf("current version unparseable: %w", err)
	}
	bmaj, bmin, bpat, bpre, err := ParseVersion(b)
	if err != nil {
		return 0, fmt.Errorf("candidate version unparseable: %w", err)
	}
	if amaj != bmaj {
		return cmpInt(amaj, bmaj), nil
	}
	if amin != bmin {
		return cmpInt(amin, bmin), nil
	}
	if apat != bpat {
		return cmpInt(apat, bpat), nil
	}
	switch {
	case apre == "" && bpre == "":
		return 0, nil
	case apre == "":
		return 1, nil // release > pre-release
	case bpre == "":
		return -1, nil
	}
	return strings.Compare(apre, bpre), nil
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
