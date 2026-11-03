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

package search

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
)

// row.go — the result row shape and the projection-aware scanner.
//
// The planner lets callers project a SUBSET of the search_index columns,
// so the scanner binds targets BY COLUMN NAME from the result's field
// descriptions instead of positional scanning.

// Row is one search_index row (the v0.0.1 service-granular result).
// JSON tags are the API wire shape (SPEC-07).
type Row struct {
	RowID   int64  `json:"row_id"`
	RowKind string `json:"row_kind"` // service | asset-only
	AssetID int64  `json:"asset_id"`

	AssetKind  string `json:"asset_kind"` // ip | domain | hostname
	AssetValue string `json:"asset_value"`

	IP       *string `json:"ip,omitempty"`
	Domain   *string `json:"domain,omitempty"`
	Hostname *string `json:"hostname,omitempty"`

	ASN    *int    `json:"asn,omitempty"`
	ASNOrg *string `json:"asn_org,omitempty"`

	GeoCountry *string `json:"geo_country,omitempty"`
	GeoRegion  *string `json:"geo_region,omitempty"`
	GeoCity    *string `json:"geo_city,omitempty"`

	Port      *int    `json:"port,omitempty"`
	Transport *string `json:"transport,omitempty"`
	Protocol  *string `json:"protocol,omitempty"`
	Name      *string `json:"name,omitempty"`
	Product   *string `json:"product,omitempty"`
	Version   *string `json:"version,omitempty"`

	HTTPStatus *int    `json:"http_status,omitempty"`
	HTTPTitle  *string `json:"http_title,omitempty"`
	HTTPServer *string `json:"http_server,omitempty"`

	TLSVersion       *string    `json:"tls_version,omitempty"`
	TLSCertSubjectCN *string    `json:"tls_cert_subject_cn,omitempty"`
	TLSCertIssuerCN  *string    `json:"tls_cert_issuer_cn,omitempty"`
	TLSCertExpiry    *time.Time `json:"tls_cert_expiry,omitempty"`

	Technologies []string `json:"technologies,omitempty"`
	Sources      []string `json:"sources,omitempty"`

	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Stale     bool      `json:"stale"`

	// ipPrefix is the raw inet scan target (PG type inet cannot scan
	// into *string); after Scan it reduces to the IP string form.
	ipPrefix *netip.Prefix `json:"-"`
}

// sortValue renders the row's value for the given canonical sort field
// (the cursor payload form): stringified, with isNull=true when the
// column was SQL NULL.
func (r *Row) sortValue(field string) (string, bool) {
	switch field {
	case "port":
		if r.Port == nil {
			return "", true
		}
		return fmt.Sprintf("%d", *r.Port), false
	case "asn":
		if r.ASN == nil {
			return "", true
		}
		return fmt.Sprintf("%d", *r.ASN), false
	case "http.status":
		if r.HTTPStatus == nil {
			return "", true
		}
		return fmt.Sprintf("%d", *r.HTTPStatus), false
	case "asset_value":
		return r.AssetValue, false
	case "asset_kind":
		return r.AssetKind, false
	case "ip":
		if r.IP == nil {
			return "", true
		}
		return *r.IP, false
	case "domain":
		if r.Domain == nil {
			return "", true
		}
		return *r.Domain, false
	case "hostname":
		if r.Hostname == nil {
			return "", true
		}
		return *r.Hostname, false
	case "asn_org":
		if r.ASNOrg == nil {
			return "", true
		}
		return *r.ASNOrg, false
	case "geo_country":
		if r.GeoCountry == nil {
			return "", true
		}
		return *r.GeoCountry, false
	case "geo_region":
		if r.GeoRegion == nil {
			return "", true
		}
		return *r.GeoRegion, false
	case "geo_city":
		if r.GeoCity == nil {
			return "", true
		}
		return *r.GeoCity, false
	case "transport":
		if r.Transport == nil {
			return "", true
		}
		return *r.Transport, false
	case "protocol":
		if r.Protocol == nil {
			return "", true
		}
		return *r.Protocol, false
	case "name":
		if r.Name == nil {
			return "", true
		}
		return *r.Name, false
	case "product":
		if r.Product == nil {
			return "", true
		}
		return *r.Product, false
	case "version":
		if r.Version == nil {
			return "", true
		}
		return *r.Version, false
	case "http.title":
		if r.HTTPTitle == nil {
			return "", true
		}
		return *r.HTTPTitle, false
	case "http.server":
		if r.HTTPServer == nil {
			return "", true
		}
		return *r.HTTPServer, false
	case "tls.version":
		if r.TLSVersion == nil {
			return "", true
		}
		return *r.TLSVersion, false
	case "tls.cert_subject_cn":
		if r.TLSCertSubjectCN == nil {
			return "", true
		}
		return *r.TLSCertSubjectCN, false
	case "tls.cert_issuer_cn":
		if r.TLSCertIssuerCN == nil {
			return "", true
		}
		return *r.TLSCertIssuerCN, false
	case "first_seen", "last_seen", "tls.cert_expiry":
		var t time.Time
		switch field {
		case "first_seen":
			t = r.FirstSeen
		case "last_seen":
			t = r.LastSeen
		case "tls.cert_expiry":
			if r.TLSCertExpiry == nil {
				return "", true
			}
			t = *r.TLSCertExpiry
		}
		return t.UTC().Format(time.RFC3339Nano), false
	}
	return "", true
}

