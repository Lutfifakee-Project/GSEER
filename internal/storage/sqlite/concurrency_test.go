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

package sqlite

// concurrency_test.go — SQ-03 (safe DSN for exotic paths) and SQ-05
// (the SetMaxOpenConns(1) + busy_timeout + WAL decision, proven under
// concurrent access with an integrity check).

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/storage"
)

// TestBuildDSNExoticPaths (SQ-03) — a path containing spaces, Unicode,
// '?', '#', '&' or '%' must open, persist data and REOPEN correctly.
func TestBuildDSNExoticPaths(t *testing.T) {
	cases := []string{
		"my database.db",          // spaces
		"data-日本語.db",             // unicode
		"quest?ion.db",            // '?' (query injection attempt)
		"frag#ment.db",            // '#' (fragment injection attempt)
		"amp&ersand.db",           // '&' (param separator attempt)
		"percent%20file.db",       // '%' (escape sequence attempt)
		"deep dir/nested name.db", // nested + spaces
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if runtime.GOOS == "windows" && name == "quest?ion.db" {
				t.Skip("'?' is a reserved filename character on Windows (the filesystem cannot create it); the DSN round-trip itself is exercised by the other cases on every platform")
			}
			path := filepath.Join(t.TempDir(), name)
			res, err := Open(path)
			if err != nil {
				t.Fatalf("open %q: %v", name, err)
			}
			ctx := context.Background()
			if _, err := Migrate(ctx, res.DB); err != nil {
				t.Fatalf("migrate %q: %v", name, err)
			}
			s := NewStore(res.DB)
			src := ensureSource(t, s, "dsn-src")
			if _, err := s.IngestFact(ctx, ptr(assetFact(src, storage.AssetKindIP, "198.51.100.80"))); err != nil {
				t.Fatalf("ingest %q: %v", name, err)
			}
			if err := res.DB.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}

			// REOPEN through the same path — the DSN must round-trip.
			res2, err := Open(path)
			if err != nil {
				t.Fatalf("reopen %q: %v", name, err)
			}
			defer res2.DB.Close()
			s2 := NewStore(res2.DB)
			st, err := s2.Stats(ctx)
			if err != nil {
				t.Fatalf("stats %q: %v", name, err)
			}
			if st.Assets != 1 {
				t.Errorf("path %q: reopened assets = %d, want 1 (data must persist through the exotic DSN)", name, st.Assets)
			}
		})
	}
}

// TestConcurrentIngestAndRead (SQ-05) — the SetMaxOpenConns(1) decision:
// writers and readers serialize inside the process without SQLITE_BUSY
// or corruption.  The data races are the point; -race runs this file.
func TestConcurrentIngestAndRead(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	src := ensureSource(t, s, "concurrent-src")

	const (
		writers  = 4
		readers  = 4
		perBatch = 25
	)
	var wg sync.WaitGroup
	errCh := make(chan error, writers+readers)

	for w := 0; w < writers; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perBatch; i++ {
				f := assetFact(src, storage.AssetKindIP, fmt.Sprintf("198.51.20%d.%d", w, i))
				f.Observation.SourceCursor = fmt.Sprintf("cw-%d-%d", w, i)
				f.Observation.PayloadHash = fmt.Sprintf("hw-%d-%d", w, i)
				if _, err := s.IngestFact(ctx, &f); err != nil {
					errCh <- fmt.Errorf("writer %d batch %d: %w", w, i, err)
					return
				}
			}
		}()
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perBatch; i++ {
				if _, err := s.Stats(ctx); err != nil {
					errCh <- fmt.Errorf("reader batch %d: %w", i, err)
					return
				}
				rules, err := s.LoadSuppressions(ctx)
				if err != nil {
					errCh <- fmt.Errorf("reader suppressions %d: %w", i, err)
					return
				}
				_ = rules
				time.Sleep(time.Millisecond)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	var n int
	if err := s.DB.QueryRow(`SELECT count(*) FROM assets`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != writers*perBatch {
		t.Errorf("assets = %d, want %d (no lost writes under concurrency)", n, writers*perBatch)
	}
	var verdict string
	if err := s.DB.QueryRow(`PRAGMA integrity_check`).Scan(&verdict); err != nil {
		t.Fatal(err)
	}
	if verdict != "ok" {
		t.Fatalf("SQ-05 VIOLATION: integrity_check = %q", verdict)
	}
}

// TestMultiStoreSameFile (SQ-05) — a second gseer process opening the
// same file must not corrupt or fail: the busy_timeout pragma serializes
// the cross-process writers (here: two stores in-process, same contract).
func TestMultiStoreSameFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.db")

	res1, err := Open(path)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	defer res1.DB.Close()
	if _, err := Migrate(context.Background(), res1.DB); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	res2, err := Open(path)
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	defer res2.DB.Close()

	s1, s2 := NewStore(res1.DB), NewStore(res2.DB)
	ctx := context.Background()
	src1 := ensureSource(t, s1, "shared-src1")
	src2 := ensureSource(t, s2, "shared-src2")

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			f := assetFact(src1, storage.AssetKindIP, fmt.Sprintf("198.51.201.%d", i))
			f.Observation.SourceCursor = fmt.Sprintf("s1-%d", i)
			f.Observation.PayloadHash = fmt.Sprintf("s1-%d", i)
			if _, err := s1.IngestFact(ctx, &f); err != nil {
				errCh <- fmt.Errorf("store1 %d: %w", i, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			f := assetFact(src2, storage.AssetKindIP, fmt.Sprintf("198.51.202.%d", i))
			f.Observation.SourceCursor = fmt.Sprintf("s2-%d", i)
			f.Observation.PayloadHash = fmt.Sprintf("s2-%d", i)
			if _, err := s2.IngestFact(ctx, &f); err != nil {
				errCh <- fmt.Errorf("store2 %d: %w", i, err)
				return
			}
		}
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	var n int
	if err := s1.DB.QueryRow(`SELECT count(*) FROM assets`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 40 {
		t.Errorf("assets = %d, want 40 (both stores' writes must land)", n)
	}
	var verdict string
	if err := s1.DB.QueryRow(`PRAGMA integrity_check`).Scan(&verdict); err != nil {
		t.Fatal(err)
	}
	if verdict != "ok" {
		t.Fatalf("SQ-05 VIOLATION: shared-file integrity_check = %q", verdict)
	}
}
