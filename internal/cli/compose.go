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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// compose.go — `gseer setup --with-compose` (QS-2): write the db-only
// docker-compose.yml (the canonical template shipped since M1) and a
// generated .env with a random password, then print the next commands
// verbatim.  Generation never overwrites an existing .env silently and
// never weakens the loopback-only port binding.
//
// v0.1.0 adds `--full`: the complete self-hosted stack (db + API/dashboard
// server, healthchecks, persistent volume, generated API token) per the
// owner directive §6.  The full stack deliberately has NO collector
// container — nothing collects uninvoked; collection is
// `docker compose exec server gseer collect run ...` by explicit
// operator action.  Container deployments upgrade by rebuilding the
// image, not via `gseer update` (the image binary is root-owned).
//
// v0.1.0 adds `--full`: the complete self-hosted stack (db + API/dashboard
// server, healthchecks, persistent volume, generated API token) per the
// owner directive §6.  The full stack deliberately has NO collector
// container — nothing collects uninvoked; collection is
// `docker compose exec server gseer collect run ...` by explicit operator
// action, or bare-metal `gseer collect run` against the published DB.

// composeTemplate is the canonical db-only compose (kept byte-identical
// in structure to the repo-root docker-compose.yml shipped in M1).
const composeTemplate = `# GSEER v0.0.1 docker-compose — db only (QS-2).
#
# Written by: gseer setup --with-compose
# Defines ONLY the ` + "`db`" + ` service: PostgreSQL 17-alpine, a named volume,
# a loopback-only port binding, and healthcheck-gated readiness.
# No collector, no egress, no public service.

services:
  db:
    image: postgres:17-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: ${GSEER_DB_USER:-gseer}
      POSTGRES_PASSWORD: ${GSEER_DB_PASSWORD:?set GSEER_DB_PASSWORD in .env}
      POSTGRES_DB: ${GSEER_DB_NAME:-gseer}
      POSTGRES_INITDB_ARGS: "--encoding=UTF8 --locale=C.UTF-8"
    ports:
      # Loopback-only: the database is not reachable from other hosts.
      - "127.0.0.1:5432:5432"
    volumes:
      - gseer-db-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${GSEER_DB_USER:-gseer} -d ${GSEER_DB_NAME:-gseer}"]
      interval: 5s
      timeout: 3s
      retries: 12
      start_period: 10s

volumes:
  gseer-db-data:
    name: gseer-db-data
`

// fullComposeTemplate is the v0.1.0 complete self-hosted stack: db +
// server.  Security posture:
//   - the DB publishes NO host port (compose-internal network only —
//     strictly stronger than the db-only template's loopback bind);
//   - the server publishes the API on the HOST loopback only;
//   - the server binds 0.0.0.0 INSIDE the container (compose network),
//     which requires an API token (the E_CONFIG_INSECURE gate) — the
//     generated .env carries a random one;
//   - migrations run automatically on server start (QS-7); a dedicated
//     init/migrate container would be an unused dependency (directive §6).
const fullComposeTemplate = `# GSEER v0.1.0 docker-compose — full self-hosted stack (db + server).
#
# Written by: gseer setup --with-compose --full
#
# Services:
#   db      PostgreSQL 17 (compose-internal; NO host port published)
#   server  the GSEER API + dashboard (host loopback :8443, token-authed)
#
# Deliberately absent: a collector container.  Nothing collects
# uninvoked — run
#   docker compose exec server gseer collect run --source <name>
# explicitly, and only for sources whose terms you have reviewed
# (docs/SOURCES.md).  Container deployments upgrade by rebuilding the
# image, not via gseer update (docs/UPDATE.md §1).

services:
  db:
    image: postgres:17-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: ${GSEER_DB_USER:-gseer}
      POSTGRES_PASSWORD: ${GSEER_DB_PASSWORD:?set GSEER_DB_PASSWORD in .env}
      POSTGRES_DB: ${GSEER_DB_NAME:-gseer}
      POSTGRES_INITDB_ARGS: "--encoding=UTF8 --locale=C.UTF-8"
    # No ports section: the database is reachable ONLY from the compose
    # network (the server service).  Host-side admin/backup:
    #   docker compose exec db pg_dump ...
    volumes:
      - gseer-db-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${GSEER_DB_USER:-gseer} -d ${GSEER_DB_NAME:-gseer}"]
      interval: 5s
      timeout: 3s
      retries: 12
      start_period: 10s

  server:
    build:
      context: .
      args:
        VERSION: ${GSEER_VERSION:-v0.1.0}
        COMMIT: ${GSEER_COMMIT:-unknown}
    image: gseer:local
    restart: unless-stopped
    depends_on:
      db:
        condition: service_healthy
    environment:
      GSEER_PROFILE: self-hosted
      GSEER_DB_DSN: postgres://${GSEER_DB_USER:-gseer}:${GSEER_DB_PASSWORD:?set GSEER_DB_PASSWORD in .env}@db:5432/${GSEER_DB_NAME:-gseer}?sslmode=disable
      # Non-loopback bind inside the container network — the API token
      # (the E_CONFIG_INSECURE gate) is mandatory and generated in .env.
      GSEER_LISTEN: 0.0.0.0:8443
      GSEER_TOKEN: ${GSEER_TOKEN:?set GSEER_TOKEN in .env}
    ports:
      # Host loopback only; expose to a LAN deliberately and keep the
      # token (the server refuses tokenless non-loopback binds).
      - "127.0.0.1:8443:8443"
    healthcheck:
      test: ["CMD-SHELL", "wget -q -O- http://127.0.0.1:8443/health || exit 1"]
      interval: 10s
      timeout: 3s
      retries: 12
      start_period: 15s
    stop_grace_period: 30s

volumes:
  gseer-db-data:
    name: gseer-db-data
`

