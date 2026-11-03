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

package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// version.go — PostgreSQL version check (PD-8: >= 15; SPEC-05 §1).

// ServerVersionNumQuery returns the numeric server version
// (e.g. 170002 for 17.2); the major is num / 10000.
const ServerVersionNumQuery = `SELECT current_setting('server_version_num')::int`

// PGMajorVersion queries the connected server's major version.
func PGMajorVersion(ctx context.Context, conn *pgx.Conn) (int, error) {
	var num int
	if err := conn.QueryRow(ctx, ServerVersionNumQuery).Scan(&num); err != nil {
		return 0, err
	}
	return num / 10000, nil
}

// CheckPGVersion opens a connection, verifies the server major version
// is at least minMajor (PD-8 floor), and closes.  It returns the
// detected major version on success (even when too low — callers may
// report it) and an E_DB_VERSION DBError when below the floor.
func CheckPGVersion(ctx context.Context, rawDSN string, minMajor int) (int, error) {
	conn, err := connectAdmin(ctx, rawDSN)
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close(ctx) }()

	major, err := PGMajorVersion(ctx, conn)
	if err != nil {
		return 0, unreachableError(err, rawDSN)
	}
	if major < minMajor {
		e := newDBError(CodeDBVersion,
			fmt.Sprintf("PostgreSQL %d is below the supported minimum %d (PD-8).", major, minMajor)).
			withWhy("GSEER v0.0.1 requires PostgreSQL 15+ for the schema features it uses " +
				"(GENERATED ALWAYS AS IDENTITY, DISTINCT ON views, inet GiST indexing)")
		e.Next = "upgrade the server or point GSEER_DB_HOST at a PostgreSQL >= " +
			fmt.Sprint(minMajor) + " instance; the Compose stack ships postgres:17-alpine"
		return major, e
	}
	return major, nil
}
