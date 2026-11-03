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
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// validate.go — strict schema validation.
//
// The validation pass runs AFTER defaults, env overlay, and flag
// overrides are applied, so it sees the final effective values.  It
// performs three checks:
//
//  1. Policy-active rejection (AC-7.x): any `policy.active.*` key in
//     the YAML source is rejected with E_CONFIG_POLICY_ACTIVE_REJECTED.
//     This is a load-time enforcement, not just a structural absence —
//     the operator's config file is the input surface where an
//     accidental `policy.active.enabled: true` would otherwise be
//     silently accepted.
//
//  2. Unknown-key rejection (strict schema): any key not in the
//     documented schema is rejected with E_CONFIG_UNKNOWN_KEY.  This
//     catches typos and future-version keys early.
//
//  3. Unsafe-key rejection: InsecureSkipVerify and similar unsafe keys
//     are rejected with E_CONFIG_INSECURE (SPEC-08 §71).
//
//  4. Value validation: known keys with invalid values (bad log level,
//     malformed DSN, invalid listen address) are rejected with
//     E_CONFIG_INVALID_VALUE.
//
//  5. Profile validation: fields that don't match the chosen profile
//     are rejected with E_CONFIG_PROFILE_CONFLICT (profiles.go).

// scanForPolicyActive walks the raw YAML map (before strict decoding)
// to detect any `policy.active` or `policy.active.*` key.  This is done
// as a separate pass so the error message can be specific (pointing to
// v0.0.2) rather than the generic unknown-key error.
//
// The map is the loose decode of the YAML file into map[string]any.
func scanForPolicyActive(raw map[string]any) *Error {
	policy, ok := raw["policy"].(map[string]any)
	if !ok {
		return nil // no policy section, or policy is not a map
	}
	if active, exists := policy["active"]; exists {
		// policy.active is itself a key (a map or a scalar).  Either way
		// it's rejected — the entire `active` subtree is forbidden.
		_ = active // type doesn't matter; the key's existence is the violation
		return errPolicyActiveRejected("policy.active")
	}
	return nil
}

// scanForUnsafeKeys walks the raw YAML map looking for known unsafe
// keys: InsecureSkipVerify (anywhere), tls.insecure_skip_verify, etc.
// The walk is shallow over known locations plus a deep grep for the
// InsecureSkipVerify key name.
func scanForUnsafeKeys(raw map[string]any) *Error {
	if found, path := deepFindKey(raw, "insecure_skip_verify"); found {
		return errInsecure(path)
	}
	if found, path := deepFindKey(raw, "insecureskipverify"); found {
		return errInsecure(path)
	}
	return nil
}

// deepFindKey recursively searches the map for a key (case-insensitive)
// and returns (true, dotted-path) if found.  Used by scanForUnsafeKeys.
func deepFindKey(m map[string]any, key string) (bool, string) {
	keyLower := strings.ToLower(key)
	for k, v := range m {
		kLower := strings.ToLower(k)
		if kLower == keyLower {
			return true, k
		}
		if nested, ok := v.(map[string]any); ok {
			if found, path := deepFindKey(nested, key); found {
				return true, k + "." + path
			}
		}
	}
	return false, ""
}

// strictDecode decodes the YAML bytes into the Config struct using
// yaml.v3's KnownFields(true) mode, which rejects any key not present
// in the destination struct.  This is the strict-schema enforcement.
func strictDecode(data []byte, c *Config) error {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	return dec.Decode(c)
}

// validateValues checks the final effective values for semantic
// validity (after defaults + env + flags are applied).
func validateValues(c *Config) *Error {
	// Log level.
	switch strings.ToLower(c.Log.Level) {
	case "debug", "info", "warn", "error", "":
	default:
		return errInvalidValue("log.level", c.Log.Level,
			"log.level must be one of: debug, info, warn, error")
	}
	// Log format.
	switch strings.ToLower(c.Log.Format) {
	case "text", "json", "":
	default:
		return errInvalidValue("log.format", c.Log.Format,
			"log.format must be text or json")
	}
	// Database port range (if components are used).
	if c.Database.DSN == "" && c.Database.Port != 0 {
		if c.Database.Port < 1 || c.Database.Port > 65535 {
			return errInvalidValue("database.port", fmt.Sprintf("%d", c.Database.Port),
				"database.port must be between 1 and 65535")
		}
	}
	// Migration advisory-lock timeout range (M3).  Bounded so a typo
	// cannot make the CLI wait forever (too high) or fail spuriously on
	// slow disks (too low).
	if c.Database.MigrationLockTimeoutMs != 0 {
		if c.Database.MigrationLockTimeoutMs < 1000 || c.Database.MigrationLockTimeoutMs > 300000 {
			return errInvalidValue("database.migration_lock_timeout_ms",
				fmt.Sprintf("%d", c.Database.MigrationLockTimeoutMs),
				"database.migration_lock_timeout_ms must be between 1000 and 300000 milliseconds")
		}
	}
	// Server listen address basic shape (host:port).  Detailed
	// reachability is the doctor's job (M3/M11).
	if c.Server.Listen != "" {
		if !strings.Contains(c.Server.Listen, ":") {
			return errInvalidValue("server.listen", c.Server.Listen,
				"server.listen must be host:port (e.g. 127.0.0.1:8443)")
		}
	}
	return nil
}

