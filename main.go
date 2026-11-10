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

// Package main exists for exactly ONE reason (REL-01): the Go module
// root is the package path resolved by the short-form install command
//
//	go install github.com/Lutfifakee-Project/GSEER@latest
//
// and `go install` requires a main package at the module root.  Without
// this file that command fails with "does not contain package" and the
// easy-install path documented in docs/INSTALL.md is broken.
//
// This file is a deliberate mirror of cmd/gseer/main.go — the canonical
// release entry point that GoReleaser builds (builds.main in
// .goreleaser.yaml).  It contains NO command logic of its own: all
// dispatch lives in internal/cli, so there is exactly one command tree
// and both entry points cannot diverge.  TestMain-equivalence is
// enforced by main_test.go, which builds BOTH entry points and asserts
// identical stdout and exit codes for representative invocations.
package main

import (
	"fmt"
	"os"

	"github.com/Lutfifakee-Project/GSEER/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		// Errors are formatted by the cli layer per the style guide
		// (what failed → likely why → exact next command, AC-5.9).  The
		// exit code is the cobra SilenceErrors contract — the cli layer
		// sets it via os.Exit when a specific code is required (e.g. 64
		// for reserved/planned commands per AC-5.7–5.8).
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