// fullEnvTemplateFmt is the generated .env for the full stack: database
// credential + API token (both random).  GSEER_DSN is the HOST-side
// convenience DSN for bare-metal CLI access over the published loopback
// port; the container itself uses the compose-internal `db` hostname.
const fullEnvTemplateFmt = `# GSEER .env — generated by: gseer setup --with-compose --full
# Both secrets below were randomly generated at write time.
# Keep this file private (chmod 600): it holds the database password
# AND the API token.
GSEER_DB_USER=gseer
GSEER_DB_PASSWORD=%s
GSEER_DB_NAME=gseer
GSEER_TOKEN=%s
GSEER_VERSION=v0.1.0
# Host-side DSN for CLI tools running OUTSIDE compose (the server
# container uses db:5432 over the compose network).
GSEER_DSN=postgres://gseer:%s@127.0.0.1:5432/gseer?sslmode=disable
`

// envTemplate is the generated .env body.  The DSN matches the compose
// loopback binding (host-side port 5432).
const envTemplateFmt = `# GSEER .env — generated by: gseer setup --with-compose
# The database password was randomly generated at write time.
# Keep this file private (it holds the database credential).
GSEER_DB_USER=gseer
GSEER_DB_PASSWORD=%s
GSEER_DB_NAME=gseer
GSEER_DSN=postgres://gseer:%s@127.0.0.1:5432/gseer?sslmode=disable
`

// writeComposeFiles writes ./docker-compose.yml (if absent) and ./.env
// (if absent) into dir.  Returns the actions taken.  An existing .env is
// NEVER overwritten (it may hold a live credential); an existing compose
// file is kept when byte-identical, refused otherwise.
func writeComposeFiles(dir string) (wrote []string, err error) {
	composePath := filepath.Join(dir, "docker-compose.yml")
	envPath := filepath.Join(dir, ".env")

	composeBody := composeTemplate
	if existing, rerr := os.ReadFile(composePath); rerr == nil { // #nosec G304 -- reads the operator's existing compose file for the never-overwrite comparison
		if strings.TrimSpace(string(existing)) != strings.TrimSpace(composeBody) {
			return nil, fmt.Errorf("E_COMPOSE_EXISTS: %s already exists with different content\n"+
				"Why: --with-compose never overwrites an operator-edited compose file\n"+
				"Try: keep the existing file (it already defines the db service) or remove it first", composePath)
		}
	} else if os.IsNotExist(rerr) {
		if werr := os.WriteFile(composePath, []byte(composeBody), 0o644); werr != nil { // #nosec G306 -- no secrets in the compose file; the credential lives in .env (0600)
			return nil, fmt.Errorf("E_COMPOSE_WRITE: cannot write %s: %w", composePath, werr)
		}
		wrote = append(wrote, composePath)
	} else {
		return nil, fmt.Errorf("E_COMPOSE_STAT: %s: %w", composePath, rerr)
	}

	if _, serr := os.Stat(envPath); serr == nil {
		return nil, fmt.Errorf("E_ENV_EXISTS: %s already exists\n"+
			"Why: --with-compose never overwrites an .env (it may hold a live credential)\n"+
			"Try: reuse the existing .env (GSEER_DB_PASSWORD must be set) or remove it first", envPath)
	}
	password, perr := randomPassword()
	if perr != nil {
		return nil, fmt.Errorf("E_COMPOSE_RANDOM: %w", perr)
	}
	envBody := fmt.Sprintf(envTemplateFmt, password, password) // #nosec G101 -- template placeholders filled with the runtime-generated password, not hardcoded credentials
	if werr := os.WriteFile(envPath, []byte(envBody), 0o600); werr != nil {
		return nil, fmt.Errorf("E_COMPOSE_WRITE: cannot write %s: %w", envPath, werr)
	}
	wrote = append(wrote, envPath)
	return wrote, nil
}

