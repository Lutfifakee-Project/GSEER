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

//go:build integration

package export

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Lutfifakee-Project/GSEER/internal/fixture"
	"github.com/Lutfifakee-Project/GSEER/internal/search"
	"github.com/Lutfifakee-Project/GSEER/internal/storage/postgres"
)

// export_integration_test.go — the streaming export over the fixture
// corpus (PG 17):
//
//   - pagination stability: limit=2 over 7 rows walks 4 pages via the
//     keyset cursor and emits 7 DISTINCT rows (no repeats, no loss);
//   - every format's shape holds (JSON object parses; NDJSON line/row
//     count; CSV record count + header);
//   - credential hygiene: the DSN (user/host/port) never appears in any
//     export byte stream;
//   - the footer metadata is accurate (count, query echo, notice).

const expAdminDSN = "postgres://gseer@127.0.0.1:5433/postgres?sslmode=disable"

func expSetup(t *testing.T) (*search.Service, *pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("GSEER_TEST_PG_ADMIN")
	if dsn == "" {
		dsn = expAdminDSN
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	name := fmt.Sprintf("gseer_m20_export_%d_%d", os.Getpid(), time.Now().UnixNano()%100000)
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create db: %v", err)
	}
	_ = admin.Close(ctx)
	testDSN := strings.Replace(dsn, "/postgres?", "/"+name+"?", 1)

	// v0.2.0: bootstrap roles before the hardened migration set.
	if _, err := postgres.BootstrapRoles(ctx, testDSN); err != nil {
		t.Fatalf("BootstrapRoles: %v", err)
	}
	if _, _, err := postgres.RunMigrations(testDSN, 5000); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if _, err := fixture.Load(context.Background(), postgres.NewStore(pool)); err != nil {
		pool.Close()
		t.Fatalf("fixture load: %v", err)
	}
	cleanup := func() {
		pool.Close()
		actx, acancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer acancel()
		a, err := pgx.Connect(actx, dsn)
		if err == nil {
			_, _ = a.Exec(actx,
				`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid <> pg_backend_pid()`, name)
			_, _ = a.Exec(actx, `DROP DATABASE IF EXISTS `+name)
			_ = a.Close(actx)
		}
	}
	t.Cleanup(cleanup)
	return search.New(pool), pool, testDSN
}

func TestExportFormatsOverFixtures(t *testing.T) {
	svc, _, dsn := expSetup(t)
	ctx := context.Background()
	secret := dsn // contains user@host:port — must never leak into output

	t.Run("json", func(t *testing.T) {
		var buf bytes.Buffer
		st, err := Stream(ctx, svc, Request{
			Query:  search.Query{GQL: `stale == "false"`, Limit: 2},
			Format: FormatJSON,
		}, &buf)
		if err != nil {
			t.Fatalf("stream json: %v", err)
		}
		if st.Rows != 7 || st.Pages != 4 {
			t.Errorf("json stats = %+v, want rows=7 pages=4 (limit=2 over 7 rows)", st)
		}
		var doc struct {
			Rows   []map[string]any `json:"rows"`
			Count  int64            `json:"count"`
			Footer struct {
				Query struct {
					Mode   string `json:"mode"`
					Branch string `json:"branch"`
				} `json:"query"`
			} `json:"footer"`
		}
		if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
			t.Fatalf("json output does not parse: %v\n%s", err, buf.String()[:min(400, buf.Len())])
		}
		if len(doc.Rows) != 7 || doc.Count != 7 {
			t.Errorf("json rows=%d count=%d, want 7/7", len(doc.Rows), doc.Count)
		}
		if doc.Footer.Query.Mode != "service" {
			t.Errorf("footer mode = %q", doc.Footer.Query.Mode)
		}
		// Distinct row ids — pagination never repeats a row.
		seen := map[float64]bool{}
		for _, r := range doc.Rows {
			id := r["row_id"].(float64)
			if seen[id] {
				t.Errorf("row_id %v exported twice (pagination repeat)", id)
			}
			seen[id] = true
		}
		if strings.Contains(buf.String(), secret) {
			t.Error("DSN leaked into the JSON export stream")
		}
	})

	t.Run("ndjson", func(t *testing.T) {
		var buf bytes.Buffer
		st, err := Stream(ctx, svc, Request{
			Query:  search.Query{GQL: `stale == "false"`, Limit: 3},
			Format: FormatNDJSON,
		}, &buf)
		if err != nil {
			t.Fatalf("stream ndjson: %v", err)
		}
		if st.Rows != 7 {
			t.Errorf("ndjson rows = %d, want 7", st.Rows)
		}
		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		if len(lines) != 8 { // 7 rows + footer
			t.Fatalf("ndjson lines = %d, want 8", len(lines))
		}
		var footer map[string]any
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &footer); err != nil {
			t.Fatalf("footer line does not parse: %v", err)
		}
		if footer["record"] != "footer" {
			t.Errorf("footer marker = %v", footer["record"])
		}
		// Data rows carry row_id; the footer does not.
		var row map[string]any
		if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
			t.Fatalf("data line does not parse: %v", err)
		}
		if row["row_id"] == nil {
			t.Error("first data line lacks row_id")
		}
		if strings.Contains(buf.String(), secret) {
			t.Error("DSN leaked into the NDJSON export stream")
		}
	})

	t.Run("csv", func(t *testing.T) {
		var buf bytes.Buffer
		st, err := Stream(ctx, svc, Request{
			Query:  search.Query{GQL: `stale == "false"`, Limit: 2},
			Format: FormatCSV,
		}, &buf)
		if err != nil {
			t.Fatalf("stream csv: %v", err)
		}
		if st.Rows != 7 {
			t.Errorf("csv rows = %d, want 7", st.Rows)
		}
		r := csv.NewReader(&buf)
		records, err := r.ReadAll()
		if err != nil {
			t.Fatalf("csv re-parse: %v", err)
		}
		if len(records) != 8 { // header + 7 rows
			t.Fatalf("csv records = %d, want 8", len(records))
		}
		if strings.Join(records[0], ",") != strings.Join(csvColumns, ",") {
			t.Errorf("csv header mismatch:\n got %v\nwant %v", records[0], csvColumns)
		}
		if strings.Contains(buf.String(), secret) {
			t.Error("DSN leaked into the CSV export stream")
		}
	})
}

func TestExportMaxRowsCap(t *testing.T) {
	svc, _, _ := expSetup(t)
	var buf bytes.Buffer
	st, err := Stream(context.Background(), svc, Request{
		Query:   search.Query{GQL: `stale == "false"`},
		Format:  FormatNDJSON,
		MaxRows: 3,
	}, &buf)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if st.Rows != 3 || !st.Truncated {
		t.Errorf("stats = %+v, want rows=3 truncated=true", st)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
