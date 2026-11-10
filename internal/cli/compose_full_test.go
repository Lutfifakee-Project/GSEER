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

package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// compose_full_test.go — the v0.1.0 full-stack compose template
// (directive §6): exactly db + server, healthchecks, persistent volume,
// no published DB port, token-mandatory server, and a generated .env
// holding both secrets at 0600.

func TestSetupWithComposeFull(t *testing.T) {
	resetSetupFlags(t)
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	out := &strings.Builder{}
	cmd := findSetup(t)
	cmd.SetOut(out)
	if err := cmd.Flags().Set("with-compose", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("full", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("setup --with-compose --full: %v\n%s", err, out.String())
	}

	composeBody, err := os.ReadFile(filepath.Join(dir, "docker-compose.full.yml"))
	if err != nil {
		t.Fatalf("compose file missing: %v", err)
	}
	envBody, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatalf(".env missing: %v", err)
	}

	// YAML must parse and define EXACTLY two services — no unused
	// containers (directive §6).
	var doc struct {
		Services map[string]struct {
			Image       string            `yaml:"image"`
			Build       *struct{}         `yaml:"build"`
			Ports       []string          `yaml:"ports"`
			Environment map[string]string `yaml:"environment"`
			Healthcheck *struct {
				Test []string `yaml:"test"`
			} `yaml:"healthcheck"`
			DependsOn map[string]struct {
				Condition string `yaml:"condition"`
			} `yaml:"depends_on"`
		}
		Volumes map[string]struct{} `yaml:"volumes"`
	}
	if err := yaml.Unmarshal(composeBody, &doc); err != nil {
		t.Fatalf("compose YAML does not parse: %v", err)
	}
	if len(doc.Services) != 2 {
		t.Fatalf("services = %d, want exactly db + server (no unused containers)", len(doc.Services))
	}
	if _, ok := doc.Services["db"]; !ok {
		t.Error("db service missing")
	}
	server, ok := doc.Services["server"]
	if !ok {
		t.Fatal("server service missing")
	}
	if server.Build == nil {
		t.Error("server must build from the local Dockerfile")
	}
	// The DB publishes NO host port (compose-internal only).
	if db := doc.Services["db"]; len(db.Ports) != 0 {
		t.Errorf("db publishes ports %v — must be compose-internal only", db.Ports)
	}
	// The server healthchecks via the binary's /health endpoint.
	if server.Healthcheck == nil || !strings.Contains(strings.Join(server.Healthcheck.Test, " "), "/health") {
		t.Error("server healthcheck missing or not hitting /health")
	}
	// The server waits for a HEALTHY db (readiness ordering).
	if server.DependsOn["db"].Condition != "service_healthy" {
		t.Errorf("depends_on condition = %q, want service_healthy", server.DependsOn["db"].Condition)
	}
	// The API publishes on host loopback only.
	for _, p := range server.Ports {
		if !strings.HasPrefix(p, "127.0.0.1:") {
			t.Errorf("server port binding %q is not host-loopback", p)
		}
	}
	// Persistent volume for the database.
	if _, ok := doc.Volumes["gseer-db-data"]; !ok {
		t.Error("persistent volume gseer-db-data missing")
	}

	// The .env holds both secrets, at 0600, with the non-loopback
	// contract satisfied (token present).
	envStr := string(envBody)
	if !strings.Contains(envStr, "GSEER_TOKEN=") || !strings.Contains(envStr, "GSEER_DB_PASSWORD=") {
		t.Errorf(".env missing secrets:\n%s", envStr)
	}
	st, err := os.Stat(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	// 0600 is the POSIX contract.  On Windows os.Chmod can only map
	// the single owner-write bit to the read-only attribute (Go
	// reports 0666 for writable files), so the honest Windows check
	// is "exists and writable"; full POSIX mode preservation is a
	// Unix property and is not claimed on Windows.
	if runtime.GOOS == "windows" {
		if st.Mode().Perm()&0o200 == 0 {
			t.Errorf(".env unexpectedly read-only on Windows: %v", st.Mode().Perm())
		}
	} else if st.Mode().Perm() != 0o600 {
		t.Errorf(".env mode = %v, want 0600", st.Mode().Perm())
	}
	// The generated token is not a placeholder.
	for _, line := range strings.Split(envStr, "\n") {
		if strings.HasPrefix(line, "GSEER_TOKEN=") {
			tok := strings.TrimPrefix(line, "GSEER_TOKEN=")
			if len(tok) < 16 || tok == "changeme" {
				t.Errorf("generated token too weak: %q", tok)
			}
		}
	}
}

func TestSetupWithComposeFullRefusesExistingEnv(t *testing.T) {
	resetSetupFlags(t)
	dir := t.TempDir()
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GSEER_DB_PASSWORD=live\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := &strings.Builder{}
	cmd := findSetup(t)
	cmd.SetOut(out)
	_ = cmd.Flags().Set("with-compose", "true")
	_ = cmd.Flags().Set("full", "true")
	err := cmd.RunE(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "E_ENV_EXISTS") {
		t.Fatalf("existing .env overwritten or wrong error: %v", err)
	}
	// The live .env survived byte-for-byte.
	body, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if string(body) != "GSEER_DB_PASSWORD=live\n" {
		t.Errorf(".env was modified: %q", body)
	}
}

func TestDockerfileContract(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatalf("Dockerfile missing: %v", err)
	}
	s := string(body)
	for _, want := range []string{
		"FROM golang:1.26-alpine AS build",
		"FROM alpine:3.20",
		"USER gseer",
		"CGO_ENABLED=0",
		`ENTRYPOINT ["gseer"]`,
		`CMD ["server", "start"]`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Dockerfile missing %q", want)
		}
	}
}
