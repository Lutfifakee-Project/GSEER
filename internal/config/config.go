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

package config

import (
	"net"
	"net/url"
	"strconv"
)

// config.go — the Config struct hierarchy and supported profile types.
//
// The Config is the single in-memory representation produced by Load().
// Every surface (CLI, REST API, dashboard) reads from the same Config
// so that precedence and validation are applied once, at load time.

// Profile is the deployment profile (SPEC-03 §5).
type Profile string

const (
	// ProfileClient is the CLI-only profile: queries a remote endpoint,
	// no PostgreSQL, no local server.
	ProfileClient Profile = "client"
	// ProfileSelfHosted runs the local API + dashboard backed by
	// PostgreSQL.  Migrations run on setup --self-hosted and server start.
	ProfileSelfHosted Profile = "self-hosted"
	// ProfilePersonal runs the full engine against ONE embedded SQLite
	// database file (checkpoint B): no PostgreSQL, no separate service.
	// database.dsn holds the database FILE path (not a postgres URL);
	// first-run provisioning is `gseer setup --personal`.
	ProfilePersonal Profile = "personal"
)

// Config is the validated, in-memory configuration.
type Config struct {
	Profile      Profile            `yaml:"profile"`
	Client       ClientConfig       `yaml:"client"`
	Server       ServerConfig       `yaml:"server"`
	Database     DatabaseConfig     `yaml:"database"`
	Policy       PolicyConfig       `yaml:"policy"`
	Log          LogConfig          `yaml:"log"`
	Sources      []SourceConfig     `yaml:"sources"`
	Defaults     DefaultsConfig     `yaml:"defaults"`
	Fingerprints FingerprintsConfig `yaml:"fingerprints"`
	Update       UpdateConfig       `yaml:"update"`

	// Source is the file path the config was loaded from, or "env-only"
	// when no file was present (defaults + env + flags only).  Set by
	// Load; not part of the YAML schema.
	Source string `yaml:"-"`
}

// ClientConfig holds the client-profile settings (SPEC-03 §5).
type ClientConfig struct {
	// Endpoint is the remote GSEER server URL (e.g. https://gseer.example).
	// Required for the client profile; ignored for self-hosted.
	Endpoint string `yaml:"endpoint"`
}

// ServerConfig holds the self-hosted server settings.
type ServerConfig struct {
	// Listen is the bind address for the API + dashboard.  Loopback by
	// default; non-loopback requires explicit operator action + auth.
	Listen string `yaml:"listen"`
	// Token is the plaintext API token from config/env.  After Load, it
	// is hashed to TokenHash and this field is CLEARED so that no
	// in-memory representation retains the plaintext beyond Load.
	Token string `yaml:"token,omitempty"`
	// TokenHash is the SHA-256 hex digest of Token, computed during Load.
	// The API verifies incoming Bearer tokens against this hash.
	TokenHash string `yaml:"-"`
	// TrustedProxies lists the CIDRs (or bare IPs) of reverse proxies in
	// front of the server whose X-Forwarded-For header may be honored
	// for client-IP resolution (rate limiting, audit).  Empty (default)
	// = the peer address is used directly and X-Forwarded-For is
	// IGNORED — a client can never spoof its identity by setting the
	// header.  Only name proxies you actually run (e.g. 127.0.0.1 for a
	// local nginx).  Invalid entries are rejected at config load.
	TrustedProxies []string `yaml:"trusted_proxies,omitempty"`
}

