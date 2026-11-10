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

package searchlite

// scan.go — the projection-aware row scanner for database/sql results
// (the personal-mode counterpart of search.scanRow over pgx.Rows).
//
// Columns bind BY NAME from the result's columns (the planner can
// project a subset); unknown projected columns fall through to a
// discard buffer.  Timestamp columns arrive as the storage layer's
// fixed-width UTC strings (SQ-02) and parse through the shared parser.
// The ip column stores the canonical address text, so it scans directly
// into *string.  List columns (technologies/sources) are JSON arrays.

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/search"
)

// parseTS parses the storage timestamp format (fixed-width UTC micro
// strings) plus the coarser operator-facing shapes.
func parseTS(s string) (time.Time, error) {
	for _, layout := range []string{
		"2006-01-02T15:04:05.000000Z07:00", // the SQ-02 storage format
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, &time.ParseError{Layout: "RFC3339", Value: s}
}

// jsonStrings decodes a JSON array-of-strings column (nil when NULL or
// empty).
func jsonStrings(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		return decodeStrings(string(t))
	case string:
		return decodeStrings(t)
	default:
		return nil
	}
}

func decodeStrings(s string) []string {
	if s == "" || s == "[]" || s == "null" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// scanCell holds one column's scan target and its post-processing.
type scanCell struct {
	kind string // dispatch key
	// one of the scan slots (bound directly), or the raw slot.
	str    sql.NullString
	num    sql.NullInt64
	raw    any
	finish func(r *search.Row) // deferred decode (nil when bound directly)
}

// scanRow binds one result row to a search.Row by column name.
func scanRow(rows *sql.Rows) (search.Row, error) {
	var r search.Row
	cols, err := rows.Columns()
	if err != nil {
		return r, err
	}
	cells := make([]scanCell, len(cols))
	ptrs := make([]any, len(cols))
	for i, c := range cols {
		cells[i] = bindCell(&r, c)
		ptrs[i] = cells[i].scanTarget()
	}
	if err := rows.Scan(ptrs...); err != nil {
		return r, err
	}
	for i := range cells {
		cells[i].apply(&r)
	}
	return r, nil
}

// bindCell picks the scan slot for one projected column.
func bindCell(r *search.Row, col string) scanCell {
	switch col {
	case "row_id":
		return scanCell{raw: &r.RowID}
	case "row_kind":
		return scanCell{raw: &r.RowKind}
	case "asset_id":
		return scanCell{raw: &r.AssetID}
	case "asset_kind":
		return scanCell{raw: &r.AssetKind}
	case "asset_value":
		return scanCell{raw: &r.AssetValue}
	case "ip":
		return scanCell{kind: "ip", finish: func(r *search.Row) {}}
	case "domain":
		return scanCell{raw: &r.Domain}
	case "hostname":
		return scanCell{raw: &r.Hostname}
	case "asn":
		return scanCell{kind: "asn", finish: func(r *search.Row) {}}
	case "asn_org":
		return scanCell{raw: &r.ASNOrg}
	case "geo_country":
		return scanCell{raw: &r.GeoCountry}
	case "geo_region":
		return scanCell{raw: &r.GeoRegion}
	case "geo_city":
		return scanCell{raw: &r.GeoCity}
	case "port":
		return scanCell{kind: "port", finish: func(r *search.Row) {}}
	case "transport":
		return scanCell{raw: &r.Transport}
	case "protocol":
		return scanCell{raw: &r.Protocol}
	case "name":
		return scanCell{raw: &r.Name}
	case "product":
		return scanCell{raw: &r.Product}
	case "version":
		return scanCell{raw: &r.Version}
	case "http_status":
		return scanCell{kind: "http_status", finish: func(r *search.Row) {}}
	case "http_title":
		return scanCell{raw: &r.HTTPTitle}
	case "http_server":
		return scanCell{raw: &r.HTTPServer}
	case "tls_version":
		return scanCell{raw: &r.TLSVersion}
	case "cert_subject_cn":
		return scanCell{raw: &r.TLSCertSubjectCN}
	case "cert_issuer_cn":
		return scanCell{raw: &r.TLSCertIssuerCN}
	case "cert_expiry":
		return scanCell{kind: "cert_expiry", finish: func(r *search.Row) {}}
	case "technologies":
		return scanCell{kind: "technologies", raw: new(any), finish: func(r *search.Row) {}}
	case "sources":
		return scanCell{kind: "sources", raw: new(any), finish: func(r *search.Row) {}}
	case "first_seen":
		return scanCell{kind: "first_seen", finish: func(r *search.Row) {}}
	case "last_seen":
		return scanCell{kind: "last_seen", finish: func(r *search.Row) {}}
	case "stale":
		return scanCell{kind: "stale", finish: func(r *search.Row) {}}
	default:
		var discard any
		return scanCell{raw: &discard}
	}
}

// numericKinds scan into the integer slot.
var numericKinds = map[string]bool{"port": true, "asn": true, "http_status": true, "stale": true}

// scanTarget returns the sql.Scan destination.
func (c *scanCell) scanTarget() any {
	if c.raw != nil {
		return c.raw
	}
	if numericKinds[c.kind] {
		return &c.num
	}
	return &c.str
}

// apply copies the scanned cell into the Row (deferred decodes).
func (c *scanCell) apply(r *search.Row) {
	switch c.kind {
	case "":
		return // bound directly
	case "ip":
		if c.str.Valid && c.str.String != "" {
			s := c.str.String
			r.IP = &s
		}
	case "port":
		if c.num.Valid {
			p := int(c.num.Int64)
			r.Port = &p
		}
	case "asn":
		if c.num.Valid {
			a := int(c.num.Int64)
			r.ASN = &a
		}
	case "http_status":
		if c.num.Valid {
			h := int(c.num.Int64)
			r.HTTPStatus = &h
		}
	case "cert_expiry":
		if c.str.Valid && c.str.String != "" {
			if t, err := parseTS(c.str.String); err == nil {
				r.TLSCertExpiry = &t
			}
		}
	case "technologies":
		r.Technologies = jsonStrings(*(c.raw.(*any)))
	case "sources":
		r.Sources = jsonStrings(*(c.raw.(*any)))
	case "first_seen":
		if c.str.Valid && c.str.String != "" {
			if t, err := parseTS(c.str.String); err == nil {
				r.FirstSeen = t
			}
		}
	case "last_seen":
		if c.str.Valid && c.str.String != "" {
			if t, err := parseTS(c.str.String); err == nil {
				r.LastSeen = t
			}
		}
	case "stale":
		if c.num.Valid && c.num.Int64 != 0 {
			r.Stale = true
		}
	}
}
