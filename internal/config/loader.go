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
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// loader.go — the Load entry point.
//
// Load is the single function that produces a validated *Config.  It
// implements the precedence: CLI flags > environment variables > YAML
// config file > owner-approved defaults (PD-1…PD-9).
//
// The flow:
//
//  1. Resolve the config file path (--config flag > GSEER_CONFIG env >
//     platform default).  If no file exists, proceed with defaults +
//     env + flags only (Source = "env-only").
//  2. Read the file; loose-decode into map[string]any.
//  3. scanForPolicyActive — reject any policy.active.* (AC-7.x).
//  4. scanForUnsafeKeys — reject InsecureSkipVerify etc.
//  5. strictDecode into Config (KnownFields(true) — unknown keys → error).
//  6. applyDefaults — fill in PD-1…PD-9 and v0.0.1 defaults.
//  7. applyEnv — environment-variable overlay.
//  8. applyFlags — CLI flag overrides (highest precedence).
//  9. hashAndClearToken — SHA-256 the token, clear the plaintext.
// 10. Validate — profile + value validation.
//
// Steps 3 and 4 run against the raw map BEFORE strict decode so that
// policy.active.* and unsafe keys produce pointed error messages rather
// than the generic unknown-key error.

// Flags holds the CLI flags that participate in config resolution.
// Only the flags that map to config fields are here; --verbose/--quiet/
// --no-color/--output are CLI state, not config, and are owned by the
// cli package.
type Flags struct {
	// ConfigPath is the --config flag value (path to a YAML file).
	ConfigPath string
	// Profile is the --profile flag value (client | self-hosted).
	Profile string
	// Endpoint is the --server flag value (remote endpoint URL for
	// the client profile).
	Endpoint string
}

// Load resolves and validates the configuration.  Returns a *Config or
// an *Error (never a generic error — every failure carries a stable
// E_* code per SPEC-03 §7).
//
// Load order (corrected per owner M2 review Finding 1):
//
//  1. Resolve config path (--config flag > GSEER_CONFIG env > default).
//  2. Read file; loose-decode into map[string]any.
//  3. scanForPolicyActive — reject any policy.active.* (AC-7.x).
//  4. scanForUnsafeKeys — reject InsecureSkipVerify etc.
//  5. strictDecode into Config (KnownFields(true) — unknown keys → error).
//  6. applyEnv — environment-variable overlay (profile may change).
//  7. applyFlags — CLI flag overrides (profile finalized here).
//  8. applyDefaults — fill in PD-1…PD-9 and v0.0.1 defaults, based on
//     the FINAL resolved profile (so a CLI --profile=client after a
//     YAML self-hosted file does NOT inherit self-hosted DB defaults).
//  9. hashAndClearToken — SHA-256 the token, clear the plaintext.
//
// 10. Validate — profile + value validation.
//
// The final precedence: CLI Flags > Environment > YAML > Defaults.
func Load(flags Flags) (*Config, *Error) {
	path, cerr := resolveConfigPath(flags)
	if cerr != nil {
		return nil, cerr
	}

	c := &Config{}

	if path != "" {
		data, rerr := readConfigFile(path)
		if rerr != nil {
			if e, ok := rerr.(*Error); ok {
				return nil, e
			}
			return nil, newError(CodeConfigParse, fmt.Sprintf("cannot read %q", path)).
				withWhy(rerr.Error()).
				withNext("run: gseer doctor")
		}
		c.Source = path

		// Loose decode for the policy.active + unsafe-key scan.
		var raw map[string]any
		if yamlErr := yaml.Unmarshal(data, &raw); yamlErr != nil {
			return nil, errParse(path, yamlErr)
		}
		if e := scanForPolicyActive(raw); e != nil {
			return nil, e
		}
		if e := scanForUnsafeKeys(raw); e != nil {
			return nil, e
		}

		// Strict decode into the typed Config.
		if err := strictDecode(data, c); err != nil {
			if typeErr, ok := err.(*yaml.TypeError); ok {
				fields := typeErr.Errors
				if len(fields) > 0 {
					return nil, errUnknownKey(extractFieldName(fields[0]))
				}
			}
			return nil, errParse(path, err)
		}
	} else {
		c.Source = "env-only"
	}

	// Order matters: env + flags FIRST, so the profile is finalized
	// before applyDefaults fills in profile-dependent defaults (database
	// components for self-hosted; none for client).
	applyEnv(c)
	applyFlags(c, flags)
	applyDefaults(c)
	hashAndClearToken(&c.Server)

	if e := Validate(c); e != nil {
		return nil, e
	}
	return c, nil
}

// applyFlags applies CLI flag overrides (highest precedence).
func applyFlags(c *Config, flags Flags) {
	if flags.Profile != "" {
		c.Profile = Profile(strings.TrimSpace(flags.Profile))
	}
	if flags.Endpoint != "" {
		c.Client.Endpoint = flags.Endpoint
	}
}

// resolveConfigPath determines the config file path in precedence order:
// --config flag > GSEER_CONFIG env > platform default (~/.gseer/config.yaml
// or %APPDATA%\gseer\config.yaml on Windows).  Returns "" if no path
// resolves and no file exists (env-only mode is valid for client profile
// with GSEER_CLIENT_ENDPOINT set).
func resolveConfigPath(flags Flags) (string, *Error) {
	if flags.ConfigPath != "" {
		if _, err := os.Stat(flags.ConfigPath); err != nil {
			return "", errFileNotFound(flags.ConfigPath)
		}
		return flags.ConfigPath, nil
	}
	if envPath, ok := os.LookupEnv("GSEER_CONFIG"); ok && envPath != "" {
		if _, err := os.Stat(envPath); err != nil {
			return "", errFileNotFound(envPath)
		}
		return envPath, nil
	}
	// Platform default: only return if the file actually exists.
	def := defaultConfigPath()
	if def != "" {
		if _, err := os.Stat(def); err == nil {
			return def, nil
		}
	}
	return "", nil // env-only mode
}

// defaultConfigPath returns the platform-default config file location.
// ~/.gseer/config.yaml on Unix; %APPDATA%\gseer\config.yaml on Windows.
func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".gseer", "config.yaml")
}

// extractFieldName parses the yaml.v3 TypeError message to extract the
// offending key name.  yaml.v3 formats unknown-field errors as:
//
//	`line N: field X not found in type Y`
//
// We want just `X` (the key name) for the error message.
func extractFieldName(typeErrMsg string) string {
	// Find "field " prefix and " not found" suffix.
	const prefix = "field "
	const suffix = " not found"
	start := strings.Index(typeErrMsg, prefix)
	if start < 0 {
		return typeErrMsg
	}
	start += len(prefix)
	end := strings.Index(typeErrMsg[start:], suffix)
	if end < 0 {
		return typeErrMsg[start:]
	}
	return typeErrMsg[start : start+end]
}
