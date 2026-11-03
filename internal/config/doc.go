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

// Package config implements YAML configuration loading, environment-
// variable overlay, CLI flag precedence, strict schema validation, and
// actionable E_* errors for the GSEER v0.0.1 binary (SPEC-03 §5, §7).
//
// Precedence (highest wins): CLI flags > environment variables > YAML
// config file > owner-approved defaults (PD-1…PD-9).
//
// Load order (corrected per owner M2 review):
// YAML → env → flags → defaults (profile-dependent) → hash token → validate.
// Defaults are applied AFTER env+flags so profile-dependent defaults
// (database components) use the FINAL resolved profile.
//
// # Profiles (SPEC-03 §5)
//
// Two profiles are supported:
//
//   - client      — CLI-only; queries a configured remote endpoint.
//     No PostgreSQL, no local server.
//   - self-hosted — runs the local API + dashboard server backed by
//     PostgreSQL.  Migrations run on `setup --self-hosted`
//     and `server start` (M3).
//
// Neither profile ships data.  v0.0.1 has no public endpoint.
//
// # Strict schema (AC-7.x)
//
// Unknown YAML keys are rejected with E_CONFIG_UNKNOWN_KEY.  Any
// `policy.active.*` key is rejected with E_CONFIG_POLICY_ACTIVE_REJECTED
// and a pointer to v0.0.2 — active collection is not part of v0.0.1 and
// the config layer enforces this at load time, not just structurally.
//
// `InsecureSkipVerify` and similar unsafe keys are rejected with
// E_CONFIG_INSECURE for any control-plane endpoint.
//
// # Credential handling (honest terminology)
//
// The package distinguishes four credential surfaces:
//
//  1. In-memory token digest — ServerConfig.TokenHash (SHA-256 hex).
//     Computed during Load; the plaintext ServerConfig.Token field is
//     cleared immediately after hashing.  The in-memory Config never
//     retains the plaintext token beyond the Load call.
//
//  2. Plaintext token in the YAML config file — the operator's input.
//     The config file is the operator's responsibility; this package
//     does NOT hash the file contents on disk.  "Hashed at rest" would
//     be a false claim if the file still contains the plaintext.
//
//  3. Plaintext token in GSEER_TOKEN env var — the operator's input.
//     Same as above: the env var retains the plaintext; only the
//     in-memory Config has the hash.
//
//  4. Database password (DatabaseConfig.Password) — needed for the
//     PostgreSQL connection DSN.  NOT hashed; passed to pgx (M3) as
//     part of the connection string.  The password is percent-encoded
//     in the DSN via net/url (Finding 4), but the plaintext is required
//     for the connection.  This field must NEVER appear in logs or
//     diagnostic output.
//
// # M10 API auth requirements (recorded for the future API layer)
//
// The API layer (M10) must:
//
//   - Verify Bearer tokens using crypto/subtle.ConstantTimeCompare
//     against TokenHash (never ==, never strings.Equal).
//   - Require high-entropy tokens (≥ 256 bits / 32 bytes; the
//     .env.example documents `openssl rand -hex 32`).
//   - Never log or return the token hash in any response or error.
//
// # Logging restrictions (M3/M10)
//
// Until a SEC-6 slog redaction handler lands (M10), the following are
// PENDING and must be enforced by discipline in M3:
//
//   - DatabaseConfig.Password must not appear in any log line, error
//     message, or doctor output.
//   - The effective DSN (which embeds the password) must not be logged
//     directly; M3 must log a redacted form (host:port/db, no userinfo).
//   - ServerConfig.TokenHash may appear in diagnostics (it's a hash,
//     not plaintext), but the plaintext Token (if transiently present
//     during Load) must never be logged.
//
// The `yaml:"password,omitempty"` struct tag only controls YAML
// marshaling (omit when empty); it does NOT provide logging redaction.
package config