// scanRow binds one result row to a Row by column name.
func scanRow(rows pgx.Rows) (Row, error) {
	var r Row
	fields := rows.FieldDescriptions()
	ptrs := make([]any, len(fields))

	for i, f := range fields {
		var target any
		switch f.Name {
		case "row_id":
			target = &r.RowID
		case "row_kind":
			target = &r.RowKind
		case "asset_id":
			target = &r.AssetID
		case "asset_kind":
			target = &r.AssetKind
		case "asset_value":
			target = &r.AssetValue
		case "ip":
			// ip is PG type inet; scan through netip.Prefix and reduce a
			// single-host prefix (/32, /128) to the bare address string.
			target = &r.ipPrefix
		case "domain":
			target = &r.Domain
		case "hostname":
			target = &r.Hostname
		case "asn":
			target = &r.ASN
		case "asn_org":
			target = &r.ASNOrg
		case "geo_country":
			target = &r.GeoCountry
		case "geo_region":
			target = &r.GeoRegion
		case "geo_city":
			target = &r.GeoCity
		case "port":
			target = &r.Port
		case "transport":
			target = &r.Transport
		case "protocol":
			target = &r.Protocol
		case "name":
			target = &r.Name
		case "product":
			target = &r.Product
		case "version":
			target = &r.Version
		case "http_status":
			target = &r.HTTPStatus
		case "http_title":
			target = &r.HTTPTitle
		case "http_server":
			target = &r.HTTPServer
		case "tls_version":
			target = &r.TLSVersion
		case "cert_subject_cn":
			target = &r.TLSCertSubjectCN
		case "cert_issuer_cn":
			target = &r.TLSCertIssuerCN
		case "cert_expiry":
			target = &r.TLSCertExpiry
		case "technologies":
			target = &r.Technologies
		case "sources":
			target = &r.Sources
		case "first_seen":
			target = &r.FirstSeen
		case "last_seen":
			target = &r.LastSeen
		case "stale":
			target = &r.Stale
		default:
			// Unknown projected column — scan into a discard buffer so
			// the row stays consumable.
			var discard any
			target = &discard
		}
		ptrs[i] = target
	}
	if err := rows.Scan(ptrs...); err != nil {
		return Row{}, err
	}
	if r.ipPrefix != nil {
		s := formatPrefix(*r.ipPrefix)
		r.IP = &s
	}
	return r, nil
}

// formatPrefix renders an inet value: single-host prefixes collapse to
// the bare address (203.0.113.10/32 → 203.0.113.10).
func formatPrefix(p netip.Prefix) string {
	if (p.Addr().Is4() && p.Bits() == 32) || (p.Addr().Is6() && p.Bits() == 128) {
		return p.Addr().String()
	}
	return p.String()
}

// jsonIndent is a tiny helper for surfaces that want pretty JSON.
func jsonIndent(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
