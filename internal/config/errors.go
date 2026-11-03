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
	"strings"
)

// Error is the structured config-layer error.  Every error carries a
// stable Code (E_* per SPEC-03 §7) and a human-readable message that
// follows the style guide: what failed → likely why → exact next command.
//
// The Code is stable across releases so that scripts and CI can match
// on it; the Message may evolve.  Errors are never printed with stack
// traces unless --verbose is set (AC-5.9).
type Error struct {
	// Code is the stable E_* identifier (e.g. E_CONFIG_UNKNOWN_KEY).
	Code string
	// Field is the dotted config path of the offending key, when
	// applicable (e.g. "policy.active.enabled", "server.listen").
	Field string
	// Detail is the short "what failed" sentence.
	Detail string
	// Why is the "likely why" sentence (optional).
	Why string
	// Next is the "exact next command" or fix instruction.
	Next string
}

// Error renders the error per the SPEC-03 §7 style guide:
//
//	E_CONFIG_UNKNOWN_KEY: unknown key "policy.active.enabled" in config.
//	Why: active collection is not part of any release until its gates close.
//	Try: remove the key; see docs/ROADMAP.md for v0.0.2 planning.
func (e *Error) Error() string {
	var b strings.Builder
	if e.Code != "" {
		fmt.Fprintf(&b, "%s: ", e.Code)
	}
	b.WriteString(e.Detail)
	if e.Field != "" {
		fmt.Fprintf(&b, " (field: %s)", e.Field)
	}
	if e.Why != "" {
		fmt.Fprintf(&b, "\nWhy: %s", e.Why)
	}
	if e.Next != "" {
		fmt.Fprintf(&b, "\nTry: %s", e.Next)
	}
	return b.String()
}

// Stable E_* codes for the config layer (SPEC-03 §7; M2 adds the
// E_CONFIG_* family).  Codes are lowercase-with-underscores; the E_
// prefix is reserved for all user-facing error codes across GSEER.
const (
	// E_CONFIG_PARSE — the YAML file could not be parsed.
	CodeConfigParse = "E_CONFIG_PARSE"

	// E_CONFIG_UNKNOWN_KEY — strict schema: an unknown top-level or
	// nested key appeared in the YAML file.
	CodeConfigUnknownKey = "E_CONFIG_UNKNOWN_KEY"

	// E_CONFIG_POLICY_ACTIVE_REJECTED — any `policy.active.*` key.
	// Active collection is not part of any release until the P2/PAC gates
	// is normative, not a missing feature.
	CodeConfigPolicyActiveRejected = "E_CONFIG_POLICY_ACTIVE_REJECTED"

	// E_CONFIG_INSECURE — an unsafe key was detected (e.g.
	// InsecureSkipVerify on a control-plane endpoint).  Security
	// close; the rejection is permanent by design (AC-7.x).
	CodeConfigInsecure = "E_CONFIG_INSECURE"

	// E_CONFIG_INVALID_VALUE — a known key has an invalid value (e.g.
	// an unknown log level, a malformed DSN, an invalid listen address).
	CodeConfigInvalidValue = "E_CONFIG_INVALID_VALUE"

	// E_CONFIG_PROFILE_CONFLICT — the config mixes keys that don't
	// belong to the chosen profile (e.g. database.* in a client profile,
	// or client.endpoint in a self-hosted profile without explanation).
	CodeConfigProfileConflict = "E_CONFIG_PROFILE_CONFLICT"

	// E_CONFIG_REQUIRED — a required key for the chosen profile is
	// missing (e.g. no database DSN in a self-hosted profile).
	CodeConfigRequired = "E_CONFIG_REQUIRED"

	// E_CONFIG_FILE_NOT_FOUND — the --config path or default config
	// file does not exist.
	CodeConfigFileNotFound = "E_CONFIG_FILE_NOT_FOUND"

	// E_CLIENT_ONLY — a server command was invoked under a client
	// profile (SPEC-03 §5, §7).  Raised by the CLI layer when the
	// profile is consulted; the code is defined here so the config
	// layer can reference it in profile-validation hints.
	CodeClientOnly = "E_CLIENT_ONLY"
)

// newError constructs an *Error with the given code and detail.  Optional
// fields (Field, Why, Next) are set via the with* helpers below.
func newError(code, detail string) *Error {
	return &Error{Code: code, Detail: detail}
}

// withField sets the offending field path.  Returns the error for chaining.
func (e *Error) withField(field string) *Error {
	e.Field = field
	return e
}

// withWhy sets the "likely why" sentence.
func (e *Error) withWhy(why string) *Error {
	e.Why = why
	return e
}

// withNext sets the "exact next command" or fix instruction.
func (e *Error) withNext(next string) *Error {
	e.Next = next
	return e
}

// Pre-built error constructors (each returns a fully-styled *Error).

func errParse(path string, cause error) *Error {
	return newError(CodeConfigParse, fmt.Sprintf("cannot parse config file %q", path)).
		withWhy(cause.Error()).
		withNext("fix the YAML syntax and retry: gseer doctor")
}

func errUnknownKey(path string) *Error {
	return newError(CodeConfigUnknownKey, fmt.Sprintf("unknown key %q in config", path)).
		withWhy("strict schema: only documented keys are accepted so that typos and future-version keys surface immediately.").
		withNext("remove the key or consult docs/QUICKSTART.md for the supported schema")
}

func errPolicyActiveRejected(key string) *Error {
	return newError(CodeConfigPolicyActiveRejected, fmt.Sprintf("key %q is rejected", key)).
		withWhy("active collection (P2/PAC) is not part of any release until its gates close; PD-2..PD-7 are approved as engineering defaults only, not authorization to scan.").
		withNext("remove the key; see docs/ROADMAP.md for v0.0.2 planning")
}

func errInsecure(key string) *Error {
	return newError(CodeConfigInsecure, fmt.Sprintf("unsafe key %q is rejected", key)).
		withWhy("security defaults (InsecureSkipVerify on control-plane endpoints, plaintext token storage, etc.) are not negotiable in any release.").
		withNext("remove the key and configure TLS properly; see docs/INSTALL.md")
}

func errInvalidValue(field, value, hint string) *Error {
	return newError(CodeConfigInvalidValue, fmt.Sprintf("invalid value %q for %q", value, field)).
		withWhy(hint).
		withNext("set a valid value and retry: gseer doctor")
}

func errProfileConflict(field, profile string) *Error {
	return newError(CodeConfigProfileConflict, fmt.Sprintf("field %q does not apply to profile %q", field, profile)).
		withWhy("each profile has a specific schema; mixing keys produces undefined behavior and is rejected.").
		withNext(fmt.Sprintf("remove the field or switch profile: gseer setup --%s", profile))
}

func errRequired(field, profile string) *Error {
	return newError(CodeConfigRequired, fmt.Sprintf("required field %q is missing for profile %q", field, profile)).
		withWhy("the profile cannot operate without this field.").
		withNext(fmt.Sprintf("run: gseer setup --%s", profile))
}

func errFileNotFound(path string) *Error {
	return newError(CodeConfigFileNotFound, fmt.Sprintf("config file %q not found", path)).
		withWhy("the --config path or default config location does not exist.").
		withNext("create one with: gseer setup  (or pass --config <path>)")
}
