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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lutfifakee-Project/GSEER/internal/config"
	"github.com/Lutfifakee-Project/GSEER/internal/search"
	"github.com/Lutfifakee-Project/GSEER/internal/storage/postgres"
	"github.com/Lutfifakee-Project/GSEER/internal/version"
)

// doctor.go — `gseer doctor` (SPEC-03 §2; QS-4).
//
// M1 registered the command; M3 wires the DB-side checks (config
// validity, DB reachable + PG version, migrations at head) using the
// storage-layer check assembler.  The remaining QS-4 checks (binary
// checksum, index contents, server reachability) land with M11 when
// the fixtures and server exist — the help text and output are honest
// about which checks are wired (AC-8.x).
//
// Doctor is strictly read-only: it never migrates and never forces.
// A dirty migration state FAILS with a pointer to
// `gseer db migrate diagnose` (owner directive §2, M3 round).

// registerDoctorCmd attaches `gseer doctor`.
func registerDoctorCmd(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose config, connectivity, database, and index health",
		Long: `Diagnose config, connectivity, database, and index health.

Reports PASS/FAIL/HINT with a fix line for each check (QS-4).
Wired:
  - config validity
  - DB reachable + PG version compatible (PostgreSQL 15+, PD-8)
  - migrations at head (dirty state → gseer db migrate diagnose)
  - binary version / OS-arch / SHA-256 (self-reported; compare with the
    corresponding published release checksum when a binary release is
    available)
  - index contents: EMPTY | counts | fixture-labeled honesty label
  - server reachability (client profile: probes the configured endpoint)

Doctor MUST NOT show a generic "healthy" that hides an empty or fixture-
filled index (QS-4 normative).  Doctor is read-only: it never migrates.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()

			cfg, cerr := config.Load(config.Flags{
				ConfigPath: flags.configPath,
				Profile:    flags.profile,
				Endpoint:   flags.server,
			})
			pass, fail := 0, 0
			report := func(name, outcome, detail, fix string, err error) {
				switch outcome {
				case "PASS":
					pass++
					printf(out, "[ PASS ] %s: %s\n", name, detail)
				case "HINT":
					printf(out, "[ HINT ] %s: %s\n        Try: %s\n", name, detail, fix)
				case "SKIP":
					printf(out, "[ -    ] %s: %s\n", name, detail)
				default:
					fail++
					printf(out, "[ FAIL ] %s: %s\n", name, detail)
					if fix != "" {
						printf(out, "        Try: %s\n", fix)
					}
					if err != nil {
						// Render the full structured error (Code/Why/Try).
						printf(out, "        %s\n", strings.ReplaceAll(err.Error(), "\n", "\n        "))
					}
				}
			}

			if cerr != nil {
				report("config validity", "FAIL", "configuration is invalid", "gseer setup --help", cerr)
				printf(out, "\n%d PASS, %d FAIL. Fix the failures above and re-run.\n", pass, fail)
				os.Exit(ExitConfig)
			}
			report("config validity", "PASS", "loaded "+cfg.Source+" (profile="+string(cfg.Profile)+")", "", nil)

			// Binary identity: version/commit/build + SHA-256.
			info := version.Current()
			report("binary", "PASS", fmt.Sprintf("version %s, commit %s, built %s, %s/%s, sha256=%s",
				info.Version, orUnknown(info.Commit), orUnknown(info.BuiltAt),
				runtime.GOOS, runtime.GOARCH, binarySHA256()), "", nil)

			if cfg.Profile != config.ProfileSelfHosted {
				// Client profile: probe the configured endpoint.
				printf(out, "[ -    ] DB checks: skipped (client profile has no local database)\n")
				reportClientEndpoint(cfg, report)
				printf(out, "\n%d PASS, %d FAIL", pass, fail)
				if fail > 0 {
					println(out, ". Fix the failures above and re-run.")
					os.Exit(ExitUnavailable)
				}
				println(out, ".")
				return nil
			}

			dsn := cfg.Database.EffectiveDSN()
			results := postgres.DoctorDBChecks(cmd.Context(), dsn, cfg.Defaults.PostgresMajor)
			for _, r := range results {
				report(r.Name, string(r.Outcome), r.Detail, r.Fix, r.Err)
			}

			// Index contents (QS-4 honesty): empty vs counts vs fixture-labeled.
			reportIndexCheck(cmd, cfg, report)

			printf(out, "\n%d PASS, %d FAIL", pass, fail)
			if fail > 0 {
				println(out, ". Fix the failures above and re-run.")
				os.Exit(ExitUnavailable)
			}
			println(out, ".")
			return nil
		},
	}
	root.AddCommand(cmd)
}

// binarySHA256 returns the hex SHA-256 of the running executable (or
// "unavailable" when the path/content cannot be read — doctor stays
// honest about it).
func binarySHA256() string {
	path, err := os.Executable()
	if err != nil {
		return "unavailable"
	}
	b, err := os.ReadFile(path) // #nosec G304 -- reads the running binary itself (os.Executable) for the doctor fingerprint
	if err != nil {
		return "unavailable"
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// reportIndexCheck reports the QS-4 index-contents honesty check:
// EMPTY (HINT with the load pointer), fixture-labeled (HINT with the
// synthetic-data label), or live counts (PASS).
func reportIndexCheck(cmd *cobra.Command, cfg *config.Config, report func(name, outcome, detail, fix string, err error)) {
	ctx := commandContext(cmd)
	pool, err := postgres.NewPool(ctx, cfg.Database.EffectiveDSN())
	if err != nil {
		report("index", "FAIL", "cannot open the index pool", "gseer doctor", err)
		return
	}
	defer pool.Close()
	meta, serr := search.New(pool).Stats(ctx)
	if serr != nil {
		report("index", "FAIL", "index query failed", "gseer db migrate status", serr)
		return
	}
	switch {
	case meta.Empty:
		report("index", "HINT", "EMPTY — no assets or services ingested yet",
			"for evaluation data run: gseer setup --load-fixtures", nil)
	case meta.FixtureOnly:
		report("index", "HINT", fmt.Sprintf("contains FIXTURE data — %d assets, %d services, %d observations (SYNTHETIC, not real internet data)",
			meta.Assets, meta.Services, meta.Observations),
			"remove with: gseer setup --purge-fixtures", nil)
	default:
		report("index", "PASS", fmt.Sprintf("%d assets, %d services, %d observations (last observed %s)",
			meta.Assets, meta.Services, meta.Observations, orNever(meta.LastObserved)), "", nil)
	}
}

// reportClientEndpoint probes the configured endpoint's health route
// (client profile).
func reportClientEndpoint(cfg *config.Config, report func(name, outcome, detail, fix string, err error)) {
	endpoint := strings.TrimRight(cfg.Client.Endpoint, "/")
	if endpoint == "" {
		report("server reachability", "HINT", "no endpoint configured",
			"gseer setup --server https://gseer.example.com", nil)
		return
	}
	hc := &http.Client{Timeout: 5 * time.Second}
	resp, err := hc.Get(endpoint + "/api/v1/health")
	if err != nil {
		report("server reachability", "FAIL", fmt.Sprintf("%s did not answer", endpoint),
			"check client.endpoint and that the server is running", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		report("server reachability", "FAIL", fmt.Sprintf("%s answered HTTP %d", endpoint, resp.StatusCode),
			"check the server logs", nil)
		return
	}
	report("server reachability", "PASS", endpoint+" health OK", "", nil)
}

func orNever(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.UTC().Format("2006-01-02 15:04:05Z")
}
