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
	kind string // dispatch key into applyKinds ("" = bound directly)
	// one of the scan slots (bound directly), or the raw slot.
	str sql.NullString
	num sql.NullInt64
	raw any
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

// bindCell picks the scan slot for one projected column.  The
// scanBindings table maps every projected column name to its slot
// constructor; unknown columns fall through to a discard buffer.
func bindCell(r *search.Row, col string) scanCell {
	if newCell, ok := scanBindings[col]; ok {
		return newCell(r)
	}
	var discard any
	return scanCell{raw: &discard}
}

// scanBindings maps each projected column name to the scanCell it
// binds.  Direct columns return a raw pointer into the Row; deferred
// columns return a kind-keyed cell that apply copies after the Scan.
var scanBindings = map[string]func(*search.Row) scanCell{
	"row_id":          func(r *search.Row) scanCell { return scanCell{raw: &r.RowID} },
	"row_kind":        func(r *search.Row) scanCell { return scanCell{raw: &r.RowKind} },
	"asset_id":        func(r *search.Row) scanCell { return scanCell{raw: &r.AssetID} },
	"asset_kind":      func(r *search.Row) scanCell { return scanCell{raw: &r.AssetKind} },
	"asset_value":     func(r *search.Row) scanCell { return scanCell{raw: &r.AssetValue} },
	"ip":              func(*search.Row) scanCell { return scanCell{kind: "ip"} },
	"domain":          func(r *search.Row) scanCell { return scanCell{raw: &r.Domain} },
	"hostname":        func(r *search.Row) scanCell { return scanCell{raw: &r.Hostname} },
	"asn":             func(*search.Row) scanCell { return scanCell{kind: "asn"} },
	"asn_org":         func(r *search.Row) scanCell { return scanCell{raw: &r.ASNOrg} },
	"geo_country":     func(r *search.Row) scanCell { return scanCell{raw: &r.GeoCountry} },
	"geo_region":      func(r *search.Row) scanCell { return scanCell{raw: &r.GeoRegion} },
	"geo_city":        func(r *search.Row) scanCell { return scanCell{raw: &r.GeoCity} },
	"port":            func(*search.Row) scanCell { return scanCell{kind: "port"} },
	"transport":       func(r *search.Row) scanCell { return scanCell{raw: &r.Transport} },
	"protocol":        func(r *search.Row) scanCell { return scanCell{raw: &r.Protocol} },
	"name":            func(r *search.Row) scanCell { return scanCell{raw: &r.Name} },
	"product":         func(r *search.Row) scanCell { return scanCell{raw: &r.Product} },
	"version":         func(r *search.Row) scanCell { return scanCell{raw: &r.Version} },
	"http_status":     func(*search.Row) scanCell { return scanCell{kind: "http_status"} },
	"http_title":      func(r *search.Row) scanCell { return scanCell{raw: &r.HTTPTitle} },
	"http_server":     func(r *search.Row) scanCell { return scanCell{raw: &r.HTTPServer} },
	"tls_version":     func(r *search.Row) scanCell { return scanCell{raw: &r.TLSVersion} },
	"cert_subject_cn": func(r *search.Row) scanCell { return scanCell{raw: &r.TLSCertSubjectCN} },
	"cert_issuer_cn":  func(r *search.Row) scanCell { return scanCell{raw: &r.TLSCertIssuerCN} },
	"cert_expiry":     func(*search.Row) scanCell { return scanCell{kind: "cert_expiry"} },
	"technologies":    func(*search.Row) scanCell { return scanCell{kind: "technologies", raw: new(any)} },
	"sources":         func(*search.Row) scanCell { return scanCell{kind: "sources", raw: new(any)} },
	"first_seen":      func(*search.Row) scanCell { return scanCell{kind: "first_seen"} },
	"last_seen":       func(*search.Row) scanCell { return scanCell{kind: "last_seen"} },
	"stale":           func(*search.Row) scanCell { return scanCell{kind: "stale"} },
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

// applyKinds maps each deferred-decode kind to its post-Scan copy into
// the Row (the per-column semantics that used to live in a switch).
var applyKinds = map[string]func(*scanCell, *search.Row){
	"ip": func(c *scanCell, r *search.Row) {
		if c.str.Valid && c.str.String != "" {
			s := c.str.String
			r.IP = &s
		}
	},
	"port": func(c *scanCell, r *search.Row) {
		if c.num.Valid {
			p := int(c.num.Int64)
			r.Port = &p
		}
	},
	"asn": func(c *scanCell, r *search.Row) {
		if c.num.Valid {
			a := int(c.num.Int64)
			r.ASN = &a
		}
	},
	"http_status": func(c *scanCell, r *search.Row) {
		if c.num.Valid {
			h := int(c.num.Int64)
			r.HTTPStatus = &h
		}
	},
	"cert_expiry": func(c *scanCell, r *search.Row) {
		if c.str.Valid && c.str.String != "" {
			if t, err := parseTS(c.str.String); err == nil {
				r.TLSCertExpiry = &t
			}
		}
	},
	"technologies": func(c *scanCell, r *search.Row) {
		r.Technologies = jsonStrings(*(c.raw.(*any)))
	},
	"sources": func(c *scanCell, r *search.Row) {
		r.Sources = jsonStrings(*(c.raw.(*any)))
	},
	"first_seen": func(c *scanCell, r *search.Row) {
		if c.str.Valid && c.str.String != "" {
			if t, err := parseTS(c.str.String); err == nil {
				r.FirstSeen = t
			}
		}
	},
	"last_seen": func(c *scanCell, r *search.Row) {
		if c.str.Valid && c.str.String != "" {
			if t, err := parseTS(c.str.String); err == nil {
				r.LastSeen = t
			}
		}
	},
	"stale": func(c *scanCell, r *search.Row) {
		if c.num.Valid && c.num.Int64 != 0 {
			r.Stale = true
		}
	},
}

// apply copies the scanned cell into the Row (deferred decodes).
func (c *scanCell) apply(r *search.Row) {
	if c.kind == "" {
		return // bound directly
	}
	if fn, ok := applyKinds[c.kind]; ok {
		fn(c, r)
	}
}
