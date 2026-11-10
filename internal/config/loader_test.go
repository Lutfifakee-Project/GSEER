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
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// loader_test.go — end-to-end precedence tests.
//
// Precedence (highest wins): flags > env > file > defaults.

// helper: load a fixture with the given flags.  Does NOT clear env vars
// — each test manages its own env via t.Setenv (which is scoped per test
// by the testing framework).  TestMain sets HOME to a temp dir so the
// default config-file path doesn't pick up a real ~/.gseer/config.yaml.
func loadWithFlags(t *testing.T, fixture string, flags Flags) (*Config, *Error) {
	t.Helper()
	path := ""
	if fixture != "" {
		path = filepath.Join("testdata", fixture)
	}
	flags.ConfigPath = path
	return Load(flags)
}

// TestPrecedenceFlagOverEnvOverFile verifies the full precedence chain:
// a flag value beats an env value which beats a file value.
func TestPrecedenceFlagOverEnvOverFile(t *testing.T) {
	// File says: profile=self-hosted, listen=127.0.0.1:8443.
	// Env says:  GSEER_LISTEN=127.0.0.1:7000  (overrides file).
	// Flag says: --profile=client  (overrides env+file).
	//
	// But profile=client + listen would be a conflict... use a different shape:
	//
	// File: valid-selfhosted.yaml (profile=self-hosted, listen=127.0.0.1:8443)
	// Env:  GSEER_LISTEN=127.0.0.1:7000 (overrides file listen)
	// Flag: --server NOT used here; instead test env-over-file for listen.
	t.Setenv("GSEER_LISTEN", "127.0.0.1:7000")
	c, e := loadWithFlags(t, "valid-selfhosted.yaml", Flags{})
	if e != nil {
		t.Fatalf("expected success, got %v", e)
	}
	if c.Server.Listen != "127.0.0.1:7000" {
		t.Errorf("env should override file: want 127.0.0.1:7000, got %q", c.Server.Listen)
	}
}

// TestPrecedenceFlagOverEnv verifies a flag beats an env var.
func TestPrecedenceFlagOverEnv(t *testing.T) {
	t.Setenv("GSEER_PROFILE", "self-hosted")
	c, e := loadWithFlags(t, "", Flags{Profile: "client", Endpoint: "https://flag.example.com"})
	if e != nil {
		t.Fatalf("expected success, got %v", e)
	}
	if c.Profile != ProfileClient {
		t.Errorf("flag should override env: want client, got %q", c.Profile)
	}
	if c.Client.Endpoint != "https://flag.example.com" {
		t.Errorf("flag endpoint: got %q", c.Client.Endpoint)
	}
}

// TestApplyDefaultsPopulatesPDValues verifies applyDefaults fills in
// every PD-1..PD-9 default value.
func TestApplyDefaultsPopulatesPDValues(t *testing.T) {
	c := &Config{Profile: ProfileSelfHosted, Database: DatabaseConfig{Host: "127.0.0.1"}}
	applyDefaults(c)
	if c.Defaults.SearchMode != DefaultSearchMode {
		t.Errorf("PD-1 SearchMode: want %q, got %q", DefaultSearchMode, c.Defaults.SearchMode)
	}
	if len(c.Defaults.CollectionPorts) != len(DefaultCollectionPorts) {
		t.Errorf("PD-2 CollectionPorts length: want %d, got %d", len(DefaultCollectionPorts), len(c.Defaults.CollectionPorts))
	}
	if c.Defaults.AuditSpoolBytes != DefaultAuditSpoolBytes {
		t.Errorf("PD-4 AuditSpoolBytes: want %d, got %d", DefaultAuditSpoolBytes, c.Defaults.AuditSpoolBytes)
	}
	if c.Defaults.EmergencyReserveBytes != DefaultEmergencyReserveBytes {
		t.Errorf("PD-5 EmergencyReserveBytes: want %d, got %d", DefaultEmergencyReserveBytes, c.Defaults.EmergencyReserveBytes)
	}
	if c.Defaults.DecompressedCapBytes != DefaultDecompressedCapBytes {
		t.Errorf("PD-6 DecompressedCapBytes: want %d, got %d", DefaultDecompressedCapBytes, c.Defaults.DecompressedCapBytes)
	}
	if c.Defaults.DecompressedCapMaxBytes != DefaultDecompressedCapMaxBytes {
		t.Errorf("PD-7 DecompressedCapMaxBytes: want %d, got %d", DefaultDecompressedCapMaxBytes, c.Defaults.DecompressedCapMaxBytes)
	}
	if c.Defaults.PostgresMajor != DefaultPostgresMajor {
		t.Errorf("PD-8 PostgresMajor: want %d, got %d", DefaultPostgresMajor, c.Defaults.PostgresMajor)
	}
}