// DatabaseConfig holds the PostgreSQL connection settings (PD-8).
type DatabaseConfig struct {
	// DSN is the full connection string.  When set, it overrides the
	// component fields below.  Format:
	//   postgres://user:password@host:port/dbname?sslmode=disable
	// This is the OPERATOR credential (database owner / migration
	// executor) in the v0.2.0 dual-credential model.
	DSN string `yaml:"dsn"`
	// RuntimeDSN (v0.2.0 M-B) is the least-privilege RUNTIME credential
	// — the `gseer_app` login role provisioned by the hardened
	// deployment order (bootstrap → migrate → grants → audit).
	// `gseer server start` REQUIRES it: the server never serves traffic
	// under the operator credential (OD-5 fail-closed model).  CLI
	// data-plane commands prefer it and say so explicitly when they
	// fall back to the operator credential.
	RuntimeDSN string `yaml:"runtime_dsn,omitempty"`
	// Component fields (used when DSN is not set).
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password,omitempty"` // never logged
	Name     string `yaml:"name"`
	// SSLMode controls connection encryption.  Loopback-only deployments
	// may use "disable"; non-loopback MUST use "require" or "verify-full".
	SSLMode string `yaml:"sslmode"`
	// MigrationLockTimeoutMs bounds how long the migration runner waits
	// for the migration advisory lock before failing with
	// DB_MIGRATION_LOCK_TIMEOUT.  Implemented as a session-scoped
	// statement_timeout inside the migration driver's Lock(): it covers
	// ONLY the pg_advisory_lock acquisition and is reset to 0 (verified)
	// before any migration SQL runs — slow migrations are never killed
	// by the lock-wait limit.  Range 1000..300000 ms; default
	// DefaultMigrationLockTimeoutMs (applied for explicit-DSN configs
	// too — see applyDatabaseDefaults).  Used by the storage layer
	// (internal/storage/postgres/lock.go + migratedriver.go).
	MigrationLockTimeoutMs int `yaml:"migration_lock_timeout_ms"`
}

// PolicyConfig holds policy switches.  The `active` sub-section is
// INTENTIONALLY ABSENT — any policy.active.* key is rejected at load
// time with E_CONFIG_POLICY_ACTIVE_REJECTED (AC-7.x).
type PolicyConfig struct {
	// EvaluationFixtures gates `setup --load-fixtures` (QS-6).  Default
	// false on non-dev installs; when false, the loader refuses with a
	// confirmation prompt.
	EvaluationFixtures bool `yaml:"evaluation_fixtures"`
}

// LogConfig holds slog settings.
type LogConfig struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // text | json
}

// FingerprintsConfig controls the v0.1.0 fingerprint rule engine
// (internal/fprule).  The engine classifies technology evidence that a
// stored observation ALREADY carries into `fingerprint.technologies` —
// it never produces observations and never contacts the network.
type FingerprintsConfig struct {
	// Disabled turns OFF rule-derived technology enrichment at
	// ingestion.  Zero value false = enabled (the useful default); the
	// inverted name avoids the YAML bool zero-value trap.
	Disabled bool `yaml:"disabled"`
	// RulesDir optionally loads operator-contributed rules on top of
	// the embedded seed set (contribution format: docs/RULES.md).  A
	// broken operator rule disables the registry — fail-closed, never
	// a silent no-op.
	RulesDir string `yaml:"rules_dir"`
}

// UpdateConfig points the updater at the signed release channel.
type UpdateConfig struct {
	// ManifestURL is the signed update manifest location (https URL or
	// local path).  The manifest MUST carry an Ed25519 signature that
	// verifies against the key embedded in the binary — the URL alone
	// proves nothing (docs/UPDATE.md).
	ManifestURL string `yaml:"manifest_url"`
}

// SourceConfig is a registered data source (config-only in v0.0.1;
// never collected).  The collection framework lands in v0.0.2.
type SourceConfig struct {
	Name     string `yaml:"name"`
	Type     string `yaml:"type"`     // e.g. "ct", "rdap", "dns"
	Endpoint string `yaml:"endpoint"` // the source's API URL (passive)
	Enabled  bool   `yaml:"enabled"`  // always false in v0.0.1 runtime
}