// randomPassword returns a 24-hex-char password from crypto/rand.
func randomPassword() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// runSetupWithCompose generates the compose + .env pair and prints the
// next commands verbatim (QS-2).  Runs after a successful --self-hosted
// provisioning OR standalone with --self-hosted.  full=true selects the
// v0.1.0 complete stack (db + server).
func runSetupWithCompose(cmd *cobra.Command, dir string, full bool) error {
	out := cmd.OutOrStdout()
	var wrote []string
	var err error
	if full {
		wrote, err = writeFullComposeFiles(dir)
	} else {
		wrote, err = writeComposeFiles(dir)
	}
	if err != nil {
		return err
	}
	for _, f := range wrote {
		printf(out, "[ ok  ] wrote %s\n", f)
	}
	if !full {
		printf(out, "\nNext (QS-2):\n")
		printf(out, "  docker compose up -d db                    # DB now running (healthcheck-gated)\n")
		printf(out, "  docker compose ps db                       # expect: healthy\n")
		printf(out, "  gseer setup --self-hosted --db-url \"$(grep '^GSEER_DSN=' .env | cut -d= -f2-)\"\n\n")
		printf(out, "The database binds 127.0.0.1 only; the generated .env holds the credential (chmod 600).\n")
		return nil
	}
	printf(out, "\nNext (full stack, docs/DOCKER.md):\n")
	printf(out, "  docker compose -f docker-compose.full.yml up -d --build   # db + server (migrations run on start)\n")
	printf(out, "  docker compose -f docker-compose.full.yml ps              # expect: db healthy, server healthy\n")
	printf(out, "  curl -s http://127.0.0.1:8443/api/v1/health               # expect: {\"status\":\"ok\"}\n")
	printf(out, "  gseer --server http://127.0.0.1:8443 --token \"$(grep '^GSEER_TOKEN=' .env | cut -d= -f2-)\" search 'port == 443'\n\n")
	printf(out, "Posture: the DB publishes NO host port (compose-internal); the API publishes on host loopback only and requires the generated token.  The .env holds both secrets (chmod 600).\n")
	printf(out, "Lifecycle (startup/upgrade/backup/recovery): docs/DOCKER.md.\n")
	return nil
}

// writeFullComposeFiles writes ./docker-compose.full.yml + ./.env for the
// complete stack.  Same overwrite-safety contract as writeComposeFiles:
// an existing .env is never replaced (live credentials), an existing
// compose file must be byte-identical or the operator keeps theirs.
func writeFullComposeFiles(dir string) (wrote []string, err error) {
	composePath := filepath.Join(dir, "docker-compose.full.yml")
	envPath := filepath.Join(dir, ".env")

	if existing, rerr := os.ReadFile(composePath); rerr == nil { // #nosec G304 -- reads the operator's existing full-stack compose for the never-overwrite comparison
		if strings.TrimSpace(string(existing)) != strings.TrimSpace(fullComposeTemplate) {
			return nil, fmt.Errorf("E_COMPOSE_EXISTS: %s already exists with different content\n"+
				"Why: --with-compose never overwrites an operator-edited compose file\n"+
				"Try: keep the existing file or remove it first", composePath)
		}
	} else if os.IsNotExist(rerr) {
		if werr := os.WriteFile(composePath, []byte(fullComposeTemplate), 0o644); werr != nil { // #nosec G306 -- no secrets in the compose file; the credential lives in .env (0600)
			return nil, fmt.Errorf("E_COMPOSE_WRITE: cannot write %s: %w", composePath, werr)
		}
		wrote = append(wrote, composePath)
	} else {
		return nil, fmt.Errorf("E_COMPOSE_STAT: %s: %w", composePath, rerr)
	}

	if _, serr := os.Stat(envPath); serr == nil {
		return nil, fmt.Errorf("E_ENV_EXISTS: %s already exists\n"+
			"Why: --with-compose never overwrites an .env (it may hold live credentials)\n"+
			"Try: reuse the existing .env (GSEER_DB_PASSWORD and GSEER_TOKEN must be set) or remove it first", envPath)
	}
	password, perr := randomPassword()
	if perr != nil {
		return nil, fmt.Errorf("E_COMPOSE_RANDOM: %w", perr)
	}
	token, perr := randomPassword()
	if perr != nil {
		return nil, fmt.Errorf("E_COMPOSE_RANDOM: %w", perr)
	}
	envBody := fmt.Sprintf(fullEnvTemplateFmt, password, token, password) // #nosec G101 -- template placeholders filled with runtime-generated secrets, not hardcoded credentials
	if werr := os.WriteFile(envPath, []byte(envBody), 0o600); werr != nil {
		return nil, fmt.Errorf("E_COMPOSE_WRITE: cannot write %s: %w", envPath, werr)
	}
	wrote = append(wrote, envPath)
	return wrote, nil
}
