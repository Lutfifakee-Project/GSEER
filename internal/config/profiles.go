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
)

// profiles.go — profile validation per SPEC-03 §5.
//
// Each profile has a specific schema.  Mixing keys produces undefined
// behavior and is rejected (E_CONFIG_PROFILE_CONFLICT).  Missing
// required keys is rejected (E_CONFIG_REQUIRED).

// ValidateProfile checks that the config's fields are consistent with
// its declared profile.  Returns nil if consistent, or an *Error
// describing the first violation.
func ValidateProfile(c *Config) *Error {
	switch c.Profile {
	case ProfileClient:
		return validateClient(c)
	case ProfileSelfHosted:
		return validateSelfHosted(c)
	case ProfilePersonal:
		return validatePersonal(c)
	default:
		return errInvalidValue("profile", string(c.Profile),
			"profile must be 'client', 'self-hosted' or 'personal'").
			withNext("run: gseer setup  (to choose a profile)")
	}
}

// validateClient enforces the client-profile schema:
//   - client.endpoint is required (the remote server to query).
//   - database.* is rejected (no PostgreSQL in client profile).
//   - server.listen/token are rejected (no local server in client profile).
//   - sources may be present (config-only; never collected).
func validateClient(c *Config) *Error {
	if c.Client.Endpoint == "" {
		return errRequired("client.endpoint", string(ProfileClient)).
			withNext("run: gseer setup  (to configure an endpoint)  or: gseer setup --self-hosted")
	}
	if c.Database.DSN != "" || c.Database.Host != "" || c.Database.Password != "" {
		return errProfileConflict("database.*", string(ProfileClient)).
			withWhy("the client profile queries a remote endpoint; PostgreSQL is not used.").
			withNext("switch profile: gseer setup --self-hosted  or remove the database.* keys")
	}
	if c.Server.Listen != "" || c.Server.Token != "" || c.Server.TokenHash != "" {
		return errProfileConflict("server.*", string(ProfileClient)).
			withWhy("the client profile does not run a local server; `gseer server …` errors with E_CLIENT_ONLY.").
			withNext("switch profile: gseer setup --self-hosted  or remove the server.* keys")
	}
	return nil
}

// validateSelfHosted enforces the self-hosted profile schema:
//   - database.* is required (DSN or components).
//   - server.listen defaults to loopback (DefaultListen) if unset.
//   - server.token is strongly recommended; warn (not error) if unset.
//   - client.endpoint is rejected (no remote endpoint in self-hosted).
func validateSelfHosted(c *Config) *Error {
	if c.Database.DSN == "" && c.Database.Host == "" {
		return errRequired("database.dsn (or database.host+port+user+password+name)", string(ProfileSelfHosted)).
			withWhy("the self-hosted profile requires a PostgreSQL connection.").
			withNext("run: gseer setup --self-hosted --with-compose  (to provision one)")
	}
	if c.Client.Endpoint != "" {
		return errProfileConflict("client.endpoint", string(ProfileSelfHosted)).
			withWhy("the self-hosted profile queries the local server, not a remote endpoint.").
			withNext("remove client.endpoint  or switch profile: gseer setup  (client)")
	}
	// server.listen defaults to loopback; non-loopback is allowed but
	// requires auth (server.token).  We don't error on empty listen —
	// the default is applied in applyDefaults.
	if c.Server.Listen != "" && !isLoopback(c.Server.Listen) && c.Server.Token == "" {
		return errInvalidValue("server.listen", c.Server.Listen,
			"non-loopback binding requires an API token (server.token) for auth.").
			withNext("set server.token  or bind loopback: 127.0.0.1:8443")
	}
	return nil
}

// isLoopback reports whether the listen address binds to a loopback
// interface (127.0.0.0/8 or [::1]).  Non-loopback bindings require auth.
func isLoopback(listen string) bool {
	host := listen
	if idx := strings.LastIndex(listen, ":"); idx >= 0 {
		host = listen[:idx]
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "localhost", "127.0.0.1", "::1", "":
		return true
	}
	return strings.HasPrefix(host, "127.")
}

// validatePersonal enforces the personal-profile schema:
//   - database.dsn is REQUIRED and must be the database FILE path
//     (postgres:// URLs are rejected — wrong engine, wrong profile);
//   - client.endpoint is rejected (no remote endpoint in personal mode);
//   - server.* is allowed (a personal server can serve the local API);
//   - sources may be present (config-only; the collect runner gates on
//     enabled=false by default).
func validatePersonal(c *Config) *Error {
	if c.Database.DSN == "" {
		return errRequired("database.dsn", string(ProfilePersonal)).
			withNext("run: gseer setup --personal  (provisions the database file and writes the config)")
	}
	if strings.HasPrefix(strings.TrimSpace(c.Database.DSN), "postgres://") {
		return errInvalidValue("database.dsn", "[redacted]",
			"the personal profile stores the SQLite database FILE path in database.dsn, not a postgres:// URL").
			withNext("run: gseer setup --personal  (writes the correct config), or switch: gseer setup --self-hosted")
	}
	if c.Client.Endpoint != "" {
		return errProfileConflict("client.endpoint", string(ProfilePersonal)).
			withWhy("the personal profile runs the engine locally; there is no remote endpoint.").
			withNext("remove client.* keys, or switch profile: gseer setup --server <url> --profile client")
	}
	if c.Database.RuntimeDSN != "" {
		return errProfileConflict("database.runtime_dsn", string(ProfilePersonal)).
			withWhy("personal mode uses ONE local credential — the database file's filesystem permissions (0600).").
			withNext("remove database.runtime_dsn")
	}
	return nil
}
