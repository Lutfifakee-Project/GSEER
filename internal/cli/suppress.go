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

// suppress.go — `gseer suppress`: the operator surface of the centralized
// suppression policy layer (v0.0.2 M13, owner directive §3).
//
//      gseer suppress add <kind> <value> --reason "..."   add a rule and
//                                                         retract matching
//                                                         indexed records
//      gseer suppress list                                show every rule
//      gseer suppress remove <id>                         remove a rule
//
// Kinds: ip | cidr | domain | domain_suffix | source.
//
// Behavior contract:
//   - `add` writes the rule with its reason + author + optional expiry
//     (audit metadata), then runs the retroactive retraction in ONE
//     transaction and reports exactly what was removed.  --no-retract
//     skips the retraction (pre-seeding rules before first collection).
//   - The stored value is canonicalized (lowercase, no trailing dot,
//     punycode, masked CIDR, netip IPs) so the gate, the DB backstop and
//     the retraction all match the same bytes.
//   - Suppression applies from the NEXT statement on: ingestion paths
//     re-compile the rule set per call (fail-closed).
//   - `remove` does NOT resurrect retracted records; the audit surface
//     (suppression_applications) survives rule removal.
//
// Every DB-touching path is self-hosted-profile only (the client profile
// has no database) and honors the global --config/--profile flags.

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/Lutfifakee-Project/gseer/internal/config"
	"github.com/Lutfifakee-Project/gseer/internal/storage/postgres"
	"github.com/Lutfifakee-Project/gseer/internal/suppress"
)

// registerSuppressCmd attaches `gseer suppress` with add/list/remove.
func registerSuppressCmd(root *cobra.Command) {
	sup := &cobra.Command{
		Use:   "suppress",
		Short: "Manage the suppression policy (exact IP/CIDR/domain/suffix/source rules)",
		Args:  cobra.NoArgs,
		Long: `Manage the centralized suppression policy layer.

A suppression rule removes classes of records from GSEER's data plane:
records are refused BEFORE ingestion (and the database itself refuses
them if any write path forgets the gate), and adding a rule retracts
matching records that are already indexed.

Rule kinds:
  ip            exact IP address (IPv4 + IPv6, canonical form)
  cidr          network range (host bits masked; containment match)
  domain        exact domain or hostname (IDNA/case/trailing-dot canonical)
  domain_suffix the name + every descendant at a strict label boundary
                (sub.example.com matches example.com; notexample.com does not)
  source        a registered data source name: everything it delivers
                is refused

Examples:
  gseer suppress add ip 198.51.100.4 --reason "operator-owned host"
  gseer suppress add cidr 10.0.0.0/8 --reason "internal space"
  gseer suppress add domain_suffix internal.example --reason "our own domains"
  gseer suppress add source shady-feed --reason "untrustworthy upstream"`,
	}
	sup.AddCommand(newSuppressAddCmd(), newSuppressListCmd(), newSuppressRemoveCmd())
	root.AddCommand(sup)
}

func newSuppressAddCmd() *cobra.Command {
	var (
		reason    string
		by        string
		expires   string
		noRetract bool
		jsonOut   bool
	)
	cmd := &cobra.Command{
		Use:   "add <kind> <value>",
		Short: "Add a suppression rule and retract matching indexed records",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := suppress.Kind(args[0])
			value := args[1]
			if reason == "" {
				return fmt.Errorf("E_SUPPRESS_REASON: a suppression rule requires --reason.\n" +
					"Why: every rule must carry its justification for the audit surface.\n" +
					"Try: gseer suppress add ip 198.51.100.4 --reason \"operator-owned host\"")
			}
			if by == "" {
				by = "cli"
			}
			in := postgres.SuppressInput{Kind: kind, Value: value, Reason: reason, CreatedBy: by}
			if expires != "" {
				exp, err := parseSuppressExpiry(expires, time.Now())
				if err != nil {
					return err
				}
				in.ExpiresAt = &exp
			}
			cfg, err := loadEngineConfig()
			if err != nil {
				return err
			}
			if cfg.Profile == config.ProfilePersonal {
				return runPersonalSuppressAdd(cmd, kind, value, reason, by, expires, noRetract, jsonOut)
			}
			pool, err := openAppPool(cmd, cfg)
			if err != nil {
				return err
			}
			defer pool.Close()
			store := postgres.NewStore(pool)

			rule, existed, err := store.AddSuppression(cmd.Context(), in)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				if existed {
					return fmt.Errorf("E_SUPPRESS_EXISTS: rule #%d (%s:%s) already exists", rule.ID, rule.Kind, rule.Value)
				}
				_, _ = fmt.Fprintf(out, "{\"id\":%d,\"kind\":%q,\"value\":%q,\"reason\":%q,\"expires_at\":%q}\n",
					rule.ID, rule.Kind, rule.Value, rule.Reason, expires)
				return nil
			}
			if existed {
				printf(out, "Rule already exists: #%d (%s: %s) — nothing changed.\n", rule.ID, rule.Kind, rule.Value)
				return nil
			}
			printf(out, "[ ok  ] suppression rule #%d added: %s = %s\n", rule.ID, rule.Kind, rule.Value)
			if in.ExpiresAt != nil {
				printf(out, "        expires: %s\n", in.ExpiresAt.UTC().Format(time.RFC3339))
			}
			if noRetract {
				println(out, "        retraction skipped (--no-retract); matching records stay until the next retraction")
				return nil
			}
			stats, err := store.RetractSuppressed(cmd.Context(), rule)
			if err != nil {
				return fmt.Errorf("E_SUPPRESS_RETRACT: the rule is stored but the retroactive retraction failed.\n"+
					"Why: %v\n"+
					"Try: the rule is already enforcing on ingestion; retry the retraction by re-adding the rule (idempotent)", err)
			}
			printf(out, "[ ok  ] retroactive retraction: %d assets removed, %d services, %d observations (audit rows: %d)\n",
				stats.AssetsRetracted, stats.ServicesRemoved, stats.ObservationsRemoved, stats.AuditRows)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why the rule exists (required, audit metadata)")
	cmd.Flags().StringVar(&by, "by", "", "author identity recorded on the rule (default: cli)")
	cmd.Flags().StringVar(&expires, "expires", "", "expiry: RFC 3339 timestamp or Go duration (e.g. 720h)")
	cmd.Flags().BoolVar(&noRetract, "no-retract", false, "store the rule without retracting already-indexed records")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	// reason is validated in RunE (not MarkFlagRequired) so the refusal
	// carries the stable E_SUPPRESS_REASON code and the audit rationale.
	return cmd
}