// DefaultsConfig records the owner-approved engineering defaults.
// PD-1 (search mode) is used by v0.0.1 runtime.  PD-2…PD-7 are recorded
// here as v0.0.2 planning defaults; NO v0.0.1 runtime code path reads
// them.  The owner's directive: "PD-2 through PD-7 are approved as
// engineering defaults only, not authorization to scan."
type DefaultsConfig struct {
	// SearchMode (PD-1) — the default GQL search mode.  Used by v0.0.1.
	SearchMode string `yaml:"search_mode"`
	// CollectionPorts (PD-2) — v0.0.2 planning default.  NOT used in v0.0.1.
	CollectionPorts []int `yaml:"collection_ports"`
	// Protocols (PD-3) — v0.0.2 planning default.  NOT used in v0.0.1.
	Protocols []string `yaml:"protocols"`
	// AuditSpoolBytes (PD-4) — v0.0.2 planning default.  NOT used in v0.0.1.
	AuditSpoolBytes int `yaml:"audit_spool_bytes"`
	// EmergencyReserveBytes (PD-5) — v0.0.2 planning default.  NOT used in v0.0.1.
	EmergencyReserveBytes int `yaml:"emergency_reserve_bytes"`
	// DecompressedCapBytes (PD-6) — v0.0.2 planning default.  NOT used in v0.0.1.
	DecompressedCapBytes int `yaml:"decompressed_cap_bytes"`
	// DecompressedCapMaxBytes (PD-7) — v0.0.2 planning default.  NOT used in v0.0.1.
	DecompressedCapMaxBytes int `yaml:"decompressed_cap_max_bytes"`
	// PostgresMajor (PD-8) — minimum supported PostgreSQL version.  Used by doctor (M3).
	PostgresMajor int `yaml:"postgres_major"`
}

// EffectiveDSN returns the connection string to use, preferring the
// explicit DSN over the component fields.  Used by the storage layer
// (M3) and the doctor reachability check (M3/M11).
//
// When building from components, the password is percent-encoded via
// net/url so that special characters (@, :, /, ?, #, spaces, quotes) are
// handled correctly.  Manual string interpolation would produce a
// malformed DSN for such passwords (owner M2 review Finding 4).
//
// Exported in M3: the storage layer (internal/storage/postgres) and the
// `gseer db migrate` / `doctor` commands resolve the operator's DSN
// through this single method so precedence (DSN > components) lives in
// exactly one place.  The raw result embeds the password — callers must
// pass it only to connection APIs and use postgres.RedactDSN for any
// display (M2/M3 logging restrictions).
func (d DatabaseConfig) EffectiveDSN() string {
	if d.DSN != "" {
		return d.DSN
	}
	sslmode := d.SSLMode
	if sslmode == "" {
		sslmode = "disable" // loopback default
	}
	return fmtDSN(d.User, d.Password, d.Host, d.Port, d.Name, sslmode)
}

// RuntimeCredentialDSN returns the connection string the RUNTIME data
// plane (server, search, collect, suppress, export, host) should use.
// The second return reports whether the runtime credential was EXPLICITLY
// configured (runtime_dsn / GSEER_DB_RUNTIME_DSN):
//
//	explicit=true  → hardened path: connect as gseer_app (least privilege).
//	explicit=false → the deployment has not been provisioned with the
//	  hardened role model; callers decide their own policy (the server
//	  refuses; CLI commands fall back to the operator DSN with an
//	  explicit notice — never silently).
func (d DatabaseConfig) RuntimeCredentialDSN() (dsn string, explicit bool) {
	if d.RuntimeDSN != "" {
		return d.RuntimeDSN, true
	}
	return d.EffectiveDSN(), false
}

// fmtDSN builds a postgres URL-format DSN from components using net/url
// for proper percent-encoding of the userinfo (user, password).  This
// correctly handles passwords containing @, :, /, ?, #, spaces, quotes,
// and other URI-special characters.
//
// Host handling uses net.JoinHostPort, which brackets IPv6 hosts
// automatically ([::1]:5432) and passes IPv4/hostname forms through
// unchanged (host:5432).  Manual Sprintf interpolation would produce an
// ambiguous, unparseable host for IPv6 (::1:5432) — owner DSN review.
//
// Format: postgres://user:password@host:port/dbname?sslmode=...
func fmtDSN(user, password, host string, port int, name, sslmode string) string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Path:   name,
	}
	if port > 0 {
		// net.JoinHostPort brackets IPv6 literals (::1 → [::1]:port)
		// and leaves IPv4/hostname forms unchanged (127.0.0.1:port).
		u.Host = net.JoinHostPort(host, strconv.Itoa(port))
	} else {
		u.Host = host
	}
	q := u.Query()
	q.Set("sslmode", sslmode)
	u.RawQuery = q.Encode()
	return u.String()
}
