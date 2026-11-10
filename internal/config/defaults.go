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

// defaults.go — owner-approved engineering defaults (PD-1 … PD-9).
//
// The owner approved these values on 2026-11-02 in the Phase 1
// authorization directive.  PD-1 is used by v0.0.1 runtime code (the
// default search mode).  PD-8 is used by the doctor PostgreSQL version
// check (M3+).  PD-9 is recorded in internal/version.  PD-2 … PD-7 are
// recorded here as the v0.0.2 planning defaults; NO v0.0.1 runtime code
// path reads them — they exist so that when v0.0.2 lands the collector
// framework, the defaults are already owner-approved and do not need to
// be re-negotiated.
//
// The owner's directive is explicit: "PD-2 through PD-7 are approved as
// engineering defaults only, not authorization to perform scanning or
// activate collection."

// DefaultSearchMode (PD-1) — the default GQL search mode.
// Used by v0.0.1: the `gseer search --mode` flag default and the
// SearchService default (M10).
const DefaultSearchMode = "service"

// DefaultCollectionPorts (PD-2) — initial collection ports for v0.0.2.
// NOT used by any v0.0.1 runtime code path.
var DefaultCollectionPorts = []int{80, 443, 8080, 8443}

// DefaultProtocols (PD-3) — initial protocols for v0.0.2.
// NOT used by any v0.0.1 runtime code path.
var DefaultProtocols = []string{"https", "tls"}

// DefaultAuditSpoolBytes (PD-4) — audit spool size for v0.0.2.
// NOT used by any v0.0.1 runtime code path.
const DefaultAuditSpoolBytes = 10 * 1024 * 1024 // 10 MiB

// DefaultEmergencyReserveBytes (PD-5) — emergency reserve for v0.0.2.
// NOT used by any v0.0.1 runtime code path.
const DefaultEmergencyReserveBytes = 64 * 1024 // 64 KiB

// DefaultDecompressedCapBytes (PD-6) — default decompressed response cap for v0.0.2.
// NOT used by any v0.0.1 runtime code path.
const DefaultDecompressedCapBytes = 256 * 1024 // 256 KiB

// DefaultDecompressedCapMaxBytes (PD-7) — absolute decompressed response cap for v0.0.2.
// NOT used by any v0.0.1 runtime code path.
const DefaultDecompressedCapMaxBytes = 1024 * 1024 // 1 MiB

// DefaultPostgresMajor (PD-8 as amended by OD-7) — minimum supported
// PostgreSQL major version.  Used by the doctor check and the setup
// provisioning floor.  v0.1.0 shipped with floor 15 (PD-8); v0.2.0
// raises it to 16 (OD-7, OWNER APPROVED): the Rev 6.7.2 privilege model
// (membership SET/INHERIT options in pg_auth_members, the closure-
// walking audit, and the 0016/0017 migrations' floor guards) is only
// expressible on PostgreSQL 16+.
const DefaultPostgresMajor = 16

// DefaultPostgresImage — the Compose-provisioned PostgreSQL image
// (SPEC-02 §1: postgres:17-alpine).  Used by the `setup --with-compose`
// compose-file generation (M2/M11).
const DefaultPostgresImage = "postgres:17-alpine"

// v0.0.1-specific defaults (not PD-numbered, but normative per SPEC-03 §5).

const (
	// DefaultListen is the loopback bind for the self-hosted server.
	// Non-loopback binding requires explicit operator action and auth.
	DefaultListen = "127.0.0.1:8443"

	// DefaultLogLevel is the slog level for v0.0.1.
	DefaultLogLevel = "info"

	// DefaultLogFormat is the slog format (text for CLI-friendliness).
	DefaultLogFormat = "text"

	// DefaultDBPort is the default PostgreSQL port (loopback).
	DefaultDBPort = 5432

	// DefaultDBName is the default database name.
	DefaultDBName = "gseer"

	// DefaultDBUser is the default database user.
	DefaultDBUser = "gseer"

	// DefaultDBHost is the default database host (loopback).
	DefaultDBHost = "127.0.0.1"

	// DefaultMigrationLockTimeoutMs bounds the migration advisory-lock
	// wait (M3): how long `gseer setup --self-hosted` / `gseer server
	// start` waits for the golang-migrate pg_advisory_lock before
	// failing with DB_MIGRATION_LOCK_TIMEOUT.  30s is enough for a
	// concurrent-start race (the second process waits for the first to
	// finish) while never leaving the operator facing a frozen CLI.
	// Range: 1000..300000 ms (validated in validateValues).
	DefaultMigrationLockTimeoutMs = 30000

	// DefaultEvaluationFixtures is the default for policy.evaluation_fixtures.
	// False on non-dev installs (QS-6): `setup --load-fixtures` is refused
	// with a confirmation prompt when false.
	DefaultEvaluationFixtures = false
)
