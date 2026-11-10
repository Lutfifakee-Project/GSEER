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

package sqlite

// timefmt.go — SQ-02: fixed-width timestamp encoding for the personal
// engine.
//
// The personal schema stores every timestamp as TEXT.  With one format
// — UTC, RFC 3339, ALWAYS six fractional digits — lexicographic order
// equals chronological order, which gives the personal engine the
// properties PostgreSQL gets from timestamptz for free:
//   - L2 fact-identity dedup compares observed_at exactly (no
//     format-dependent collapses);
//   - keyset pagination and ORDER BY last_seen are deterministic down
//     to the microsecond (sub-ms observations never tie);
//   - the planner's plain text comparisons stay chronological.
//
// The storage layer is the ONLY writer of these columns; everything it
// reads goes through ParseTS, which also accepts the coarser RFC 3339
// and date-only forms the operator-facing surfaces may hand over (the
// cert_expiry evidence values, for example, are caller-formatted and
// normalized on read via the gseer_ts SQL function).

import (
	"fmt"
	"time"
)

// tsLayout is the canonical storage format: UTC, microseconds, Z.
const tsLayout = "2006-01-02T15:04:05.000000Z07:00"

// accepted parses the shapes the operator-facing surfaces may deliver.
var accepted = []string{
	"2006-01-02T15:04:05.000000Z07:00", // canonical
	time.RFC3339Nano,                   // operator ISO with varying fraction
	time.RFC3339,                       // operator ISO, seconds
	"2006-01-02T15:04:05",              // no zone → UTC
	"2006-01-02 15:04:05",              // no zone, space → UTC
	"2006-01-02",                       // date-only → midnight UTC
}

// FormatTS renders t in the canonical storage form (UTC, microseconds).
func FormatTS(t time.Time) string {
	return t.UTC().Format(tsLayout)
}

// ParseTS parses any accepted timestamp shape into time.Time (UTC).
func ParseTS(s string) (time.Time, error) {
	s = trimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	for _, layout := range accepted {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("not a recognized timestamp: %q", s)
}

// trimSpace is strings.TrimSpace without the import noise (the format
// helpers must stay dependency-free).
func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
