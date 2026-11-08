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

// Package cli implements the `gseer` command tree (SPEC-03).
//
// M1 scope: the root command, the `version` command (fully working), and
// stub registrations for every other command listed in SPEC-03 §2 so the
// help tree is truthful (AC-5.1 stub) — no command is listed that is not
// registered, and the "Coming later" pointer line is honest about which
// commands are absent (update/scan/asset/export → exit 64, AC-5.7–5.8).
// The full command behavior lands in M11; the stubs here say so plainly.
//
// The Search ≠ Scan seam (ADR-0010) is enforced from M1: no command in
// this package imports any collector/egress package (none exists in the
// v0.0.1 module; AC-3.1 import-graph test pins the invariant).
package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// Exit codes per SPEC-03 §9.  These are stable across every command and
// surface; the CLI never returns ad-hoc codes.
const (
	ExitSuccess      = 0  // Success (including a zero-result search)
	ExitInvalidInput = 2  // Invalid query / invalid input
	ExitConfig       = 3  // Configuration failure
	ExitUnavailable  = 4  // Service unavailable (endpoint/DB/index)
	ExitAuth         = 5  // Authentication failure
	ExitUsagePlanned = 64 // Usage error (wrong args; planned-command pointer)
	ExitInternal     = 70 // Internal error (stack trace only with --verbose)
)

// rootFlags holds the global persistent flags declared on RootCmd.  M1
// wires only the flag declarations; flag handling lands with the commands
// that consume them (M2 config, M11 CLI).
type rootFlags struct {
	configPath string
	profile    string
	server     string
	output     string
	verbose    bool
	quiet      bool
	noColor    bool
	showVer    bool
}

// RootCmd is the top-level `gseer` command.  Help text matches SPEC-03 §2
// verbatim (the "Coming later" pointer line is normative; AC-5.1 parses it).
var RootCmd = &cobra.Command{
	Use:   "gseer",
	Short: "GSEER — Internet Asset Intelligence & Search Engine",
	Long: `GSEER — Internet Asset Intelligence & Search Engine
Discover. Identify. Search.

Quick start:
  gseer setup
  gseer doctor
  gseer search 'port==443 && title CONTAINS "login"'
Coming later (not in this release — see docs/ROADMAP.md):
  scan (authorized observation), asset
License: GNU AGPL-3.0 — https://github.com/Lutfifakee-Project/GSEER`,
	// Disable the cobra completion subcommand's auto-help banner; we
	// register `completion` ourselves with the exact short description.
	CompletionOptions: cobra.CompletionOptions{
		DisableDefaultCmd: false,
	},
	// Silence cobra's default error printing — the cli layer formats
	// errors per the style guide (AC-5.9: what failed → why → next cmd).
	SilenceErrors: true,
	SilenceUsage:  true,
}

// flags is the singleton rootFlags; populated by persistent flag parsing.
var flags rootFlags

// init registers the persistent flags and every subcommand on RootCmd at
// package load time.  This is the idiomatic cobra pattern and makes the
// command tree available to tests (AC-5.1 stub) without requiring them
// to call Execute first.  Each registration is a separate function so
// the file layout matches the SPEC-02 §2 package map.
func init() {
	RootCmd.PersistentFlags().StringVar(&flags.configPath, "config", "", "path to config file (default: $GSEER_CONFIG or platform default)")
	RootCmd.PersistentFlags().StringVar(&flags.profile, "profile", "", "configuration profile (client | self-hosted)")
	RootCmd.PersistentFlags().StringVar(&flags.server, "server", "", "endpoint URL for client profile")
	RootCmd.PersistentFlags().StringVarP(&flags.output, "output", "o", "", "output format: table|json|yaml|csv|ndjson")
	RootCmd.PersistentFlags().BoolVar(&flags.verbose, "verbose", false, "verbose output (include stack traces on error)")
	RootCmd.PersistentFlags().BoolVar(&flags.quiet, "quiet", false, "quiet output (suppress tips and banners)")
	RootCmd.PersistentFlags().BoolVar(&flags.noColor, "no-color", false, "disable color output")
	RootCmd.PersistentFlags().BoolVar(&flags.showVer, "version", false, "show version and exit")

	registerVersionCmd(RootCmd)
	registerSearchCmd(RootCmd)
	registerHostCmd(RootCmd)
	registerSetupCmd(RootCmd)
	registerDoctorCmd(RootCmd)
	registerServerCmd(RootCmd)
	registerSourceCmd(RootCmd)
	registerCollectCmd(RootCmd)
	registerSuppressCmd(RootCmd)
	registerExportCmd(RootCmd)
	registerUpdateCmd(RootCmd)
	registerRulesCmd(RootCmd)
	registerDBCmd(RootCmd)
	registerComingLaterCmds(RootCmd)

	// Banner before help for root-level invocations only
	// (`gseer`, `gseer --help`, `gseer help`).  Subcommand help uses the
	// default cobra help func and prints no banner.
	RootCmd.SetHelpFunc(bannerHelpFunc)
}

// Execute runs the root command and returns the cobra error (if any).
// main.go translates a non-nil error into exit code 1 unless a command
// has already called os.Exit with a specific code (e.g. 64 for planned
// commands per AC-5.7–5.8).  Command and flag registration happens in
// init() so Execute is just the dispatch call.
func Execute() error {
	return RootCmd.Execute()
}

// printf and println are the rendering helpers for command output.
// The output target is the cobra-managed writer (in-memory buffer in
// tests, stdout in production).  A write failure to a closed stdout
// terminates the process via SIGPIPE before the command can do
// anything useful with the error, so there is no actionable recovery;
// the discard is explicit here — in ONE auditable place — keeping the
// per-command rendering call sites readable (M1 errcheck discipline:
// no unchecked error returns anywhere in the package).
func printf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}

func println(w io.Writer, a ...any) {
	_, _ = fmt.Fprintln(w, a...)
}
