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
//
// The per-field dispatch is table-driven: stringSortFields / intSortFields
// cover the pointer columns, the switch below the nullable time columns
// and the two never-null text columns.
var stringSortFields = map[string]func(*Row) *string{
	"ip":                  func(r *Row) *string { return r.IP },
	"domain":              func(r *Row) *string { return r.Domain },
	"hostname":            func(r *Row) *string { return r.Hostname },
	"asn_org":             func(r *Row) *string { return r.ASNOrg },
	"geo_country":         func(r *Row) *string { return r.GeoCountry },
	"geo_region":          func(r *Row) *string { return r.GeoRegion },
	"geo_city":            func(r *Row) *string { return r.GeoCity },
	"transport":           func(r *Row) *string { return r.Transport },
	"protocol":            func(r *Row) *string { return r.Protocol },
	"name":                func(r *Row) *string { return r.Name },
	"product":             func(r *Row) *string { return r.Product },
	"version":             func(r *Row) *string { return r.Version },
	"http.title":          func(r *Row) *string { return r.HTTPTitle },
	"http.server":         func(r *Row) *string { return r.HTTPServer },
	"tls.version":         func(r *Row) *string { return r.TLSVersion },
	"tls.cert_subject_cn": func(r *Row) *string { return r.TLSCertSubjectCN },
	"tls.cert_issuer_cn":  func(r *Row) *string { return r.TLSCertIssuerCN },
}

var intSortFields = map[string]func(*Row) *int{
	"port":        func(r *Row) *int { return r.Port },
	"asn":         func(r *Row) *int { return r.ASN },
	"http.status": func(r *Row) *int { return r.HTTPStatus },
}

func (r *Row) sortValue(field string) (string, bool) {
	if get, ok := stringSortFields[field]; ok {
		if p := get(r); p != nil {
			return *p, false
		}
		return "", true
	}
	if get, ok := intSortFields[field]; ok {
		if p := get(r); p != nil {
			return fmt.Sprintf("%d", *p), false
		}
		return "", true
	}
	switch field {
	case "asset_value":
		return r.AssetValue, false
	case "asset_kind":
		return r.AssetKind, false
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
//
// The column → target dispatch is the rowScanTargets table (the planner
// owns the projection list; unknown columns fall through to a discard
// buffer so the row stays consumable).
var rowScanTargets = map[string]func(*Row) any{
	"row_id":      func(r *Row) any { return &r.RowID },
	"row_kind":    func(r *Row) any { return &r.RowKind },
	"asset_id":    func(r *Row) any { return &r.AssetID },
	"asset_kind":  func(r *Row) any { return &r.AssetKind },
	"asset_value": func(r *Row) any { return &r.AssetValue },
	// ip is PG type inet; scan through netip.Prefix and reduce a
	// single-host prefix (/32, /128) to the bare address string.
	"ip":              func(r *Row) any { return &r.ipPrefix },
	"domain":          func(r *Row) any { return &r.Domain },
	"hostname":        func(r *Row) any { return &r.Hostname },
	"asn":             func(r *Row) any { return &r.ASN },
	"asn_org":         func(r *Row) any { return &r.ASNOrg },
	"geo_country":     func(r *Row) any { return &r.GeoCountry },
	"geo_region":      func(r *Row) any { return &r.GeoRegion },
	"geo_city":        func(r *Row) any { return &r.GeoCity },
	"port":            func(r *Row) any { return &r.Port },
	"transport":       func(r *Row) any { return &r.Transport },
	"protocol":        func(r *Row) any { return &r.Protocol },
	"name":            func(r *Row) any { return &r.Name },
	"product":         func(r *Row) any { return &r.Product },
	"version":         func(r *Row) any { return &r.Version },
	"http_status":     func(r *Row) any { return &r.HTTPStatus },
	"http_title":      func(r *Row) any { return &r.HTTPTitle },
	"http_server":     func(r *Row) any { return &r.HTTPServer },
	"tls_version":     func(r *Row) any { return &r.TLSVersion },
	"cert_subject_cn": func(r *Row) any { return &r.TLSCertSubjectCN },
	"cert_issuer_cn":  func(r *Row) any { return &r.TLSCertIssuerCN },
	"cert_expiry":     func(r *Row) any { return &r.TLSCertExpiry },
	"technologies":    func(r *Row) any { return &r.Technologies },
	"sources":         func(r *Row) any { return &r.Sources },
	"first_seen":      func(r *Row) any { return &r.FirstSeen },
	"last_seen":       func(r *Row) any { return &r.LastSeen },
	"stale":           func(r *Row) any { return &r.Stale },
}

func scanRow(rows pgx.Rows) (Row, error) {
	var r Row
	fields := rows.FieldDescriptions()
	ptrs := make([]any, len(fields))

	for i, f := range fields {
		if get, ok := rowScanTargets[f.Name]; ok {
			ptrs[i] = get(&r)
			continue
		}
		// Unknown projected column — scan into a discard buffer so
		// the row stays consumable.
		var discard any
		ptrs[i] = &discard
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