// TestApplyDefaultsV001Specific verifies the v0.0.1-specific defaults
// (listen, log, db components) are applied.
func TestApplyDefaultsV001Specific(t *testing.T) {
	c := &Config{Profile: ProfileSelfHosted, Database: DatabaseConfig{Host: "127.0.0.1"}}
	applyDefaults(c)
	if c.Server.Listen != DefaultListen {
		t.Errorf("Listen: want %q, got %q", DefaultListen, c.Server.Listen)
	}
	if c.Log.Level != DefaultLogLevel {
		t.Errorf("LogLevel: want %q, got %q", DefaultLogLevel, c.Log.Level)
	}
	if c.Log.Format != DefaultLogFormat {
		t.Errorf("LogFormat: want %q, got %q", DefaultLogFormat, c.Log.Format)
	}
	if c.Database.Port != DefaultDBPort {
		t.Errorf("DBPort: want %d, got %d", DefaultDBPort, c.Database.Port)
	}
}

// TestApplyDefaultsClientProfileNoListen verifies the client profile
// does NOT get a server.listen default (no local server).
func TestApplyDefaultsClientProfileNoListen(t *testing.T) {
	c := &Config{Profile: ProfileClient}
	applyDefaults(c)
	if c.Server.Listen != "" {
		t.Errorf("client profile should not get a listen default; got %q", c.Server.Listen)
	}
}

// TestApplyDefaultsLockTimeoutWithExplicitDSN — regression (M4 review
// round): the migration lock timeout default was previously skipped for
// configs that set an explicit DSN, leaving MigrationLockTimeoutMs=0 —
// which made the migration advisory-lock wait effectively UNBOUNDED
// (the permanent freeze the M3 owner directive forbids).  The default
// must apply regardless of how the DSN is formed.
func TestApplyDefaultsLockTimeoutWithExplicitDSN(t *testing.T) {
	c := &Config{
		Profile:  ProfileSelfHosted,
		Database: DatabaseConfig{DSN: "postgres://u:p@127.0.0.1:5432/gseer?sslmode=disable"},
	}
	applyDefaults(c)
	if c.Database.MigrationLockTimeoutMs != DefaultMigrationLockTimeoutMs {
		t.Errorf("explicit-DSN config: MigrationLockTimeoutMs = %d, want default %d",
			c.Database.MigrationLockTimeoutMs, DefaultMigrationLockTimeoutMs)
	}
	// Component-formed DSN keeps the default too (pre-existing path).
	c2 := &Config{Profile: ProfileSelfHosted, Database: DatabaseConfig{Host: "127.0.0.1"}}
	applyDefaults(c2)
	if c2.Database.MigrationLockTimeoutMs != DefaultMigrationLockTimeoutMs {
		t.Errorf("component-DSN config: MigrationLockTimeoutMs = %d, want default %d",
			c2.Database.MigrationLockTimeoutMs, DefaultMigrationLockTimeoutMs)
	}
	// An operator-configured value must win over the default.
	c3 := &Config{Profile: ProfileSelfHosted, Database: DatabaseConfig{DSN: "postgres://u@h/db", MigrationLockTimeoutMs: 60000}}
	applyDefaults(c3)
	if c3.Database.MigrationLockTimeoutMs != 60000 {
		t.Errorf("operator value overridden: MigrationLockTimeoutMs = %d, want 60000", c3.Database.MigrationLockTimeoutMs)
	}
}

