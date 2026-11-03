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
	"path/filepath"
	"testing"
)

// profile_defaults_test.go — tests for the owner's M2 review Finding 1:
// profile-dependent defaults must be applied based on the FINAL resolved
// profile (after env + flags), not the YAML/env profile.
//
// The 5 scenarios the owner specified:
//
//  1. YAML self-hosted → CLI client.
//  2. YAML client → CLI self-hosted.
//  3. Environment self-hosted → CLI client.
//  4. Environment client → CLI self-hosted.
//  5. No YAML + profile selected by CLI.
//
// The corrected Load order (loader.go) applies defaults AFTER env + flags,
// so the final resolved profile drives the defaults.

// helper: load a fixture with a clean env and the given flags.
func loadForProfileTest(t *testing.T, fixture string, flags Flags) (*Config, *Error) {
	t.Helper()
	// Clear profile-related env vars so they don't interfere.
	for _, k := range []string{
		"GSEER_PROFILE", "GSEER_CLIENT_ENDPOINT", "GSEER_DB_HOST",
		"GSEER_DB_PORT", "GSEER_DB_USER", "GSEER_DB_PASSWORD", "GSEER_DB_NAME",
		"GSEER_DB_DSN", "GSEER_DB_SSLMODE", "GSEER_CONFIG",
	} {
		t.Setenv(k, "")
	}
	path := ""
	if fixture != "" {
		path = filepath.Join("testdata", fixture)
	}
	flags.ConfigPath = path
	return Load(flags)
}

// Scenario 1a: YAML self-hosted (no DB fields) → CLI client.
// The final profile is client; DB defaults must NOT be applied.
// No profile conflict because the YAML didn't set any DB fields.
func TestProfileDefaults_Scenario1a_YAMLSelfHostedToCLIClient(t *testing.T) {
	c, e := loadForProfileTest(t, "selfhosted-no-db.yaml", Flags{Profile: "client", Endpoint: "https://x"})
	if e != nil {
		t.Fatalf("expected success, got %v", e)
	}
	if c.Profile != ProfileClient {
		t.Errorf("Profile: want client, got %q", c.Profile)
	}
	// DB defaults must NOT be applied (final profile is client).
	if c.Database.Host != "" {
		t.Errorf("Database.Host should be empty (client profile, no DB defaults); got %q", c.Database.Host)
	}
	if c.Database.Port != 0 {
		t.Errorf("Database.Port should be 0 (client profile); got %d", c.Database.Port)
	}
}

// Scenario 1b: YAML self-hosted WITH DB fields → CLI client.
// The final profile is client, but the YAML explicitly set database.* —
// this is a profile conflict (operator set DB fields for self-hosted but
// asked for client mode).  Must be rejected with E_CONFIG_PROFILE_CONFLICT.
// The Endpoint flag is set so the client.endpoint-required check passes
// and the database.* conflict check fires.
func TestProfileDefaults_Scenario1b_YAMLSelfHostedWithDBToCLIClient(t *testing.T) {
	_, e := loadForProfileTest(t, "valid-selfhosted.yaml", Flags{Profile: "client", Endpoint: "https://x"})
	if e == nil {
		t.Fatal("expected E_CONFIG_PROFILE_CONFLICT, got nil")
	}
	if e.Code != CodeConfigProfileConflict {
		t.Errorf("Code: want %q, got %q", CodeConfigProfileConflict, e.Code)
	}
}

// Scenario 2: YAML client → CLI self-hosted.
// The final profile is self-hosted; DB defaults MUST be applied.
func TestProfileDefaults_Scenario2_YAMLClientToCLISelfHosted(t *testing.T) {
	c, e := loadForProfileTest(t, "client-minimal.yaml", Flags{Profile: "self-hosted"})
	if e != nil {
		t.Fatalf("expected success, got %v", e)
	}
	if c.Profile != ProfileSelfHosted {
		t.Errorf("Profile: want self-hosted, got %q", c.Profile)
	}
	// DB defaults MUST be applied (final profile is self-hosted, no DSN set).
	if c.Database.Host != DefaultDBHost {
		t.Errorf("Database.Host: want %q (default applied for self-hosted), got %q", DefaultDBHost, c.Database.Host)
	}
	if c.Database.Port != DefaultDBPort {
		t.Errorf("Database.Port: want %d, got %d", DefaultDBPort, c.Database.Port)
	}
	if c.Server.Listen != DefaultListen {
		t.Errorf("Server.Listen: want %q, got %q", DefaultListen, c.Server.Listen)
	}
}

