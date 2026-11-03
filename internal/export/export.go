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

// Package export streams search results as JSON, CSV, or NDJSON
// (v0.1.0, owner directive §4).
//
// Contracts:
//
//   - Same semantics: export pages through the SAME SearchService the
//     interactive surfaces use — the keyset cursor guarantees a stable
//     page sequence even while the index changes underneath (already-
//     emitted rows never repeat).
//   - Streaming: rows are written per page; memory stays O(page), not
//     O(result set).  Large exports are bounded only by MaxRows=0
//     (unlimited, the operator's explicit choice).
//   - Correct escaping: CSV is RFC 4180 via encoding/csv (quotes,
//     commas, newlines, UTF-8); JSON/NDJSON via encoding/json.
//   - Honest metadata: the footer carries the compiled query info
//     (mode, branch, took), the total count, and the index freshness
//     notice — never credentials, never connection strings.
//   - Array fields (technologies, sources) serialize as ";"-joined
//     strings inside CSV cells; JSON/NDJSON keep real arrays.
package export

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/search"
)

// Format names.
const (
	FormatJSON   = "json"
	FormatCSV    = "csv"
	FormatNDJSON = "ndjson"
)

// Formats lists the supported export formats.
var Formats = []string{FormatJSON, FormatCSV, FormatNDJSON}

// DefaultMaxRows is the CLI-side guard: an export without --max stops
// after this many rows with an explicit "raise --max" message.  A
// Request MaxRows of 0 means unlimited.
const DefaultMaxRows = 10000

// Request is one export job.
type Request struct {
	Query  search.Query // GQL/mode/sort/fields/timeout; Limit is managed by the pager
	Format string
	// MaxRows caps the stream; 0 = unlimited.
	MaxRows int
}

// Stats reports what one export wrote.
type Stats struct {
	Rows      int64  `json:"rows"`
	Pages     int    `json:"pages"`
	Format    string `json:"format"`
	TookMs    int64  `json:"took_ms"`
	Query     string `json:"query"`
	Mode      string `json:"mode"`
	Truncated bool   `json:"truncated"` // stopped at MaxRows
	Notice    string `json:"notice,omitempty"`
}

// csvColumns is the CSV header: the search_index projection in canonical
// order (search.Row's JSON tags are the JSON wire shape).
var csvColumns = []string{
	"row_id", "row_kind", "asset_id", "asset_kind", "asset_value",
	"ip", "domain", "hostname", "asn", "asn_org",
	"geo_country", "geo_region", "geo_city",
	"port", "transport", "protocol", "name", "product", "version",
	"http_status", "http_title", "http_server",
	"tls_version", "tls_cert_subject_cn", "tls_cert_issuer_cn", "tls_cert_expiry",
	"technologies", "sources", "first_seen", "last_seen", "stale",
}

// initExportWriter prepares the format-specific writer pair (the JSON
// encoder and/or the CSV writer) and emits any format prologue.
func initExportWriter(w io.Writer, format string) (*json.Encoder, *csv.Writer, error) {
	switch format {
	case FormatJSON:
		// Streamed object: {"rows": [ ... ], "count": N, "footer": {...}}
		if _, err := fmt.Fprint(w, `{"rows": [`); err != nil {
			return nil, nil, err
		}
		return json.NewEncoder(w), nil, nil
	case FormatNDJSON:
		return json.NewEncoder(w), nil, nil
	case FormatCSV:
		cw := csv.NewWriter(w)
		if err := cw.Write(csvColumns); err != nil {
			return nil, nil, err
		}
		return nil, cw, nil
	}
	return nil, nil, fmt.Errorf("E_EXPORT_FORMAT: unsupported format %q (supported: %s)",
		format, strings.Join(Formats, ", "))
}

// writeExportFooter emits the format-specific trailer after the last row.
func writeExportFooter(w io.Writer, enc *json.Encoder, cw *csv.Writer, format string, st Stats, meta search.QueryInfo) error {
	switch format {
	case FormatJSON:
		if _, err := fmt.Fprintf(w, `], "count": %d, "footer": `, st.Rows); err != nil {
			return err
		}
		if err := enc.Encode(map[string]any{"query": meta, "notice": st.Notice}); err != nil {
			return err
		}
		_, err := fmt.Fprint(w, "}")
		return err
	case FormatCSV:
		cw.Flush()
		return cw.Error()
	case FormatNDJSON:
		// The metadata record distinguishes itself from data rows by its
		// "record" key (data rows have "row_id").
		return enc.Encode(map[string]any{"record": "footer", "count": st.Rows, "query": meta, "notice": st.Notice})
	}
	return fmt.Errorf("E_EXPORT_FORMAT: unsupported format %q", format)
}

