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
	"strings"
	"testing"
)

// errors_test.go — tests for the E_* error format (SPEC-03 §7 style guide).
//
// Every error must render as:
//   E_CODE: detail (field: path)
//   Why: likely-why sentence
//   Try: exact next command

// TestErrorFormat verifies the SPEC-03 §7 style: Code + Detail + Why + Try.
func TestErrorFormat(t *testing.T) {
	e := errPolicyActiveRejected("policy.active.enabled")
	s := e.Error()

	if !strings.HasPrefix(s, "E_CONFIG_POLICY_ACTIVE_REJECTED: ") {
		t.Errorf("error must start with the code; got: %q", s)
	}
	if !strings.Contains(s, "Why: ") {
		t.Errorf("error must contain a Why: line; got: %q", s)
	}
	if !strings.Contains(s, "Try: ") {
		t.Errorf("error must contain a Try: line; got: %q", s)
	}
}

// TestErrorFieldRendering verifies the field path appears in the output.
func TestErrorFieldRendering(t *testing.T) {
	e := errUnknownKey("policy.active.enabled")
	e = e.withField("policy.active.enabled")
	s := e.Error()
	if !strings.Contains(s, "policy.active.enabled") {
		t.Errorf("error must mention the field path; got: %q", s)
	}
}

// TestErrorCodesStable verifies every E_* code is the expected stable string.
func TestErrorCodesStable(t *testing.T) {
	codes := map[string]string{
		"E_CONFIG_PARSE":                  CodeConfigParse,
		"E_CONFIG_UNKNOWN_KEY":            CodeConfigUnknownKey,
		"E_CONFIG_POLICY_ACTIVE_REJECTED": CodeConfigPolicyActiveRejected,
		"E_CONFIG_INSECURE":               CodeConfigInsecure,
		"E_CONFIG_INVALID_VALUE":          CodeConfigInvalidValue,
		"E_CONFIG_PROFILE_CONFLICT":       CodeConfigProfileConflict,
		"E_CONFIG_REQUIRED":               CodeConfigRequired,
		"E_CONFIG_FILE_NOT_FOUND":         CodeConfigFileNotFound,
		"E_CLIENT_ONLY":                   CodeClientOnly,
	}
	for want, got := range codes {
		if want != got {
			t.Errorf("code mismatch: want %q, got %q", want, got)
		}
	}
}

// TestErrorMessageSnapshots verifies the exact rendered output for each
// error constructor (AC-5.9: every documented E_* renders a suggestion
// line).  These snapshots pin the wording so that doc/drsift is caught.
func TestErrorMessageSnapshots(t *testing.T) {
	cases := []struct {
		name string
		err  *Error
		// wants: substrings that MUST appear in the rendered error.
		wants []string
	}{
		{
			name:  "policy.active rejected",
			err:   errPolicyActiveRejected("policy.active.enabled"),
			wants: []string{"E_CONFIG_POLICY_ACTIVE_REJECTED", "v0.0.2", "Why:", "Try:"},
		},
		{
			name:  "unknown key",
			err:   errUnknownKey("bogus_key"),
			wants: []string{"E_CONFIG_UNKNOWN_KEY", "bogus_key", "Why:", "Try:"},
		},
		{
			name:  "insecure",
			err:   errInsecure("server.insecure_skip_verify"),
			wants: []string{"E_CONFIG_INSECURE", "Why:", "Try:"},
		},
		{
			name:  "invalid value",
			err:   errInvalidValue("log.level", "trace", "must be debug/info/warn/error"),
			wants: []string{"E_CONFIG_INVALID_VALUE", "log.level", "trace", "Why:", "Try:"},
		},
		{
			name:  "profile conflict",
			err:   errProfileConflict("database.*", "client"),
			wants: []string{"E_CONFIG_PROFILE_CONFLICT", "database.*", "client", "Why:", "Try:"},
		},
		{
			name:  "required",
			err:   errRequired("database.dsn", "self-hosted"),
			wants: []string{"E_CONFIG_REQUIRED", "database.dsn", "self-hosted", "Why:", "Try:"},
		},
		{
			name:  "file not found",
			err:   errFileNotFound("/tmp/missing.yaml"),
			wants: []string{"E_CONFIG_FILE_NOT_FOUND", "/tmp/missing.yaml", "Why:", "Try:"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.err.Error()
			for _, w := range tc.wants {
				if !strings.Contains(s, w) {
					t.Errorf("missing %q in:\n%s", w, s)
				}
			}
		})
	}
}

// TestErrorWithChainedBuilders verifies the with* helpers return the
// same *Error (fluent chaining).
func TestErrorWithChainedBuilders(t *testing.T) {
	e := newError(CodeConfigParse, "test")
	e2 := e.withField("f").withWhy("w").withNext("n")
	if e != e2 {
		t.Error("with* helpers must return the same *Error for chaining")
	}
	if e.Field != "f" || e.Why != "w" || e.Next != "n" {
		t.Errorf("chained fields: got Field=%q Why=%q Next=%q", e.Field, e.Why, e.Next)
	}
}
