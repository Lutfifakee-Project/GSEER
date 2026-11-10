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

package storage

// domain.go — the v0.0.1 domain types (SPEC-05 §2).
//
// M3 lands the types; M4 lands the repositories (upserts, L1/L2 insert
// paths) and the search_index union view.  The types mirror the M3
// migration set v1 schema (internal/storage/migrations/0001_core.up.sql)
// column-for-column so the M4 row scans are mechanical.

import "time"

// AssetKind enumerates the searchable asset types (SPEC-05 §2 row 1).
type AssetKind string

const (
	AssetKindIP       AssetKind = "ip"
	AssetKindDomain   AssetKind = "domain"
	AssetKindHostname AssetKind = "hostname"
)

// Asset is a searched entity: an IP, a domain, or a hostname with its
// metadata (SPEC-05 §2 row 1).  Uniqueness: (kind, value).
type Asset struct {
	ID    int64     `json:"id"`
	Kind  AssetKind `json:"kind"`
	Value string    `json:"value"`

	// Typed columns mirroring the kind (only the kind's column is set).
	IP       *string `json:"ip,omitempty"`
	Domain   *string `json:"domain,omitempty"`
	Hostname *string `json:"hostname,omitempty"`

	ASN        int    `json:"asn,omitempty"`
	ASNOrg     string `json:"asn_org,omitempty"`
	GeoCountry string `json:"geo_country,omitempty"`
	GeoRegion  string `json:"geo_region,omitempty"`
	GeoCity    string `json:"geo_city,omitempty"`

	// Sources that have observed this asset (search_index projection).
	Sources []string `json:"sources,omitempty"`

	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Stale     bool      `json:"stale"`
}

// Service is an observed service on an asset (SPEC-05 §2 row 2).
// Uniqueness: (asset_id, port, transport).
type Service struct {
	ID      int64 `json:"id"`
	AssetID int64 `json:"asset_id"`

	Port      int    `json:"port"`
	Transport string `json:"transport"` // tcp | udp
	Protocol  string `json:"protocol"`
	Name      string `json:"name,omitempty"`
	Product   string `json:"product,omitempty"`
	Version   string `json:"version,omitempty"`

	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Stale     bool      `json:"stale"`
}

// Observation is one provenance-bearing fact (SPEC-05 §2 row 3, §5).
// It carries the L1 delivery identity (SourceID + SourceCursor) and the
// L2 fact identity (SourceID, AssetID, ServiceID, ObservedAt,
// PayloadHash).  In v0.0.1 only the fixture loader writes observations
// (M4); real collectors arrive in v0.0.2.
type Observation struct {
	ID        int64  `json:"id"`
	SourceID  int64  `json:"source_id"`
	AssetID   int64  `json:"asset_id"`
	ServiceID *int64 `json:"service_id,omitempty"`

	ObservedAt   time.Time `json:"observed_at"`
	PayloadHash  string    `json:"payload_hash"`
	SourceCursor string    `json:"source_cursor"`
	// Provenance labels the record: "fixture" for the synthetic dataset
	// (QS-6, AC-9.7.x); source categories from v0.0.2 onwards.
	Provenance string         `json:"provenance"`
	Evidence   map[string]any `json:"evidence,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

// DataSource is a registered data source (SPEC-05 §2 row 5).  The
// v0.0.1 fixture source is itself a DataSource row whose TermsRef
// documents the labeled-synthetic nature (owner decision D-M3-1).
type DataSource struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Connector string `json:"connector"` // "fixture", "ct", "rdap", ...
	TermsRef  string `json:"terms_ref,omitempty"`
	// Config is the connector-specific configuration (JSONB in the DB).
	Config map[string]any `json:"config,omitempty"`
	// Enabled is always false for v0.0.1 runtime sources: nothing is
	// collected in the Developer Preview (SPEC-09 §1).
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}
