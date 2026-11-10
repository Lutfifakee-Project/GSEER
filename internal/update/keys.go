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

package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"runtime"
)

// keys.go — the update trust anchor.
//
// EmbeddedUpdatePublicKey is the base64 Ed25519 PUBLIC key that signed
// release manifests must verify against.  It is baked into the binary
// on purpose: an update manifest is only as trustworthy as this key,
// never as the URL it was fetched from.
//
// Key ceremony (docs/UPDATE.md §2): the project owner generates the
// keypair with `gseer-release keygen`, keeps the PRIVATE key offline
// with the release process, and replaces the constant below with the
// matching public half before cutting a release.  The private key must
// NEVER live in this repository.  Rotation = new keypair + new embedded
// anchor + a signed revocation note in the release channel.
const EmbeddedUpdatePublicKeyB64 = "hB+FoV/HjZ4AQlsaCosmdMkzX3LsyWkDwZeGNFAHlsc="

// testKey overrides the embedded anchor in tests only (SetTestKey is
// not exported beyond the package seam used by the CLI tests).
var testKey ed25519.PublicKey

// SetTestPublicKey swaps the trust anchor (TESTS ONLY — the CLI wires
// it through an internal seam so the embedded key stays the only
// production trust root).
func SetTestPublicKey(pub ed25519.PublicKey) {
	testKey = pub
}

// TrustAnchor returns the active public key (test override wins).
func TrustAnchor() ed25519.PublicKey {
	if testKey != nil {
		return testKey
	}
	return embeddedKey()
}

func embeddedKey() ed25519.PublicKey {
	raw, err := base64.StdEncoding.DecodeString(EmbeddedUpdatePublicKeyB64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		panic(fmt.Sprintf("update: embedded public key is invalid (decode err %v, len %d) — build broken", err, len(raw)))
	}
	return ed25519.PublicKey(raw)
}

func ed25519Key(pub []byte) ed25519.PublicKey {
	if len(pub) == 0 {
		return TrustAnchor()
	}
	return ed25519.PublicKey(pub)
}

// goos/goarch indirections keep the platform selection testable.
var (
	goos   = runtime.GOOS
	goarch = runtime.GOARCH
)

// RuntimeGOOS/RuntimeGOARCH expose the platform pair for display.
func RuntimeGOOS() string   { return goos }
func RuntimeGOARCH() string { return goarch }
