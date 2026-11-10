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
	"os"
	"strconv"
	"strings"
)

// env.go — environment-variable overlay.
//
// Environment variables override YAML file values; CLI flags override
// environment variables (handled in loader.go).  The mapping is:
//
//      GSEER_PROFILE                    → Profile
//      GSEER_CLIENT_ENDPOINT            → Client.Endpoint
//      GSEER_LISTEN                     → Server.Listen
//      GSEER_TOKEN                      → Server.Token (plaintext; hashed in Load)
//      GSEER_SERVER_TRUSTED_PROXIES     → Server.TrustedProxies (CIDR list, M16)
//      GSEER_DB_DSN                     → Database.DSN (overrides components)
//      GSEER_DB_RUNTIME_DSN             → Database.RuntimeDSN (the gseer_app
//                                         runtime credential, v0.2.0 M-B)
//      GSEER_DB_HOST                    → Database.Host
//      GSEER_DB_PORT                    → Database.Port
//      GSEER_DB_USER                    → Database.User
//      GSEER_DB_PASSWORD                → Database.Password
//      GSEER_DB_NAME                    → Database.Name
//      GSEER_DB_SSLMODE                 → Database.SSLMode
//      GSEER_DB_MIGRATION_LOCK_TIMEOUT_MS → Database.MigrationLockTimeoutMs
//      GSEER_LOG_LEVEL                  → Log.Level
//      GSEER_LOG_FORMAT                 → Log.Format
//      GSEER_POLICY_EVALUATION_FIXTURES → Policy.EvaluationFixtures
//      GSEER_UPDATE_MANIFEST_URL        → Update.ManifestURL (v0.1.0)
//      GSEER_FINGERPRINTS_DISABLED      → Fingerprints.Disabled (v0.1.0)
//      GSEER_FINGERPRINTS_RULES_DIR     → Fingerprints.RulesDir (v0.1.0)
//
// All GSEER_* env vars are matched case-sensitively.  An env var that
// is set but empty ("") is treated as unset (os.LookupEnv distinguishes),
// EXCEPT GSEER_DB_PASSWORD where an explicitly-set empty value is
// meaningful (trust auth, dev setups).

// envSetter is a function that applies an env-var value to the config.
// The bool return is `applied` — false means the env var was unset or
// empty (in which case the YAML value is preserved).
type envSetter func(c *Config, value string) (applied bool)

