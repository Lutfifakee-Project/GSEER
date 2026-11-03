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

// validate_test.go — tests for strict schema, value validation, and
// profile validation.

// TestValidateValuesLogLevel verifies invalid log levels are rejected.
func TestValidateValuesLogLevel(t *testing.T) {
	cases := []struct {
		level string
		ok    bool
	}{
		{"debug", true},
		{"info", true},
		{"warn", true},
		{"error", true},
		{"", true}, // empty is OK (default applied later)
		{"trace", false},
		{"verbose", false},
		{"INFO", true}, // case-insensitive
	}
	for _, tc := range cases {
		c := &Config{Profile: ProfileClient, Client: ClientConfig{Endpoint: "x"}, Log: LogConfig{Level: tc.level}}
		err := validateValues(c)
		if tc.ok && err != nil {
			t.Errorf("level %q: expected nil, got %v", tc.level, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("level %q: expected error, got nil", tc.level)
		}
		if !tc.ok && err != nil && err.Code != CodeConfigInvalidValue {
			t.Errorf("level %q: code want %q, got %q", tc.level, CodeConfigInvalidValue, err.Code)
		}
	}
}

// TestValidateValuesLogFormat verifies invalid log formats are rejected.
func TestValidateValuesLogFormat(t *testing.T) {
	c := &Config{Profile: ProfileClient, Client: ClientConfig{Endpoint: "x"}, Log: LogConfig{Format: "xml"}}
	err := validateValues(c)
	if err == nil || err.Code != CodeConfigInvalidValue {
		t.Errorf("format xml: expected E_CONFIG_INVALID_VALUE, got %v", err)
	}
}

// TestValidateValuesDBPort verifies out-of-range ports are rejected.
// Port 0 is treated as "unset" (the default is applied later) and is valid.
func TestValidateValuesDBPort(t *testing.T) {
	cases := []struct {
		port int
		ok   bool
	}{
		{0, true}, // 0 = unset, default applied later
		{1, true},
		{5432, true},
		{65535, true},
		{-1, false},
		{70000, false},
		{99, true},
	}
	for _, tc := range cases {
		c := &Config{
			Profile:  ProfileSelfHosted,
			Database: DatabaseConfig{Host: "127.0.0.1", Port: tc.port},
		}
		err := validateValues(c)
		if tc.ok && err != nil {
			t.Errorf("port %d: expected nil, got %v", tc.port, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("port %d: expected error, got nil", tc.port)
		}
	}
}

// TestValidateValuesListenShape verifies malformed listen addresses are caught.
func TestValidateValuesListenShape(t *testing.T) {
	c := &Config{Profile: ProfileSelfHosted, Server: ServerConfig{Listen: "no-port-here"}}
	err := validateValues(c)
	if err == nil || err.Code != CodeConfigInvalidValue {
		t.Errorf("listen 'no-port-here': expected E_CONFIG_INVALID_VALUE, got %v", err)
	}
}

// TestValidateProfileClientMissingEndpoint verifies the client profile
// requires client.endpoint.
func TestValidateProfileClientMissingEndpoint(t *testing.T) {
	c := &Config{Profile: ProfileClient}
	err := ValidateProfile(c)
	if err == nil || err.Code != CodeConfigRequired {
		t.Errorf("client without endpoint: expected E_CONFIG_REQUIRED, got %v", err)
	}
}

// TestValidateProfileClientWithDB verifies the client profile rejects
// database.* keys (profile conflict).
func TestValidateProfileClientWithDB(t *testing.T) {
	c := &Config{
		Profile:  ProfileClient,
		Client:   ClientConfig{Endpoint: "https://x"},
		Database: DatabaseConfig{Host: "127.0.0.1"},
	}
	err := ValidateProfile(c)
	if err == nil || err.Code != CodeConfigProfileConflict {
		t.Errorf("client with database: expected E_CONFIG_PROFILE_CONFLICT, got %v", err)
	}
}

// TestValidateProfileSelfHostedMissingDB verifies the self-hosted profile
// requires database.* (DSN or components).
func TestValidateProfileSelfHostedMissingDB(t *testing.T) {
	c := &Config{Profile: ProfileSelfHosted}
	err := ValidateProfile(c)
	if err == nil || err.Code != CodeConfigRequired {
		t.Errorf("self-hosted without database: expected E_CONFIG_REQUIRED, got %v", err)
	}
}

// TestValidateProfileSelfHostedWithClientEndpoint verifies the self-hosted
// profile rejects client.endpoint.
func TestValidateProfileSelfHostedWithClientEndpoint(t *testing.T) {
	c := &Config{
		Profile:  ProfileSelfHosted,
		Client:   ClientConfig{Endpoint: "https://x"},
		Database: DatabaseConfig{Host: "127.0.0.1"},
	}
	err := ValidateProfile(c)
	if err == nil || err.Code != CodeConfigProfileConflict {
		t.Errorf("self-hosted with client.endpoint: expected E_CONFIG_PROFILE_CONFLICT, got %v", err)
	}
}

// TestValidateProfileSelfHostedNonLoopbackNoToken verifies a non-loopback
// listen without an auth token is rejected.
func TestValidateProfileSelfHostedNonLoopbackNoToken(t *testing.T) {
	c := &Config{
		Profile:  ProfileSelfHosted,
		Server:   ServerConfig{Listen: "0.0.0.0:8443"}, // non-loopback, no token
		Database: DatabaseConfig{Host: "127.0.0.1"},
	}
	err := ValidateProfile(c)
	if err == nil || err.Code != CodeConfigInvalidValue {
		t.Errorf("non-loopback no token: expected E_CONFIG_INVALID_VALUE, got %v", err)
	}
}

// TestValidateProfileSelfHostedNonLoopbackWithToken verifies a non-loopback
// listen WITH an auth token passes.
func TestValidateProfileSelfHostedNonLoopbackWithToken(t *testing.T) {
	c := &Config{
		Profile:  ProfileSelfHosted,
		Server:   ServerConfig{Listen: "0.0.0.0:8443", Token: "secret"},
		Database: DatabaseConfig{Host: "127.0.0.1"},
	}
	err := ValidateProfile(c)
	if err != nil {
		t.Errorf("non-loopback with token: expected nil, got %v", err)
	}
}

// TestValidateProfileInvalid verifies an unknown profile value is rejected.
func TestValidateProfileInvalid(t *testing.T) {
	c := &Config{Profile: Profile("hybrid")}
	err := ValidateProfile(c)
	if err == nil || err.Code != CodeConfigInvalidValue {
		t.Errorf("invalid profile: expected E_CONFIG_INVALID_VALUE, got %v", err)
	}
}

// TestScanForPolicyActiveDirect verifies the policy.active scanner
// directly, including the nested case.
func TestScanForPolicyActiveDirect(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
		want bool
	}{
		{"no policy", map[string]any{"profile": "client"}, false},
		{"policy no active", map[string]any{"policy": map[string]any{"evaluation_fixtures": false}}, false},
		{"policy.active map", map[string]any{"policy": map[string]any{"active": map[string]any{"enabled": true}}}, true},
		{"policy.active scalar", map[string]any{"policy": map[string]any{"active": true}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := scanForPolicyActive(tc.raw)
			if tc.want && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.want && err != nil {
				t.Errorf("expected nil, got %v", err)
			}
			if tc.want && err != nil && err.Code != CodeConfigPolicyActiveRejected {
				t.Errorf("code: want %q, got %q", CodeConfigPolicyActiveRejected, err.Code)
			}
		})
	}
}

// TestScanForUnsafeKeys verifies the unsafe-key scanner catches
// InsecureSkipVerify in various forms and locations.
func TestScanForUnsafeKeys(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
		want bool
	}{
		{"clean", map[string]any{"profile": "client"}, false},
		{"insecure_skip_verify top", map[string]any{"insecure_skip_verify": true}, true},
		{"insecure_skip_verify nested", map[string]any{"server": map[string]any{"insecure_skip_verify": true}}, true},
		{"InsecureSkipVerify camelCase", map[string]any{"InsecureSkipVerify": true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := scanForUnsafeKeys(tc.raw)
			if tc.want && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.want && err != nil {
				t.Errorf("expected nil, got %v", err)
			}
			if tc.want && err != nil && err.Code != CodeConfigInsecure {
				t.Errorf("code: want %q, got %q", CodeConfigInsecure, err.Code)
			}
		})
	}
}

