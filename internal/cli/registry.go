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

// registry.go — the profile-aware opener for the `gseer source` /
// `gseer collect` surfaces (v0.3.0).
//
// Before v0.3.0 these commands were hardwired to PostgreSQL: a personal
// mode user could load fixtures but never COLLECT anything.  The
// collect.Backend/RegistryBackend contracts now let both engines serve
// the same command surface:
//
//   - personal     → sqlite.Store (one embedded file, no server);
//   - self-hosted  → postgres.Store (the hardened deployment);
//   - client       → refused (no local engine — E_CLIENT_ONLY).

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Lutfifakee-Project/GSEER/internal/collect"
	"github.com/Lutfifakee-Project/GSEER/internal/config"
	"github.com/Lutfifakee-Project/GSEER/internal/fprule"
	"github.com/Lutfifakee-Project/GSEER/internal/storage"
	"github.com/Lutfifakee-Project/GSEER/internal/storage/postgres"
	"github.com/Lutfifakee-Project/GSEER/internal/storage/sqlite"
)

// Compile-time proof that BOTH engines satisfy the full registry
// contract — a signature drift on either side fails the build here,
// not at an operator's terminal.
var (
	_ collect.RegistryBackend = (*postgres.Store)(nil)
	_ collect.RegistryBackend = (*sqlite.Store)(nil)
)

// fingerprintSetter is the optional engine hook for rule-driven
// enrichment (both stores implement it).
type fingerprintSetter interface {
	SetFingerprintEngine(reg *fprule.Registry)
}

// openRegistryStore loads the config, resolves the profile, and opens
// the registry/collection store for whichever engine that profile
// selects.  The returned closer releases the engine's resources and is
// always non-nil when err == nil.
func openRegistryStore(cmd *cobra.Command) (collect.RegistryBackend, func(), *config.Config, error) {
	cfg, cerr := config.Load(config.Flags{
		ConfigPath: flags.configPath,
		Profile:    flags.profile,
		Endpoint:   flags.server,
	})
	if cerr != nil {
		return nil, nil, nil, fmt.Errorf("E_CONFIG: %s\nTry: gseer setup --help", cerr.Error())
	}
	switch cfg.Profile {
	case config.ProfilePersonal:
		st, res, err := personalStore(commandContext(cmd), cfg)
		if err != nil {
			return nil, nil, nil, err
		}
		return st, func() { _ = res.DB.Close() }, cfg, nil
	case config.ProfileSelfHosted:
		pool, err := openAppPool(cmd, cfg)
		if err != nil {
			return nil, nil, nil, err
		}
		return postgres.NewStore(pool), func() { pool.Close() }, cfg, nil
	default:
		return nil, nil, nil, fmt.Errorf("E_CLIENT_ONLY: `gseer source` and `gseer collect` manage the LOCAL registry\n"+
			"Why: the client profile has no local database (current profile: %s)\n"+
			"Try: gseer setup --personal  (single-file personal mode) or gseer setup --self-hosted", cfg.Profile)
	}
}

// sourceNotFound reports whether err is the registry lookup miss
// (both engines return the same storage-level sentinel).
func sourceNotFound(err error) bool {
	return errors.Is(err, storage.ErrSourceNotFound)
}
