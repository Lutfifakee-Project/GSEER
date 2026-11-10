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
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lutfifakee-Project/GSEER/internal/config"
	"github.com/Lutfifakee-Project/GSEER/internal/update"
	"github.com/Lutfifakee-Project/GSEER/internal/version"
)

// update.go — `gseer update` (v0.1.0, owner directive §5): the signed
// update command.  Verification is FIRST (Ed25519 over the manifest's
// canonical bytes against the key embedded in THIS binary), download is
// LAST, replacement is atomic with a .bak backup and unconditional
// rollback.  An unsigned manifest from a familiar URL is still an
// unsigned manifest — refused.
//
// Modes:
//
//	gseer update --check           verify + report only (no download)
//	gseer update --apply           download + verify + replace
//	gseer update --apply --force   allow same/older versions
func registerUpdateCmd(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Verify and apply signed updates (signature → version → platform → checksum → atomic replace)",
		Long: `Verify and apply signed updates.

The update manifest MUST carry an Ed25519 signature that verifies
against the public key embedded in this binary.  The download URL is
IRRELEVANT to trust: an unsigned or wrongly-signed manifest is refused
no matter where it came from.

Chain (every step gates the next):
  1. manifest signature (embedded key)
  2. version relation (strictly newer; --force overrides)
  3. platform artifact for this GOOS/GOARCH
  4. SHA-256 + size of the downloaded payload
  5. atomic rename over the running binary (previous binary kept as
     <binary>.bak; any failure rolls the replacement back)

The manifest location comes from update.manifest_url (config) or
GSEER_UPDATE_MANIFEST_URL.  See docs/UPDATE.md for the key ceremony and
the recovery procedure.`,
		Args: cobra.NoArgs,
		RunE: runUpdate,
	}
	cmd.Flags().Bool("check", false, "verify the manifest and report (no download, no replace)")
	cmd.Flags().Bool("apply", false, "download the artifact and replace the running binary")
	cmd.Flags().String("manifest", "", "manifest URL or file path (overrides config/env)")
	cmd.Flags().Bool("force", false, "apply even when not newer (re-install/downgrade)")
	root.AddCommand(cmd)
}

func runUpdate(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	fCheck, _ := cmd.Flags().GetBool("check")
	fApply, _ := cmd.Flags().GetBool("apply")
	fManifest, _ := cmd.Flags().GetString("manifest")
	fForce, _ := cmd.Flags().GetBool("force")

	if fCheck && fApply {
		printf(out, "E_USAGE: pick --check or --apply, not both\n")
		exitWith(ExitUsagePlanned)
		return nil // unreachable when exitFunc terminates (seam returns in tests)
	}

	location := fManifest
	if location == "" {
		// The updater needs the channel, not a full profile config — a
		// missing client.endpoint must not block the check (the rules
		// commands use the same tolerant resolution).
		if cfg, cerr := config.Load(config.Flags{
			ConfigPath: flags.configPath,
			Profile:    flags.profile,
			Endpoint:   flags.server,
		}); cerr == nil {
			location = cfg.Update.ManifestURL
		} else {
			location = os.Getenv("GSEER_UPDATE_MANIFEST_URL")
		}
	}
	if location == "" {
		printf(out, "E_UPDATE_NO_CHANNEL: no update manifest configured\nWhy: the updater needs a signed manifest location\nTry: set update.manifest_url in the config or GSEER_UPDATE_MANIFEST_URL (see docs/UPDATE.md)\n")
		exitWith(ExitInvalidInput)
		return nil // unreachable when exitFunc terminates
	}

	current := version.Current().Version
	target, err := os.Executable()
	if err != nil {
		printf(out, "E_UPDATE_TARGET: cannot locate the running binary: %v\n", err)
		exitWith(ExitInternal)
		return nil // unreachable when exitFunc terminates
	}
	if resolved, rerr := os.Stat(target); rerr == nil {
		_ = resolved // symlink resolution left to the OS rename semantics
	}

	fetcher := &update.Fetcher{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	manifestBytes, err := update.LoadManifest(ctx, location, fetcher)
	if err != nil {
		printf(out, "%s\n", err.Error())
		exitWith(ExitUnavailable)
		return nil // unreachable when exitFunc terminates
	}

	if !fApply {
		// --check (default): verify + report; download nothing.
		m, cerr := update.Check(manifestBytes, nil, current)
		if cerr != nil {
			if update.IsUpToDate(cerr) {
				printf(out, "[ ok  ] up to date: running %s; channel offers %s (signature verified)\n", current, m.Version)
				return nil
			}
			printf(out, "%s\n", cerr.Error())
			exitWith(ExitInvalidInput)
			return nil // unreachable when exitFunc terminates
		}
		art, _ := m.SelectArtifact(goosSafe(), goarchSafe())
		printf(out, "update available: %s → %s (released %s, signature verified)\n", current, m.Version, m.ReleasedAt.Format(time.DateOnly))
		if art != nil {
			printf(out, "platform artifact: %s/%s, %d bytes, sha256 %.16s…\n", art.OS, art.Arch, art.Size, art.SHA256)
		}
		if m.NotesURL != "" {
			printf(out, "notes: %s\n", m.NotesURL)
		}
		printf(out, "apply with: gseer update --apply\n")
		return nil
	}

	// --apply: the full chain.
	res, err := update.Apply(ctx, manifestBytes, nil, current, target, fForce, fetcher)
	if err != nil && !update.IsDirSyncWarning(err) {
		printf(out, "%s\n", err.Error())
		printf(out, "The running binary was left untouched (or restored from %s.bak).\n", target)
		exitWith(ExitInvalidInput)
		return nil // unreachable when exitFunc terminates
	}
	if err != nil {
		// Dir-sync durability warning: the binary WAS replaced (nothing
		// was rolled back) — report the success plus the warning instead
		// of a false "left untouched" failure.
		printf(out, "[warn] %s\n", err.Error())
	}
	printf(out, "[ ok  ] updated %s → %s (%d bytes) at %s\n", res.PreviousVersion, res.NewVersion, res.Bytes, res.Replaced)
	printf(out, "previous binary kept at %s.bak — restore it manually if the new build misbehaves\n", target)
	printf(out, "restart the server to run the new version: gseer server restart (or stop + start)\n")
	return nil
}

// goosSafe/goarchSafe avoid importing runtime at every call site in the
// command layer (the update package owns the platform indirections for
// its own selection; the CLI's check-path display needs the pair too).
func goosSafe() string   { return update.RuntimeGOOS() }
func goarchSafe() string { return update.RuntimeGOARCH() }
