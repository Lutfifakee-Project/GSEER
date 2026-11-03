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
	"testing"
)

// env_test.go — tests for the environment-variable overlay.

// TestApplyEnv verifies each env var maps to the right field.
func TestApplyEnv(t *testing.T) {
	c := &Config{}
	envVars := map[string]string{
		"GSEER_PROFILE":                    "self-hosted",
		"GSEER_CLIENT_ENDPOINT":            "https://env.example.com",
		"GSEER_LISTEN":                     "127.0.0.1:9999",
		"GSEER_TOKEN":                      "env-token",
		"GSEER_DB_DSN":                     "postgres://u:p@h:5432/d",
		"GSEER_DB_HOST":                    "dbhost",
		"GSEER_DB_PORT":                    "6543",
		"GSEER_DB_USER":                    "envuser",
		"GSEER_DB_PASSWORD":                "envpass",
		"GSEER_DB_NAME":                    "envdb",
		"GSEER_DB_SSLMODE":                 "require",
		"GSEER_LOG_LEVEL":                  "debug",
		"GSEER_LOG_FORMAT":                 "json",
		"GSEER_POLICY_EVALUATION_FIXTURES": "true",
	}
	for k, v := range envVars {
		t.Setenv(k, v)
	}
	applyEnv(c)

	if c.Profile != ProfileSelfHosted {
		t.Errorf("Profile: want self-hosted, got %q", c.Profile)
	}
	if c.Client.Endpoint != "https://env.example.com" {
		t.Errorf("Client.Endpoint: got %q", c.Client.Endpoint)
	}
	if c.Server.Listen != "127.0.0.1:9999" {
		t.Errorf("Server.Listen: got %q", c.Server.Listen)
	}
	if c.Server.Token != "env-token" {
		t.Errorf("Server.Token: got %q", c.Server.Token)
	}
	if c.Database.DSN != "postgres://u:p@h:5432/d" {
		t.Errorf("Database.DSN: got %q", c.Database.DSN)
	}
	if c.Database.Host != "dbhost" {
		t.Errorf("Database.Host: got %q", c.Database.Host)
	}
	if c.Database.Port != 6543 {
		t.Errorf("Database.Port: got %d", c.Database.Port)
	}
	if c.Database.User != "envuser" {
		t.Errorf("Database.User: got %q", c.Database.User)
	}
	if c.Database.Password != "envpass" {
		t.Errorf("Database.Password: got %q", c.Database.Password)
	}
	if c.Database.Name != "envdb" {
		t.Errorf("Database.Name: got %q", c.Database.Name)
	}
	if c.Database.SSLMode != "require" {
		t.Errorf("Database.SSLMode: got %q", c.Database.SSLMode)
	}
	if c.Log.Level != "debug" {
		t.Errorf("Log.Level: got %q", c.Log.Level)
	}
	if c.Log.Format != "json" {
		t.Errorf("Log.Format: got %q", c.Log.Format)
	}
	if !c.Policy.EvaluationFixtures {
		t.Error("Policy.EvaluationFixtures: want true")
	}
}

// TestApplyEnvEmpty verifies empty env vars are treated as unset
// (don't overwrite YAML values).
func TestApplyEnvEmpty(t *testing.T) {
	c := &Config{
		Profile: ProfileSelfHosted,
		Server:  ServerConfig{Listen: "127.0.0.1:8443"},
	}
	t.Setenv("GSEER_PROFILE", "")
	t.Setenv("GSEER_LISTEN", "")
	applyEnv(c)
	if c.Profile != ProfileSelfHosted {
		t.Errorf("Profile: want self-hosted (empty env should not override), got %q", c.Profile)
	}
	if c.Server.Listen != "127.0.0.1:8443" {
		t.Errorf("Listen: want 127.0.0.1:8443 (empty env should not override), got %q", c.Server.Listen)
	}
}