// applyDefaults fills in owner-approved defaults (PD-1…PD-9 and the
// v0.0.1-specific defaults) for any field that is still zero after env
// and flag overlay.  Defaults are applied AFTER env/flags so that an
// explicit empty string from the operator does not override the default
// unless they really meant it (env overlay skips empty values for this
// reason).
//
// Database defaults are applied ONLY for the self-hosted profile — the
// client profile does not use PostgreSQL, and setting database defaults
// there would trigger a profile-conflict validation error.
func applyDefaults(c *Config) {
	if c.Profile == "" {
		c.Profile = ProfileClient // safest default: no PostgreSQL needed
	}
	if c.Server.Listen == "" && c.Profile == ProfileSelfHosted {
		c.Server.Listen = DefaultListen
	}
	if c.Log.Level == "" {
		c.Log.Level = DefaultLogLevel
	}
	if c.Log.Format == "" {
		c.Log.Format = DefaultLogFormat
	}
	if c.Profile == ProfileSelfHosted {
		applyDatabaseDefaults(c)
	}
	applyPDDefaults(c)
}

// applyDatabaseDefaults fills in the v0.0.1 database defaults for the
// self-hosted profile when the operator didn't specify a full DSN.
func applyDatabaseDefaults(c *Config) {
	// The migration lock timeout is INDEPENDENT of how the DSN is formed
	// (explicit DSN vs components) — it must be defaulted BEFORE the DSN
	// early-return below.  Regression (M4 review round): with an explicit
	// DSN the default was skipped, so the migration runner received 0 and
	// the advisory-lock wait was effectively unbounded — the exact
	// "permanent freeze" the M3 owner directive forbids.
	if c.Database.MigrationLockTimeoutMs == 0 {
		c.Database.MigrationLockTimeoutMs = DefaultMigrationLockTimeoutMs
	}
	if c.Database.DSN != "" {
		return // explicit DSN wins; component defaults not applied
	}
	if c.Database.Host == "" {
		c.Database.Host = DefaultDBHost
	}
	if c.Database.Port == 0 {
		c.Database.Port = DefaultDBPort
	}
	if c.Database.Name == "" {
		c.Database.Name = DefaultDBName
	}
	if c.Database.User == "" {
		c.Database.User = DefaultDBUser
	}
	if c.Database.SSLMode == "" {
		c.Database.SSLMode = "disable" // loopback default
	}
}

// applyPDDefaults populates the DefaultsConfig section with the
// owner-approved PD-1…PD-9 values.  These are always present in the
// in-memory Config regardless of whether the YAML file specified them.
// PD-1 (search mode) is used by v0.0.1 runtime; PD-2…PD-7 are recorded
// as v0.0.2 planning defaults (not used in v0.0.1 runtime); PD-8 is the
// PostgreSQL version floor (used by doctor, M3).
func applyPDDefaults(c *Config) {
	if c.Defaults.SearchMode == "" {
		c.Defaults.SearchMode = DefaultSearchMode
	}
	if c.Defaults.CollectionPorts == nil {
		c.Defaults.CollectionPorts = DefaultCollectionPorts
	}
	if c.Defaults.Protocols == nil {
		c.Defaults.Protocols = DefaultProtocols
	}
	if c.Defaults.AuditSpoolBytes == 0 {
		c.Defaults.AuditSpoolBytes = DefaultAuditSpoolBytes
	}
	if c.Defaults.EmergencyReserveBytes == 0 {
		c.Defaults.EmergencyReserveBytes = DefaultEmergencyReserveBytes
	}
	if c.Defaults.DecompressedCapBytes == 0 {
		c.Defaults.DecompressedCapBytes = DefaultDecompressedCapBytes
	}
	if c.Defaults.DecompressedCapMaxBytes == 0 {
		c.Defaults.DecompressedCapMaxBytes = DefaultDecompressedCapMaxBytes
	}
	if c.Defaults.PostgresMajor == 0 {
		c.Defaults.PostgresMajor = DefaultPostgresMajor
	}
}

// Validate is the top-level validation entry point.  It runs all five
// checks in order: profile → values → (policy.active and unsafe keys
// are checked at load time in loader.go against the raw map, not here,
// because the strict decode would already have rejected unknown keys).
func Validate(c *Config) *Error {
	if err := ValidateProfile(c); err != nil {
		return err
	}
	return validateValues(c)
}

// readConfigFile reads the YAML config file at path.  Returns the raw
// bytes.  Used by loader.go.
//
// both operator-controlled; this is a CLI config reader, not a web-facing
// file inclusion surface.  Validated against errFileNotFound for missing
// files and yaml.Unmarshal errors for malformed content.
//
//nolint:gosec // G304: path is the --config flag value or GSEER_CONFIG env,
func readConfigFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errFileNotFound(path)
		}
		return nil, newError(CodeConfigParse, fmt.Sprintf("cannot read config file %q", path)).
			withWhy(err.Error()).
			withNext("check file permissions and path; run: gseer doctor")
	}
	return data, nil
}