// parseSuppressExpiry accepts an RFC 3339 timestamp or a Go duration.
func parseSuppressExpiry(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		if !t.After(now) {
			return time.Time{}, fmt.Errorf("E_SUPPRESS_EXPIRY: %s is in the past; an expired rule never matches", s)
		}
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("E_SUPPRESS_EXPIRY: duration %s must be positive", s)
		}
		return now.Add(d), nil
	}
	return time.Time{}, fmt.Errorf("E_SUPPRESS_EXPIRY: %q is neither an RFC 3339 timestamp nor a duration.\n"+
		"Try: --expires 2030-01-01T00:00:00Z  or  --expires 2160h", s)
}

func newSuppressListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every suppression rule (including disabled and expired)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadEngineConfig()
			if err != nil {
				return err
			}
			if cfg.Profile == config.ProfilePersonal {
				return runPersonalSuppressList(cmd)
			}
			pool, err := openAppPool(cmd, cfg)
			if err != nil {
				return err
			}
			defer pool.Close()
			store := postgres.NewStore(pool)
			rules, err := store.ListSuppressions(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(rules) == 0 {
				println(out, "No suppression rules. Add one with: gseer suppress add <kind> <value> --reason \"...\"")
				return nil
			}
			printf(out, "%-6s %-14s %-40s %-10s %-22s %s\n", "ID", "KIND", "VALUE", "ENABLED", "EXPIRES", "REASON")
			for _, r := range rules {
				exp := "never"
				if r.ExpiresAt != nil {
					exp = r.ExpiresAt.UTC().Format(time.RFC3339)
				}
				printf(out, "%-6d %-14s %-40s %-10v %-22s %s\n", r.ID, r.Kind, r.Value, r.Enabled, exp, r.Reason)
			}
			return nil
		},
	}
}

func newSuppressRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "Remove a suppression rule by id (audit history is kept)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var id int64
			if _, err := fmt.Sscanf(args[0], "%d", &id); err != nil || id <= 0 {
				return fmt.Errorf("E_SUPPRESS_ID: %q is not a positive rule id (see gseer suppress list)", args[0])
			}
			cfg, err := loadEngineConfig()
			if err != nil {
				return err
			}
			if cfg.Profile == config.ProfilePersonal {
				return runPersonalSuppressRemove(cmd, id)
			}
			pool, err := openAppPool(cmd, cfg)
			if err != nil {
				return err
			}
			defer pool.Close()
			store := postgres.NewStore(pool)
			ok, err := store.RemoveSuppression(cmd.Context(), id)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("E_SUPPRESS_ID: rule #%d does not exist (see gseer suppress list)", id)
			}
			printf(cmd.OutOrStdout(), "[ ok  ] suppression rule #%d removed.  Its audit history is kept; retracted records are NOT resurrected.\n", id)
			return nil
		},
	}
}

// openAppPool opens the application pool a runtime data-plane command
// uses (runtime data plane: suppress, source, collect, export) after
// printing the redacted reachability line.  It prefers the
// least-privilege gseer_app runtime credential and falls back to the
// operator credential with an explicit notice (runtimeCommandDSN).
func openAppPool(cmd *cobra.Command, cfg *config.Config) (*pgxpool.Pool, error) {
	dsn := runtimeCommandDSN(cmd.OutOrStdout(), cfg)
	if err := postgres.CheckReachable(cmd.Context(), dsn); err != nil {
		return nil, err
	}
	printf(cmd.OutOrStdout(), "[ ok  ] DB reachable at %s\n", postgres.MustRedactDSN(dsn))
	pool, err := postgres.NewPool(cmd.Context(), dsn)
	if err != nil {
		return nil, err
	}
	return pool, nil
}