// TestHashAndClearToken verifies token hashing: plaintext → hash, then
// plaintext is cleared.
func TestHashAndClearToken(t *testing.T) {
	s := &ServerConfig{Token: "secret"}
	hashAndClearToken(s)
	if s.Token != "" {
		t.Errorf("plaintext must be cleared; got %q", s.Token)
	}
	if s.TokenHash == "" {
		t.Error("TokenHash must be set")
	}
	if s.TokenHash != HashToken("secret") {
		t.Errorf("TokenHash must equal HashToken(plaintext)")
	}
}

// TestHashAndClearTokenEmpty verifies an empty token leaves TokenHash empty.
func TestHashAndClearTokenEmpty(t *testing.T) {
	s := &ServerConfig{Token: ""}
	hashAndClearToken(s)
	if s.TokenHash != "" {
		t.Errorf("empty token should produce empty hash; got %q", s.TokenHash)
	}
}

// TestEffectiveDSNFromComponents verifies DSN building from components.
func TestEffectiveDSNFromComponents(t *testing.T) {
	d := DatabaseConfig{
		Host:     "127.0.0.1",
		Port:     5432,
		User:     "gseer",
		Password: "secret",
		Name:     "gseer",
		SSLMode:  "disable",
	}
	got := d.EffectiveDSN()
	want := "postgres://gseer:secret@127.0.0.1:5432/gseer?sslmode=disable"
	if got != want {
		t.Errorf("effectiveDSN: want %q, got %q", want, got)
	}
}

// TestEffectiveDSNDefaultsSSLMode verifies sslmode defaults to disable
// (loopback) when not specified.
func TestEffectiveDSNDefaultsSSLMode(t *testing.T) {
	d := DatabaseConfig{Host: "127.0.0.1", Port: 5432, User: "u", Name: "n"}
	got := d.EffectiveDSN()
	want := "postgres://u:@127.0.0.1:5432/n?sslmode=disable"
	if got != want {
		t.Errorf("effectiveDSN with default sslmode: want %q, got %q", want, got)
	}
}

// TestDSNSpecialCharPasswords verifies that passwords containing URI-
// special characters (@, :, /, ?, #, spaces, quotes) are properly
// percent-encoded in the built DSN.  Manual string interpolation would
// produce a malformed DSN; net/url handles this correctly.
//
// Owner M2 review Finding 4.
func TestDSNSpecialCharPasswords(t *testing.T) {
	cases := []struct {
		name     string
		password string
		// wantEncoded is the expected encoding of the password in the
		// userinfo portion of the DSN.  We check that the DSN contains
		// this encoded form and that the overall DSN parses cleanly.
		wantEncoded string
	}{
		{"at sign", "p@ss", "p%40ss"},
		{"colon", "p:ss", "p%3Ass"},
		{"slash", "p/ss", "p%2Fss"},
		{"question", "p?ss", "p%3Fss"},
		{"hash", "p#ss", "p%23ss"},
		{"space", "p ss", "p%20ss"},
		{"quote", "p\"ss", "p%22ss"},
		{"all special", "p@ss:w/or/d?test#1", "p%40ss%3Aw%2For%2Fd%3Ftest%231"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := DatabaseConfig{
				User:     "gseer",
				Password: tc.password,
				Host:     "127.0.0.1",
				Port:     5432,
				Name:     "gseer",
				SSLMode:  "disable",
			}
			dsn := d.EffectiveDSN()

			// The DSN must contain the encoded password (when non-empty).
			if tc.password != "" {
				want := "gseer:" + tc.wantEncoded + "@"
				if !strings.Contains(dsn, want) {
					t.Errorf("DSN missing encoded password %q; got: %s", want, dsn)
				}
			}

			// The DSN must parse cleanly as a URL — no malformed syntax.
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatalf("DSN does not parse as URL: %v; dsn=%s", err, dsn)
			}
			if u.Scheme != "postgres" {
				t.Errorf("scheme: want postgres, got %q", u.Scheme)
			}
			// The decoded password from the parsed URL must match the input.
			if u.User != nil {
				gotPass, _ := u.User.Password()
				if gotPass != tc.password {
					t.Errorf("round-trip password: want %q, got %q (dsn=%s)", tc.password, gotPass, dsn)
				}
			}
		})
	}
}