// Scenario 3: Environment self-hosted → CLI client.
// Env sets GSEER_PROFILE=self-hosted; CLI sets --profile=client.
// Final profile is client; DB defaults must NOT be applied.
func TestProfileDefaults_Scenario3_EnvSelfHostedToCLIClient(t *testing.T) {
	// Start with a clean env, then set GSEER_PROFILE=self-hosted.
	for _, k := range []string{
		"GSEER_PROFILE", "GSEER_CLIENT_ENDPOINT", "GSEER_DB_HOST",
		"GSEER_DB_PORT", "GSEER_DB_USER", "GSEER_DB_PASSWORD", "GSEER_DB_NAME",
		"GSEER_DB_DSN", "GSEER_DB_SSLMODE", "GSEER_CONFIG",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("GSEER_PROFILE", "self-hosted")
	// No fixture (env-only); CLI overrides to client.
	c, e := Load(Flags{Profile: "client", Endpoint: "https://x"})
	if e != nil {
		t.Fatalf("expected success, got %v", e)
	}
	if c.Profile != ProfileClient {
		t.Errorf("Profile: want client (CLI overrides env), got %q", c.Profile)
	}
	if c.Database.Host != "" {
		t.Errorf("Database.Host should be empty (client profile); got %q", c.Database.Host)
	}
}

// Scenario 4: Environment client → CLI self-hosted.
// Env sets GSEER_PROFILE=client; CLI sets --profile=self-hosted.
// Final profile is self-hosted; DB defaults MUST be applied.
func TestProfileDefaults_Scenario4_EnvClientToCLISelfHosted(t *testing.T) {
	for _, k := range []string{
		"GSEER_PROFILE", "GSEER_CLIENT_ENDPOINT", "GSEER_DB_HOST",
		"GSEER_DB_PORT", "GSEER_DB_USER", "GSEER_DB_PASSWORD", "GSEER_DB_NAME",
		"GSEER_DB_DSN", "GSEER_DB_SSLMODE", "GSEER_CONFIG",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("GSEER_PROFILE", "client")
	c, e := Load(Flags{Profile: "self-hosted"})
	if e != nil {
		t.Fatalf("expected success, got %v", e)
	}
	if c.Profile != ProfileSelfHosted {
		t.Errorf("Profile: want self-hosted (CLI overrides env), got %q", c.Profile)
	}
	if c.Database.Host != DefaultDBHost {
		t.Errorf("Database.Host: want %q (default for self-hosted), got %q", DefaultDBHost, c.Database.Host)
	}
}

// Scenario 5: No YAML + profile selected by CLI.
// No config file, no env profile; CLI sets --profile=self-hosted.
// Final profile is self-hosted; DB defaults MUST be applied.
func TestProfileDefaults_Scenario5_NoYAMLProfileByCLI(t *testing.T) {
	c, e := loadForProfileTest(t, "", Flags{Profile: "self-hosted"})
	if e != nil {
		t.Fatalf("expected success, got %v", e)
	}
	if c.Profile != ProfileSelfHosted {
		t.Errorf("Profile: want self-hosted, got %q", c.Profile)
	}
	if c.Source != "env-only" {
		t.Errorf("Source: want 'env-only', got %q", c.Source)
	}
	if c.Database.Host != DefaultDBHost {
		t.Errorf("Database.Host: want %q, got %q", DefaultDBHost, c.Database.Host)
	}
}

// Scenario 5b: No YAML + profile=client by CLI.
// No config file, no env; CLI sets --profile=client with --server.
func TestProfileDefaults_Scenario5b_NoYAMLClientByCLI(t *testing.T) {
	c, e := loadForProfileTest(t, "", Flags{Profile: "client", Endpoint: "https://x"})
	if e != nil {
		t.Fatalf("expected success, got %v", e)
	}
	if c.Profile != ProfileClient {
		t.Errorf("Profile: want client, got %q", c.Profile)
	}
	if c.Database.Host != "" {
		t.Errorf("Database.Host should be empty (client); got %q", c.Database.Host)
	}
}