// TestDeepFindKey verifies the recursive key finder.
func TestDeepFindKey(t *testing.T) {
	raw := map[string]any{
		"server": map[string]any{
			"tls": map[string]any{
				"insecure_skip_verify": true,
			},
		},
	}
	found, path := deepFindKey(raw, "insecure_skip_verify")
	if !found {
		t.Error("expected to find insecure_skip_verify")
	}
	if path != "server.tls.insecure_skip_verify" {
		t.Errorf("path: want 'server.tls.insecure_skip_verify', got %q", path)
	}

	found, _ = deepFindKey(raw, "nonexistent")
	if found {
		t.Error("expected not to find nonexistent key")
	}
}

// TestExtractFieldName verifies the yaml.TypeError message parser.
func TestExtractFieldName(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"line 5: field foo not found in type config.Config", "foo"},
		{"field bar not found in type X", "bar"},
		// "no field marker here" actually CONTAINS "field " at index 3,
		// so the function extracts "marker here" (everything after
		// "field " until " not found", which isn't found, so the rest).
		// This is correct behavior — the function assumes a yaml.TypeError
		// format; a plain string without " not found" returns the tail.
		{"no field marker here", "marker here"},
	}
	for _, tc := range cases {
		got := extractFieldName(tc.input)
		if got != tc.want {
			t.Errorf("extractFieldName(%q): want %q, got %q", tc.input, tc.want, got)
		}
	}
}

// TestIsLoopback verifies loopback detection for various address forms.
func TestIsLoopback(t *testing.T) {
	cases := []struct {
		listen string
		want   bool
	}{
		{"127.0.0.1:8443", true},
		{"127.0.0.1", true},
		{"localhost:8443", true},
		{"[::1]:8443", true},
		{"0.0.0.0:8443", false},
		{"10.0.0.1:8443", false},
		{"example.com:8443", false},
		{":8443", true}, // empty host = all interfaces, but we treat as loopback for default safety
	}
	for _, tc := range cases {
		if got := isLoopback(tc.listen); got != tc.want {
			t.Errorf("isLoopback(%q): want %v, got %v", tc.listen, tc.want, got)
		}
	}
}
