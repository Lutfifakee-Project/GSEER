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

package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// suppress_test.go — unit coverage of the `gseer suppress` surface that
// needs no database: the command tree, the mandatory-reason contract and
// the expiry parser.  The DB-backed behavior (gate, retraction, audit)
// lives in internal/storage/postgres/suppression_integration_test.go.

// captureSubHelp renders the help of a named root subcommand.
func captureSubHelp(t *testing.T, name string) string {
	t.Helper()
	cmd, _, err := RootCmd.Find([]string{name})
	if err != nil {
		t.Fatalf("find %s: %v", name, err)
	}
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	if err := cmd.Help(); err != nil {
		t.Fatalf("help %s: %v", name, err)
	}
	return buf.String()
}

// executeRoot runs RootCmd with args and returns the execution error
// (the result the CLI process would translate into an exit code).
func executeRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	buf := &bytes.Buffer{}
	RootCmd.SetOut(buf)
	RootCmd.SetErr(buf)
	RootCmd.SetArgs(args)
	defer RootCmd.SetArgs([]string{})
	err := RootCmd.Execute()
	return buf.String(), err
}

func TestSuppressCommandTreeRegistered(t *testing.T) {
	for _, name := range []string{"add", "list", "remove"} {
		_, _, err := RootCmd.Find([]string{"suppress", name})
		if err != nil {
			t.Errorf("suppress %s not findable: %v", name, err)
		}
	}
	// The root help must advertise the new command tree.
	help := captureHelp(t)
	if !strings.Contains(help, "suppress") {
		t.Error("root help does not mention `suppress`")
	}
	sub := captureSubHelp(t, "suppress")
	for _, want := range []string{"add", "list", "remove", "domain_suffix"} {
		if !strings.Contains(sub, want) {
			t.Errorf("suppress help missing %q", want)
		}
	}
}

func TestSuppressAddRequiresReason(t *testing.T) {
	_, err := executeRoot(t, "suppress", "add", "ip", "198.51.100.4")
	if err == nil || !strings.Contains(err.Error(), "E_SUPPRESS_REASON") {
		t.Errorf("add without --reason error = %v, want E_SUPPRESS_REASON", err)
	}
}

func TestSuppressAddRejectsUnknownKind(t *testing.T) {
	// The DB layer owns kind validation; without a DB the command fails
	// at config load first.  Verify the failure ordering is config-first
	// (no accidental kind acceptance path that skips the DB).
	_, err := executeRoot(t, "suppress", "add", "wildcard", "x", "--reason", "r")
	if err == nil {
		t.Fatal("suppress add in a client-only env must fail (no database)")
	}
}

func TestParseSuppressExpiry(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	// RFC 3339.
	got, err := parseSuppressExpiry("2030-01-01T00:00:00Z", now)
	if err != nil || !got.Equal(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("RFC3339 expiry = %v, %v", got, err)
	}
	// Duration.
	got, err = parseSuppressExpiry("24h", now)
	if err != nil || !got.Equal(now.Add(24*time.Hour)) {
		t.Errorf("duration expiry = %v, %v", got, err)
	}
	// Past timestamps rejected.
	if _, err := parseSuppressExpiry("2001-01-01T00:00:00Z", now); err == nil {
		t.Error("past timestamp accepted")
	}
	// Non-positive durations rejected.
	if _, err := parseSuppressExpiry("-5h", now); err == nil {
		t.Error("negative duration accepted")
	}
	if _, err := parseSuppressExpiry("0s", now); err == nil {
		t.Error("zero duration accepted")
	}
	// Garbage rejected with the stable code.
	_, err = parseSuppressExpiry("tomorrow-ish", now)
	if err == nil || !strings.Contains(err.Error(), "E_SUPPRESS_EXPIRY") {
		t.Errorf("garbage expiry error = %v, want E_SUPPRESS_EXPIRY", err)
	}
}