// Stream writes the export to w and returns the stats.  Rows are
// flushed per page — the caller's writer decides the buffering.
func Stream(ctx context.Context, svc *search.Service, req Request, w io.Writer) (Stats, error) {
	start := time.Now()
	if !knownFormat(req.Format) {
		return Stats{}, fmt.Errorf("E_EXPORT_FORMAT: unsupported format %q (supported: %s)",
			req.Format, strings.Join(Formats, ", "))
	}

	enc, cw, err := initExportWriter(w, req.Format)
	if err != nil {
		return Stats{}, err
	}
	var (
		st   Stats
		meta search.QueryInfo
	)
	pageQuery := req.Query
	pageQuery.WithStats = false

	first := true
	cursor := req.Query.Cursor
	limit := req.Query.Limit
	if limit <= 0 || limit > 1000 {
		limit = 500 // page size for export (a steady streaming batch)
	}

	for {
		pageQuery.Cursor = cursor
		pageQuery.Limit = limit
		res, serr := svc.Search(ctx, pageQuery)
		if serr != nil {
			return st, serr
		}
		meta = res.Query
		st.Pages++

		for i := range res.Rows {
			if req.MaxRows > 0 && st.Rows >= int64(req.MaxRows) {
				st.Truncated = true
				break
			}
			if werr := writeRow(w, enc, cw, req.Format, &res.Rows[i], first); werr != nil {
				return st, werr
			}
			first = false
			st.Rows++
		}
		if st.Truncated || !res.HasMore || res.NextCursor == "" {
			st.Notice = res.Notice
			break
		}
		cursor = res.NextCursor
	}

	if ferr := writeExportFooter(w, enc, cw, req.Format, st, meta); ferr != nil {
		return st, ferr
	}

	st.Format = req.Format
	st.Query = req.Query.GQL
	st.Mode = meta.Mode
	st.TookMs = time.Since(start).Milliseconds()
	return st, nil
}

func writeRow(w io.Writer, enc *json.Encoder, cw *csv.Writer, format string, row *search.Row, first bool) error {
	switch format {
	case FormatJSON:
		if !first {
			if _, err := fmt.Fprint(w, ", "); err != nil {
				return err
			}
		}
		return enc.Encode(row)
	case FormatNDJSON:
		return enc.Encode(row)
	case FormatCSV:
		return cw.Write(csvCells(row))
	}
	return fmt.Errorf("E_EXPORT_FORMAT: unsupported format %q", format)
}

// csvCells renders one row in the canonical column order.  Pointers
// become empty cells; arrays join on ";"; timestamps are RFC 3339 UTC.
func csvCells(r *search.Row) []string {
	cells := make([]string, len(csvColumns))
	str := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	intStr := func(i *int) string {
		if i == nil {
			return ""
		}
		return strconv.Itoa(*i)
	}
	cells[0] = strconv.FormatInt(r.RowID, 10)
	cells[1] = r.RowKind
	cells[2] = strconv.FormatInt(r.AssetID, 10)
	cells[3] = r.AssetKind
	cells[4] = r.AssetValue
	cells[5] = str(r.IP)
	cells[6] = str(r.Domain)
	cells[7] = str(r.Hostname)
	cells[8] = intStr(r.ASN)
	cells[9] = str(r.ASNOrg)
	cells[10] = str(r.GeoCountry)
	cells[11] = str(r.GeoRegion)
	cells[12] = str(r.GeoCity)
	cells[13] = intStr(r.Port)
	cells[14] = str(r.Transport)
	cells[15] = str(r.Protocol)
	cells[16] = str(r.Name)
	cells[17] = str(r.Product)
	cells[18] = str(r.Version)
	cells[19] = intStr(r.HTTPStatus)
	cells[20] = str(r.HTTPTitle)
	cells[21] = str(r.HTTPServer)
	cells[22] = str(r.TLSVersion)
	cells[23] = str(r.TLSCertSubjectCN)
	cells[24] = str(r.TLSCertIssuerCN)
	if r.TLSCertExpiry != nil {
		cells[25] = r.TLSCertExpiry.UTC().Format(time.RFC3339)
	}
	cells[26] = strings.Join(r.Technologies, ";")
	cells[27] = strings.Join(r.Sources, ";")
	cells[28] = r.FirstSeen.UTC().Format(time.RFC3339)
	cells[29] = r.LastSeen.UTC().Format(time.RFC3339)
	cells[30] = strconv.FormatBool(r.Stale)
	return cells
}

func knownFormat(f string) bool {
	for _, ok := range Formats {
		if f == ok {
			return true
		}
	}
	return false
}