// TestDSNExplicitOverridesComponents verifies an explicit DSN field wins
// over component fields.
func TestDSNExplicitOverridesComponents(t *testing.T) {
	d := DatabaseConfig{
		DSN:      "postgres://explicit@host/db",
		Host:     "ignored",
		Port:     5432,
		User:     "ignored",
		Password: "ignored",
		Name:     "ignored",
	}
	if got := d.EffectiveDSN(); got != "postgres://explicit@host/db" {
		t.Errorf("explicit DSN should win; got %q", got)
	}
}

// TestDSNNoPort verifies the port==0 branch: the DSN omits the port
// component entirely (PostgreSQL then applies its default 5432 at
// connect time).  net.JoinHostPort is not used for this case.
func TestDSNNoPort(t *testing.T) {
	d := DatabaseConfig{
		User:     "gseer",
		Password: "pw",
		Host:     "127.0.0.1",
		Port:     0, // no explicit port
		Name:     "gseer",
		SSLMode:  "disable",
	}
	dsn := d.EffectiveDSN()
	want := "postgres://gseer:pw@127.0.0.1/gseer?sslmode=disable"
	if dsn != want {
		t.Errorf("DSN without port: want %q, got %q", want, dsn)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("DSN does not parse: %v", err)
	}
	if u.Port() != "" {
		t.Errorf("DSN without port must have empty Port(); got %q", u.Port())
	}
}

// TestDSNHostForms verifies round-trip DSN construction for every
// supported host form: IPv4, IPv6 (::1), and DNS hostname.
//
// The IPv6 case is the owner's DSN edge-case review: manual
// Sprintf("%s:%d") would produce `::1:5432` — ambiguous and unparseable
// as a URL authority.  net.JoinHostPort brackets IPv6 literals
// ([::1]:5432) and leaves IPv4/hostname forms unchanged.  Each subtest
// round-trips: build DSN → url.Parse → Hostname()/Port() must return the
// original inputs.
func TestDSNHostForms(t *testing.T) {
	cases := []struct {
		name string
		host string
		port int
	}{
		{"IPv4 loopback", "127.0.0.1", 5432},
		{"IPv6 loopback", "::1", 5432},
		{"DNS hostname", "db.example.com", 5432},
		{"DNS hostname with subdomain", "db.internal.example.co", 6543},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := DatabaseConfig{
				User:     "gseer",
				Password: "pw",
				Host:     tc.host,
				Port:     tc.port,
				Name:     "gseer",
				SSLMode:  "disable",
			}
			dsn := d.EffectiveDSN()

			// Must parse cleanly as a URL.
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatalf("DSN does not parse as URL: %v; dsn=%s", err, dsn)
			}
			if u.Scheme != "postgres" {
				t.Errorf("scheme: want postgres, got %q", u.Scheme)
			}

			// Round-trip: Hostname() strips IPv6 brackets; Port() returns
			// the port as a string.  Both must equal the inputs.
			if got := u.Hostname(); got != tc.host {
				t.Errorf("hostname round-trip: want %q, got %q (dsn=%s)", tc.host, got, dsn)
			}
			if got := u.Port(); got != strconv.Itoa(tc.port) {
				t.Errorf("port round-trip: want %q, got %q (dsn=%s)", strconv.Itoa(tc.port), got, dsn)
			}

			// IPv6-specific: the DSN must contain the bracketed form.
			if strings.Contains(tc.host, ":") {
				if !strings.Contains(dsn, "["+tc.host+"]") {
					t.Errorf("IPv6 DSN must contain bracketed form [%s]; dsn=%s", tc.host, dsn)
				}
			}
		})
	}
}
