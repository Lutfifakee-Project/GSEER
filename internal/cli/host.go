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

// host.go — `gseer host <ip|hostname|domain>` (v0.2.0): the read-only
// host-inspection surface.  One identifier in, the asset's full picture
// out: identity/geo/ASN, every service with its latest HTTP/TLS/
// fingerprint state, and the observation provenance breakdown.
//
// Strictly read-only (SELECT on assets/services/observations and the
// latest_* views — all part of the §5.2 runtime grant matrix).  It runs
// on the SAME pool as the other runtime data-plane commands (the
// least-privilege gseer_app credential when configured).  Search ≠ Scan
// is untouched: nothing here contacts the network; it only reads what
// was already collected.

import (
	"context"
	"encoding/json"
	"errors"

	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/Lutfifakee-Project/GSEER/internal/config"
)

// registerHostCmd attaches `gseer host`.
func registerHostCmd(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "host <ip|hostname|domain>",
		Short: "Show everything GSEER knows about one host (read-only)",
		Long: `Show everything GSEER knows about one host.

Reads the local index for the asset matching the identifier (matched
against asset value, ip, hostname, and domain), then prints:

  - the asset row: kind, value, ASN/org, geo, sources, first/last seen
  - every service: port/transport/protocol/product/version plus the
    latest HTTP (status/title/server), TLS, and fingerprint state
  - observation provenance: how many observations, from which sources

Strictly read-only and self-hosted: no network contact happens beyond
the database connection itself — ` + "`gseer host`" + ` shows exactly what your
collectors already put in the index, nothing else.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE:              runHost,
	}
	root.AddCommand(cmd)
}

// hostAsset is the JSON/table shape of the asset row.
type hostAsset struct {
	ID        int64     `json:"-"`
	Kind      string    `json:"kind"`
	Value     string    `json:"value"`
	IP        string    `json:"ip,omitempty"`
	Domain    string    `json:"domain,omitempty"`
	Hostname  string    `json:"hostname,omitempty"`
	ASN       *int      `json:"asn,omitempty"`
	ASNOrg    string    `json:"asn_org,omitempty"`
	Country   string    `json:"geo_country,omitempty"`
	Region    string    `json:"geo_region,omitempty"`
	City      string    `json:"geo_city,omitempty"`
	Sources   []string  `json:"sources"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Stale     bool      `json:"stale"`
}

// hostService is one service with its latest-observation state.
type hostService struct {
	ID         int64     `json:"id"`
	Port       int       `json:"port"`
	Transport  string    `json:"transport"`
	Protocol   string    `json:"protocol"`
	Name       string    `json:"name"`
	Product    string    `json:"product"`
	Version    string    `json:"version"`
	LastSeen   time.Time `json:"last_seen"`
	Stale      bool      `json:"stale"`
	HTTPStatus *int      `json:"http_status,omitempty"`
	HTTPTitle  string    `json:"http_title,omitempty"`
	HTTPServer string    `json:"http_server,omitempty"`
	TLSVersion string    `json:"tls_version,omitempty"`
	Tech       []string  `json:"technologies,omitempty"`
}

// hostObservationStats is the provenance breakdown for the asset.
type hostObservationStats struct {
	Total      int64            `json:"total"`
	BySource   map[string]int64 `json:"by_source"`
	LastObtain *time.Time       `json:"last_observed,omitempty"`
}

// hostReport bundles everything one `gseer host` prints.
type hostReport struct {
	Asset        hostAsset            `json:"asset"`
	Services     []hostService        `json:"services"`
	Observations hostObservationStats `json:"observations"`
}