// TestApplyEnvEvalFixturesValues verifies the boolean parsing for
// GSEER_POLICY_EVALUATION_FIXTURES accepts common truthy/falsy forms.
func TestApplyEnvEvalFixturesValues(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"true", true},
		{"1", true},
		{"yes", true},
		{"on", true},
		{"TRUE", true},
		{"false", false},
		{"0", false},
		{"no", false},
		{"off", false},
		{"", false}, // empty = unset, value stays at default
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			c := &Config{}
			if tc.value == "" {
				// t.Setenv with "" is treated as set-empty; LookupEnv
				// returns (ok=true, v=""). Our code checks `ok && v != ""`,
				// so this is effectively unset.
				t.Setenv("GSEER_POLICY_EVALUATION_FIXTURES", "")
			} else {
				t.Setenv("GSEER_POLICY_EVALUATION_FIXTURES", tc.value)
			}
			applyEnv(c)
			// For empty string, the value should NOT be set (stays false).
			if tc.value == "" {
				if c.Policy.EvaluationFixtures {
					t.Errorf("empty env should not set EvaluationFixtures; got true")
				}
				return
			}
			if c.Policy.EvaluationFixtures != tc.want {
				t.Errorf("value %q: want %v, got %v", tc.value, tc.want, c.Policy.EvaluationFixtures)
			}
		})
	}
}

// TestApplyEnvDBPortInvalid verifies an invalid port string is ignored
// (not fatal at env-overlay time; surfaces later in value validation).
func TestApplyEnvDBPortInvalid(t *testing.T) {
	c := &Config{Database: DatabaseConfig{Port: 5432}}
	t.Setenv("GSEER_DB_PORT", "not-a-number")
	applyEnv(c)
	if c.Database.Port != 5432 {
		t.Errorf("invalid port env should not override; want 5432, got %d", c.Database.Port)
	}
}

// TestApplyEnvMigrationLockTimeout — AC-M3-18: the migration
// advisory-lock timeout env var parses into
// DatabaseConfig.MigrationLockTimeoutMs; invalid values are ignored
// (they surface later in validateValues as E_CONFIG_INVALID_VALUE).
func TestApplyEnvMigrationLockTimeout(t *testing.T) {
	t.Run("valid value applies", func(t *testing.T) {
		c := &Config{}
		t.Setenv("GSEER_DB_MIGRATION_LOCK_TIMEOUT_MS", "60000")
		applyEnv(c)
		if c.Database.MigrationLockTimeoutMs != 60000 {
			t.Errorf("MigrationLockTimeoutMs = %d, want 60000", c.Database.MigrationLockTimeoutMs)
		}
	})
	t.Run("below range ignored", func(t *testing.T) {
		c := &Config{}
		t.Setenv("GSEER_DB_MIGRATION_LOCK_TIMEOUT_MS", "999")
		applyEnv(c)
		if c.Database.MigrationLockTimeoutMs != 0 {
			t.Errorf("below-range value must be ignored; got %d", c.Database.MigrationLockTimeoutMs)
		}
	})
	t.Run("above range ignored", func(t *testing.T) {
		c := &Config{}
		t.Setenv("GSEER_DB_MIGRATION_LOCK_TIMEOUT_MS", "300001")
		applyEnv(c)
		if c.Database.MigrationLockTimeoutMs != 0 {
			t.Errorf("above-range value must be ignored; got %d", c.Database.MigrationLockTimeoutMs)
		}
	})
	t.Run("non-numeric ignored", func(t *testing.T) {
		c := &Config{}
		t.Setenv("GSEER_DB_MIGRATION_LOCK_TIMEOUT_MS", "soon")
		applyEnv(c)
		if c.Database.MigrationLockTimeoutMs != 0 {
			t.Errorf("non-numeric value must be ignored; got %d", c.Database.MigrationLockTimeoutMs)
		}
	})
	t.Run("default applied for self-hosted", func(t *testing.T) {
		c := &Config{Profile: ProfileSelfHosted}
		applyDefaults(c)
		if c.Database.MigrationLockTimeoutMs != DefaultMigrationLockTimeoutMs {
			t.Errorf("default = %d, want %d", c.Database.MigrationLockTimeoutMs, DefaultMigrationLockTimeoutMs)
		}
	})
	t.Run("out-of-range YAML value rejected", func(t *testing.T) {
		c := &Config{Profile: ProfileSelfHosted, Database: DatabaseConfig{
			Host: "127.0.0.1", MigrationLockTimeoutMs: 500,
		}}
		err := validateValues(c)
		if err == nil || err.Code != CodeConfigInvalidValue {
			t.Errorf("want E_CONFIG_INVALID_VALUE, got %v", err)
		}
	})
}
