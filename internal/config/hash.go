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

package config

import (
	"crypto/sha256"
	"encoding/hex"
)

// hash.go — in-memory token digest (NOT "hashed at rest").
//
// IMPORTANT TERMINOLOGY (owner M2 review Finding 2):
//
// "Hashed at rest" means the persisted form (on disk) is the hash.  This
// package does NOT do that.  The operator's YAML config file and the
// GSEER_TOKEN env var retain the plaintext token — those are the
// operator's input surfaces, and this package does not modify them.
//
// What this package DOES do: compute the SHA-256 digest of the plaintext
// token during Load, store it in ServerConfig.TokenHash, and CLEAR the
// plaintext ServerConfig.Token field.  So the in-memory Config retains
// only the hash, not the plaintext.  This is "hashed in memory", not
// "hashed at rest".
//
// The distinction matters for the security report: the plaintext token
// is still present in the config file and env var (operator's
// responsibility to protect).  The application's in-memory state is
// clean, but the on-disk/env sources are not.
//
// # M10 API auth requirements (recorded for the future API layer)
//
//   - Verify Bearer tokens using crypto/subtle.ConstantTimeCompare
//     against TokenHash.  Never use == or strings.Equal for token
//     comparison (timing attack).
//   - Require high-entropy tokens: >= 256 bits (32 bytes).  The
//     .env.example documents `openssl rand -hex 32` for generation.
//   - Never log or return the token hash in any response or error.

// HashToken returns the SHA-256 hex digest of the plaintext token.
// Exported so that the API layer (M10) can hash incoming Bearer tokens
// for comparison against ServerConfig.TokenHash.
//
// M10 MUST compare using crypto/subtle.ConstantTimeCompare, not ==.
func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// hashAndClearToken computes ServerConfig.TokenHash from Token and then
// clears the plaintext Token field.  If Token is empty, TokenHash is
// left empty (the API will reject all Bearer tokens — auth required).
//
// This produces the in-memory digest; it does NOT modify the on-disk
// config file or the env var (those retain the plaintext).
func hashAndClearToken(s *ServerConfig) {
	if s.Token == "" {
		return
	}
	s.TokenHash = HashToken(s.Token)
	s.Token = ""
}