func runHost(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	identifier := strings.TrimSpace(args[0])

	cfg, cerr := config.Load(config.Flags{
		ConfigPath: flags.configPath,
		Profile:    flags.profile,
		Endpoint:   flags.server,
	})
	if cerr != nil {
		printf(out, "E_CONFIG: %s\nTry: gseer setup --help\n", cerr.Error())
		exitWith(ExitConfig)
	}
	if cfg.Profile == config.ProfilePersonal {
		printf(out, "E_PROFILE_UNSUPPORTED: `gseer host` is not wired for personal mode yet\n"+
			"Why: the host report runs through the PostgreSQL service; personal mode (SQLite) covers search, export, suppression, and backup in v0.2.x\n"+
			"Try: gseer search '%s'   (the same identifiers are searchable in personal mode)\n", identifier)
		exitWith(ExitConfig)
		return nil
	}
	if cfg.Profile != config.ProfileSelfHosted {
		printf(out, "E_CLIENT_ONLY: `gseer host` inspects the LOCAL index (self-hosted profile)\n"+
			"Why: the client profile holds no data; use the server's dashboard/API against a remote instance\n"+
			"Try: gseer --profile self-hosted host %s\n", identifier)
		exitWith(ExitConfig)
	}

	pool, err := openAppPool(cmd, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	rep, found, herr := loadHostReport(cmd.Context(), pool, identifier)
	if herr != nil {
		printf(out, "E_DB: %s\n", herr.Error())
		exitWith(ExitUnavailable)
		return nil
	}
	if !found {
		printf(out, "No asset matches %q in the local index.\n"+
			"Try: gseer search '%s'   (find the exact identifier first; a fresh install has an empty index)\n",
			identifier, identifier)
		return nil
	}

	if flags.output == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	renderHostText(out, rep)
	return nil
}

// loadHostReport runs the three read-only queries behind `gseer host`.
func loadHostReport(ctx context.Context, pool *pgxpool.Pool, identifier string) (*hostReport, bool, error) {
	rep := &hostReport{Observations: hostObservationStats{BySource: map[string]int64{}}}

	// 1. The asset (value/ip/hostname/domain; ambiguous matches resolve
	//    to the oldest row — identity (kind, value) is unique, the
	//    mirror columns can collide across kinds).
	var sources []string
	err := pool.QueryRow(ctx, `
SELECT id, kind, value,
       COALESCE(ip::text, ''), COALESCE(domain, ''), COALESCE(hostname, ''),
       asn, COALESCE(asn_org, ''),
       COALESCE(geo_country, ''), COALESCE(geo_region, ''), COALESCE(geo_city, ''),
       sources, first_seen, last_seen, stale
FROM assets
WHERE value = $1 OR ip::text = $1 OR hostname = $1 OR domain = $1
ORDER BY id
LIMIT 1`, identifier).
		Scan(&rep.Asset.ID, &rep.Asset.Kind, &rep.Asset.Value,
			&rep.Asset.IP, &rep.Asset.Domain, &rep.Asset.Hostname,
			&rep.Asset.ASN, &rep.Asset.ASNOrg,
			&rep.Asset.Country, &rep.Asset.Region, &rep.Asset.City,
			&sources, &rep.Asset.FirstSeen, &rep.Asset.LastSeen, &rep.Asset.Stale)
	if err != nil {
		if isNoRows(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	rep.Asset.Sources = sources
	if rep.Asset.Sources == nil {
		rep.Asset.Sources = []string{}
	}

	// 2. Services with their latest HTTP/TLS/fingerprint state.
	rows, err := pool.Query(ctx, `
SELECT s.id, s.port, s.transport, s.protocol, s.name, s.product, s.version,
       s.last_seen, s.stale,
       h.status, COALESCE(h.title, ''), COALESCE(h.server, ''),
       COALESCE(tls.tls_version, ''),
       COALESCE(fp.technologies, '{}')
FROM services s
LEFT JOIN latest_http h         ON h.service_id = s.id
LEFT JOIN latest_tls tls        ON tls.service_id = s.id
LEFT JOIN latest_fingerprints fp ON fp.service_id = s.id
WHERE s.asset_id = $1
ORDER BY s.port, s.transport`, rep.Asset.ID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var s hostService
		if err := rows.Scan(&s.ID, &s.Port, &s.Transport, &s.Protocol, &s.Name, &s.Product, &s.Version,
			&s.LastSeen, &s.Stale,
			&s.HTTPStatus, &s.HTTPTitle, &s.HTTPServer,
			&s.TLSVersion, &s.Tech); err != nil {
			return nil, false, err
		}
		if s.Tech == nil {
			s.Tech = []string{}
		}
		rep.Services = append(rep.Services, s)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if rep.Services == nil {
		rep.Services = []hostService{}
	}

	// 3. Observation provenance breakdown.
	orows, err := pool.Query(ctx, `
SELECT COALESCE(ds.name, format('source#%s', o.source_id::text)) AS src,
       count(*), max(o.observed_at)
FROM observations o
LEFT JOIN data_sources ds ON ds.id = o.source_id
WHERE o.asset_id = $1
GROUP BY 1
ORDER BY 2 DESC`, rep.Asset.ID)
	if err != nil {
		return nil, false, err
	}
	defer orows.Close()
	for orows.Next() {
		var src string
		var n int64
		var last time.Time
		if err := orows.Scan(&src, &n, &last); err != nil {
			return nil, false, err
		}
		rep.Observations.BySource[src] = n
		rep.Observations.Total += n
		if rep.Observations.LastObtain == nil || last.After(*rep.Observations.LastObtain) {
			t := last
			rep.Observations.LastObtain = &t
		}
	}
	if err := orows.Err(); err != nil {
		return nil, false, err
	}

	return rep, true, nil
}

// isNoRows reports a pgx empty single-row result.
func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// renderHostText prints the human-readable report.
func renderHostText(out io.Writer, rep *hostReport) {
	a := rep.Asset
	printf(out, "HOST %s\n", a.Value)
	printf(out, "  kind: %s", a.Kind)
	if a.IP != "" {
		printf(out, "  ip: %s", a.IP)
	}
	if a.Hostname != "" {
		printf(out, "  hostname: %s", a.Hostname)
	}
	if a.Domain != "" {
		printf(out, "  domain: %s", a.Domain)
	}
	printf(out, "\n")
	if a.ASN != nil {
		printf(out, "  asn: %d (%s)\n", *a.ASN, a.ASNOrg)
	}
	if a.Country != "" || a.Region != "" || a.City != "" {
		printf(out, "  geo: %s / %s / %s\n", a.Country, a.Region, a.City)
	}
	printf(out, "  sources: %s\n", strings.Join(a.Sources, ", "))
	printf(out, "  first_seen: %s · last_seen: %s · stale: %v\n",
		a.FirstSeen.UTC().Format("2006-01-02 15:04:05Z"), a.LastSeen.UTC().Format("2006-01-02 15:04:05Z"), a.Stale)

	printf(out, "\nSERVICES (%d)\n", len(rep.Services))
	if len(rep.Services) == 0 {
		printf(out, "  (none — passive facts are asset-level; service rows need evidence that carries them)\n")
	}
	for _, s := range rep.Services {
		renderHostService(out, s)
	}

	printf(out, "\nOBSERVATIONS: %d total", rep.Observations.Total)
	if rep.Observations.LastObtain != nil {
		printf(out, " · last observed %s", rep.Observations.LastObtain.UTC().Format("2006-01-02 15:04:05Z"))
	}
	printf(out, "\n")
	srcs := sortedHostSources(rep.Observations.BySource)
	for _, src := range srcs {
		printf(out, "  %s: %d\n", src, rep.Observations.BySource[src])
	}
	if len(srcs) == 0 {
		printf(out, "  (no observations reference this asset)\n")
	}
	printf(out, "\nRead-only view of the local index — gseer search for cross-host queries.\n")
}

// renderHostService prints one service row of the HOST report.  Extracted
// from renderHostText to keep that function under the gocyclo gate; the
// bytes printed are byte-identical to the inline original.
func renderHostService(out io.Writer, s hostService) {
	printf(out, "  %d/%s %s", s.Port, s.Transport, s.Protocol)
	if s.Product != "" {
		printf(out, " · %s", s.Product)
		if s.Version != "" {
			printf(out, " %s", s.Version)
		}
	} else if s.Name != "" {
		printf(out, " · %s", s.Name)
	}
	if s.HTTPStatus != nil {
		printf(out, " · http %d", *s.HTTPStatus)
		if s.HTTPTitle != "" {
			printf(out, " %q", s.HTTPTitle)
		}
		if s.HTTPServer != "" {
			printf(out, " (%s)", s.HTTPServer)
		}
	}
	if s.TLSVersion != "" {
		printf(out, " · tls %s", s.TLSVersion)
	}
	if len(s.Tech) > 0 {
		printf(out, " · tech: %s", strings.Join(s.Tech, ", "))
	}
	if s.Stale {
		printf(out, " · STALE")
	}
	printf(out, "\n")
}

// sortedHostSources returns the provenance names, count-desc then name.
func sortedHostSources(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a, b := out[j-1], out[j]
			if m[b] > m[a] || (m[b] == m[a] && b < a) {
				out[j-1], out[j] = b, a
			} else {
				break
			}
		}
	}
	return out
}