// envMapping is the table of env vars → setters.  The table-driven form
// keeps applyEnv's cyclomatic complexity low and makes the mapping
// auditable at a glance.
var envMapping = []struct {
	name   string
	setter envSetter
}{
	{"GSEER_PROFILE", func(c *Config, v string) bool {
		v = strings.TrimSpace(v)
		if v == "" {
			return false
		}
		c.Profile = Profile(v)
		return true
	}},
	{"GSEER_CLIENT_ENDPOINT", func(c *Config, v string) bool {
		if v == "" {
			return false
		}
		c.Client.Endpoint = v
		return true
	}},
	{"GSEER_LISTEN", func(c *Config, v string) bool {
		if v == "" {
			return false
		}
		c.Server.Listen = v
		return true
	}},
	{"GSEER_TOKEN", func(c *Config, v string) bool {
		if v == "" {
			return false
		}
		c.Server.Token = v
		return true
	}},
	// GSEER_SERVER_TRUSTED_PROXIES (M16): comma-separated CIDRs/bare IPs
	// of proxies whose X-Forwarded-For may be honored.  Parsed strictly
	// in validate (invalid entry = config error, fail-closed).
	{"GSEER_SERVER_TRUSTED_PROXIES", func(c *Config, v string) bool {
		if v == "" {
			return false
		}
		parts := strings.Split(v, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		c.Server.TrustedProxies = parts
		return true
	}},
	{"GSEER_DB_DSN", func(c *Config, v string) bool {
		if v == "" {
			return false
		}
		c.Database.DSN = v
		return true
	}},
	// GSEER_DB_RUNTIME_DSN (v0.2.0 M-B): the `gseer_app` least-privilege
	// runtime credential DSN.  The server REQUIRES it (the hardened
	// deployment order runs the fail-closed privilege audit against
	// gseer_app and never serves traffic under the operator credential);
	// CLI data-plane commands prefer it and fall back to the operator
	// DSN with an explicit notice when it is unset.
	{"GSEER_DB_RUNTIME_DSN", func(c *Config, v string) bool {
		if v == "" {
			return false
		}
		c.Database.RuntimeDSN = v
		return true
	}},
	{"GSEER_DB_HOST", func(c *Config, v string) bool {
		if v == "" {
			return false
		}
		c.Database.Host = v
		return true
	}},
	{"GSEER_DB_PORT", func(c *Config, v string) bool {
		port, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || port < 1 || port > 65535 {
			return false // invalid port ignored; surfaces later in validation
		}
		c.Database.Port = port
		return true
	}},
	{"GSEER_DB_USER", func(c *Config, v string) bool {
		if v == "" {
			return false
		}
		c.Database.User = v
		return true
	}},
	// Password: plain `ok` check — an explicitly-set empty value is
	// meaningful (trust auth, dev setups).  This setter always applies
	// when the env var is set, even to "".
	{"GSEER_DB_PASSWORD", func(c *Config, v string) bool {
		c.Database.Password = v
		return true
	}},
	{"GSEER_DB_NAME", func(c *Config, v string) bool {
		if v == "" {
			return false
		}
		c.Database.Name = v
		return true
	}},
	{"GSEER_DB_SSLMODE", func(c *Config, v string) bool {
		if v == "" {
			return false
		}
		c.Database.SSLMode = v
		return true
	}},
	{"GSEER_DB_MIGRATION_LOCK_TIMEOUT_MS", func(c *Config, v string) bool {
		v = strings.TrimSpace(v)
		if v == "" {
			return false
		}
		ms, err := strconv.Atoi(v)
		if err != nil || ms < 1000 || ms > 300000 {
			return false // invalid value ignored; surfaces later in validation
		}
		c.Database.MigrationLockTimeoutMs = ms
		return true
	}},
	{"GSEER_LOG_LEVEL", func(c *Config, v string) bool {
		v = strings.TrimSpace(v)
		if v == "" {
			return false
		}
		c.Log.Level = v
		return true
	}},
	{"GSEER_LOG_FORMAT", func(c *Config, v string) bool {
		v = strings.TrimSpace(v)
		if v == "" {
			return false
		}
		c.Log.Format = v
		return true
	}},
	{"GSEER_UPDATE_MANIFEST_URL", func(c *Config, v string) bool {
		if v == "" {
			return false
		}
		c.Update.ManifestURL = v
		return true
	}},
	{"GSEER_FINGERPRINTS_DISABLED", func(c *Config, v string) bool {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "on":
			c.Fingerprints.Disabled = true
		case "false", "0", "no", "off":
			c.Fingerprints.Disabled = false
		default:
			return false
		}
		return true
	}},
	{"GSEER_FINGERPRINTS_RULES_DIR", func(c *Config, v string) bool {
		if v == "" {
			return false
		}
		c.Fingerprints.RulesDir = v
		return true
	}},
	{"GSEER_POLICY_EVALUATION_FIXTURES", func(c *Config, v string) bool {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "on":
			c.Policy.EvaluationFixtures = true
		case "false", "0", "no", "off":
			c.Policy.EvaluationFixtures = false
		default:
			return false
		}
		return true
	}},
}

// applyEnv applies environment-variable overrides to the config.  Only
// env vars that are explicitly set (and non-empty, except for password)
// overwrite the YAML values; unset env vars leave the YAML value in place.
func applyEnv(c *Config) {
	for _, m := range envMapping {
		v, ok := os.LookupEnv(m.name)
		if !ok {
			continue
		}
		m.setter(c, v)
	}
}
