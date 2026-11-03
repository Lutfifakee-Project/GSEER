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
	"os"
	"path/filepath"
	"testing"
)

// helper: load a testdata fixture by name, with no flags and a clean env.
func loadFixture(t *testing.T, name string) (*Config, *Error) {
	t.Helper()
	path := filepath.Join("testdata", name)
	// Clear all GSEER_* env vars so they don't leak into the test.
	for _, k := range []string{
		"GSEER_PROFILE", "GSEER_CLIENT_ENDPOINT", "GSEER_LISTEN", "GSEER_TOKEN",
		"GSEER_DB_DSN", "GSEER_DB_HOST", "GSEER_DB_PORT", "GSEER_DB_USER",
		"GSEER_DB_PASSWORD", "GSEER_DB_NAME", "GSEER_DB_SSLMODE",
		"GSEER_LOG_LEVEL", "GSEER_LOG_FORMAT", "GSEER_POLICY_EVALUATION_FIXTURES",
		"GSEER_CONFIG",
	} {
		t.Setenv(k, "")
	}
	return Load(Flags{ConfigPath: path})
}

// TestLoadValidSelfHosted verifies the happy path: a valid self-hosted
// config loads, defaults are applied, the token is hashed and cleared.
func TestLoadValidSelfHosted(t *testing.T) {
	c, e := loadFixture(t, "valid-selfhosted.yaml")
	if e != nil {
		t.Fatalf("expected success, got error: %v", e)
	}
	if c.Profile != ProfileSelfHosted {
		t.Errorf("Profile: want %q, got %q", ProfileSelfHosted, c.Profile)
	}
	if c.Server.Listen != "127.0.0.1:8443" {
		t.Errorf("Listen: want 127.0.0.1:8443, got %q", c.Server.Listen)
	}
	// Token must be hashed and the plaintext cleared.
	if c.Server.Token != "" {
		t.Errorf("Token plaintext must be cleared after hashing; got %q", c.Server.Token)
	}
	if c.Server.TokenHash == "" {
		t.Error("TokenHash must be set after Load")
	}
	if c.Server.TokenHash != HashToken("test-token-do-not-use-in-prod") {
		t.Errorf("TokenHash mismatch: expected hash of the plaintext token")
	}
	// Defaults populated.
	if c.Defaults.SearchMode != DefaultSearchMode {
		t.Errorf("Defaults.SearchMode: want %q, got %q", DefaultSearchMode, c.Defaults.SearchMode)
	}
	if c.Defaults.PostgresMajor != DefaultPostgresMajor {
		t.Errorf("Defaults.PostgresMajor: want %d, got %d", DefaultPostgresMajor, c.Defaults.PostgresMajor)
	}
}

// TestLoadValidClient verifies the client profile happy path.
func TestLoadValidClient(t *testing.T) {
	c, e := loadFixture(t, "valid-client.yaml")
	if e != nil {
		t.Fatalf("expected success, got error: %v", e)
	}
	if c.Profile != ProfileClient {
		t.Errorf("Profile: want %q, got %q", ProfileClient, c.Profile)
	}
	if c.Client.Endpoint != "https://gseer.example.com" {
		t.Errorf("Endpoint: want https://gseer.example.com, got %q", c.Client.Endpoint)
	}
}

// TestLoadPolicyActiveRejected verifies AC-7.x: any policy.active.*
// key is rejected with E_CONFIG_POLICY_ACTIVE_REJECTED.
func TestLoadPolicyActiveRejected(t *testing.T) {
	_, e := loadFixture(t, "invalid-policy-active.yaml")
	if e == nil {
		t.Fatal("expected E_CONFIG_POLICY_ACTIVE_REJECTED, got nil")
	}
	if e.Code != CodeConfigPolicyActiveRejected {
		t.Errorf("Code: want %q, got %q", CodeConfigPolicyActiveRejected, e.Code)
	}
	// The error must point to v0.0.2 (AC-7.x pointer requirement).
	if !contains(e.Error(), "v0.0.2") && !contains(e.Why, "v0.0.1") {
		t.Errorf("error must explain v0.0.1 scope; got: %v", e)
	}
}

// TestLoadUnknownKeyRejected verifies strict schema: unknown keys
// are rejected with E_CONFIG_UNKNOWN_KEY.
func TestLoadUnknownKeyRejected(t *testing.T) {
	_, e := loadFixture(t, "invalid-unknown-key.yaml")
	if e == nil {
		t.Fatal("expected E_CONFIG_UNKNOWN_KEY, got nil")
	}
	if e.Code != CodeConfigUnknownKey {
		t.Errorf("Code: want %q, got %q", CodeConfigUnknownKey, e.Code)
	}
}

// TestLoadInsecureRejected verifies unsafe keys (InsecureSkipVerify)
// are rejected with E_CONFIG_INSECURE.
func TestLoadInsecureRejected(t *testing.T) {
	_, e := loadFixture(t, "invalid-insecure.yaml")
	if e == nil {
		t.Fatal("expected E_CONFIG_INSECURE, got nil")
	}
	if e.Code != CodeConfigInsecure {
		t.Errorf("Code: want %q, got %q", CodeConfigInsecure, e.Code)
	}
}

// TestLoadMalformed verifies YAML parse errors produce E_CONFIG_PARSE.
func TestLoadMalformed(t *testing.T) {
	_, e := loadFixture(t, "invalid-malformed.yaml")
	if e == nil {
		t.Fatal("expected E_CONFIG_PARSE, got nil")
	}
	if e.Code != CodeConfigParse {
		t.Errorf("Code: want %q, got %q", CodeConfigParse, e.Code)
	}
}

// TestLoadFileNotFound verifies a missing --config path errors with
// E_CONFIG_FILE_NOT_FOUND.
func TestLoadFileNotFound(t *testing.T) {
	for _, k := range []string{"GSEER_CONFIG", "GSEER_PROFILE", "GSEER_CLIENT_ENDPOINT"} {
		t.Setenv(k, "")
	}
	_, e := Load(Flags{ConfigPath: "/nonexistent/path/config.yaml"})
	if e == nil {
		t.Fatal("expected E_CONFIG_FILE_NOT_FOUND, got nil")
	}
	if e.Code != CodeConfigFileNotFound {
		t.Errorf("Code: want %q, got %q", CodeConfigFileNotFound, e.Code)
	}
}

// TestLoadEnvOnly verifies that with no config file, env vars alone
// produce a valid client config (Source = "env-only").
func TestLoadEnvOnly(t *testing.T) {
	for _, k := range []string{"GSEER_CONFIG"} {
		t.Setenv(k, "")
	}
	t.Setenv("GSEER_PROFILE", "client")
	t.Setenv("GSEER_CLIENT_ENDPOINT", "https://env-only.example.com")
	c, e := Load(Flags{})
	if e != nil {
		t.Fatalf("env-only load failed: %v", e)
	}
	if c.Source != "env-only" {
		t.Errorf("Source: want 'env-only', got %q", c.Source)
	}
	if c.Client.Endpoint != "https://env-only.example.com" {
		t.Errorf("Endpoint from env: want https://env-only.example.com, got %q", c.Client.Endpoint)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || (len(s) > 0 && indexOf(s, substr) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestMain removes the default config file location influence so tests
// don't pick up ~/.gseer/config.yaml from the dev environment.
func TestMain(m *testing.M) {
	// HOME is overridden to a temp dir so defaultConfigPath() returns
	// a path that doesn't exist (env-only mode is the default in tests).
	tmp, err := os.MkdirTemp("", "gseer-config-test-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	_ = os.Setenv("HOME", tmp)
	os.Exit(m.Run())
}
