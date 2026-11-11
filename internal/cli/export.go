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
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lutfifakee-Project/gseer/internal/config"
	"github.com/Lutfifakee-Project/gseer/internal/export"
	"github.com/Lutfifakee-Project/gseer/internal/search"
	"github.com/Lutfifakee-Project/gseer/internal/searchlite"
)

// export.go — `gseer export` (v0.1.0, owner directive §4): stream a GQL
// result set as JSON, CSV, or NDJSON.  Pagination rides the SAME
// SearchService keyset cursor as interactive search (stable page
// sequence); output streams per page — memory stays O(page).  The
// export carries index rows + query metadata only: no configuration,
// no connection strings, no credentials.
func registerExportCmd(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "export <query>",
		Short: "Export search results as JSON, CSV, or NDJSON (streamed, cursor-stable)",
		Long: `Export search results as JSON, CSV, or NDJSON.

The query language is the same GQL as gseer search; export pages through
the same SearchService with keyset cursors, so the stream is stable and
re-exporting after new ingestions never repeats already-emitted rows.

Formats:
  json    one streamed object: {"rows": [...], "count": N, "footer": {...}}
  csv     RFC 4180 with a header row; arrays join on ";"
  ndjson  one row per line + a trailing {"record":"footer"} metadata line

Output: --out FILE (a timestamped default name in the working directory
when omitted; existing files need --force), or stdout when --out is "-".

The export stream carries index rows and query metadata only — never
configuration or credentials.`,
		Args: cobra.ExactArgs(1),
		RunE: runExport,
	}
	cmd.Flags().String("format", "json", "output format: json | csv | ndjson")
	cmd.Flags().String("out", "", `output file (default: gseer-export-<timestamp>.<ext>; "-" = stdout)`)
	cmd.Flags().Int("max", 0, "row cap for the export (0 = unlimited — the operator's explicit choice)")
	cmd.Flags().String("mode", "", "search mode: service (default) | asset")
	cmd.Flags().String("sort", "", `sort fields, e.g. "last_seen:desc" (default: last_seen:desc)`)
	cmd.Flags().StringSlice("fields", nil, "projection fields for json/ndjson (csv always emits the canonical columns)")
	cmd.Flags().Duration("timeout", 0, "per-page time budget (default 5s, hard cap 30s)")
	cmd.Flags().Bool("force", false, "overwrite an existing non-empty --out file")
	root.AddCommand(cmd)
}

func runExport(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	gql := args[0]

	fFormat, _ := cmd.Flags().GetString("format")
	fOut, _ := cmd.Flags().GetString("out")
	fMax, _ := cmd.Flags().GetInt("max")
	fMode, _ := cmd.Flags().GetString("mode")
	fSort, _ := cmd.Flags().GetString("sort")
	fFields, _ := cmd.Flags().GetStringSlice("fields")
	fTimeout, _ := cmd.Flags().GetDuration("timeout")
	fForce, _ := cmd.Flags().GetBool("force")

	cfg, cerr := engineConfigOrInit(cmd)
	if cerr != nil {
		printf(out, "%s\nTry: gseer setup --help\n", cerr.Error())
		exitWith(ExitConfig)
	}
	if cfg.Profile == config.ProfileClient {
		printf(out, "E_USAGE: export runs against the LOCAL index (self-hosted profile).\nWhy: the client profile holds no data; query the server's /api/v1/export endpoint instead.\nTry: gseer --profile self-hosted export ... or the REST API\n")
		exitWith(ExitUsagePlanned)
	}

	// Resolve the output BEFORE any query work: a bad path must fail
	// fast, before the database is touched.
	dest := out
	fName := ""
	if fOut != "-" && fOut != "" {
		p, err := export.ResolveOutPath(fOut, export.FormatExtension(fFormat), fForce)
		if err != nil {
			printf(out, "%s\n", err.Error())
			exitWith(ExitInvalidInput)
		}
		f, err := os.Create(p) // #nosec G304 -- p was sanitized by export.ResolveOutPath (.. / control chars / overwrite refused)
		if err != nil {
			printf(out, "E_EXPORT_PATH: cannot create %s: %v\n", p, err)
			exitWith(ExitInvalidInput)
		}
		defer func() { _ = f.Close() }()
		dest = f
		fName = p
	} else {
		// Streaming to stdout: EVERY status line of this command
		// (connection banner included) goes to stderr so the piped
		// stream stays parseable.
		cmd.SetOut(cmd.ErrOrStderr())
		out = cmd.ErrOrStderr()
	}

	// Backend selection: the personal profile exports through the
	// embedded SQLite service (the same stream, the same formats);
	// the self-hosted profile goes through the PostgreSQL service.
	// The client profile was refused above (no local data).
	var svc export.Searcher
	if cfg.Profile == config.ProfilePersonal {
		res, oerr := openPersonal(cfg)
		if oerr != nil {
			printf(out, "E_DB_UNAVAILABLE: cannot open the personal database\nWhy: %v\nTry: gseer setup --personal\n", oerr)
			exitWith(ExitUnavailable)
			return nil
		}
		defer func() { _ = res.DB.Close() }()
		svc = searchlite.New(res.DB)
	} else {
		pool, err := openAppPool(cmd, cfg)
		if err != nil {
			return err
		}
		defer pool.Close()
		svc = search.New(pool)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second+maxDuration(fTimeout, 0))
	defer cancel()

	st, err := export.Stream(ctx, svc, export.Request{
		Query: search.Query{
			GQL:     gql,
			Mode:    fMode,
			Sort:    fSort,
			Fields:  fFields,
			Timeout: fTimeout,
		},
		Format:  fFormat,
		MaxRows: fMax,
	}, dest)
	if err != nil {
		printf(out, "%s\n", err.Error())
		exitWith(ExitInvalidInput)
	}

	// The stats line ALWAYS goes to the CLI output (never the file).
	// When the stream itself is stdout, the stats go to STDERR so a
	// piped/redirected stream stays parseable.
	statusOut := out
	if dest == out {
		statusOut = cmd.ErrOrStderr()
	}
	where := "stdout"
	if fName != "" {
		where = fName
	}
	verb := "exported"
	note := ""
	if st.Truncated {
		verb = "exported (STOPPED at the row cap)"
		note = fmt.Sprintf("\nThe export stopped at --max %d rows; re-run with a higher --max (or --max 0 for unlimited) to continue beyond this cap.", fMax)
	}
	printf(statusOut, "%s: %d row(s) in %d page(s) → %s [%s, %d ms]%s\n",
		verb, st.Rows, st.Pages, where, st.Format, st.TookMs, note)
	return nil
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
