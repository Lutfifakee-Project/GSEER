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

package collect

// ptr_test.go — the reverse-DNS connector's unit tests: answers become
// hostname facts with PTR evidence, invalid seeds flow to the
// dead-letter pipeline (never silently dropped), and a transport-wide
// failure is retryable.

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/suppress"
)

func TestPTRConnectorCollect(t *testing.T) {
	orig := lookupAddr
	t.Cleanup(func() { lookupAddr = orig })

	now := time.Now().UTC()
	c := ptrConnector{}

	t.Run("answers become hostname facts with ptr evidence", func(t *testing.T) {
		lookupAddr = func(ctx context.Context, r *net.Resolver, addr string) ([]string, error) {
			if addr != "203.0.113.50" {
				return nil, errors.New("unexpected query " + addr)
			}
			return []string{"portal.Example.test."}, nil
		}
		b, err := c.Collect(Ctx{
			Context: context.Background(),
			Config:  SourceConfig{SeedMode: "config", Seeds: []string{"203.0.113.50"}},
			Now:     func() time.Time { return now },
		})
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		if !b.Done || len(b.Raws) != 1 {
			t.Fatalf("batch = %+v, want 1 record, done", b)
		}
		raw := b.Raws[0]
		if raw.AssetKind != "hostname" {
			t.Errorf("asset kind = %q, want hostname", raw.AssetKind)
		}
		want, _ := suppress.NormalizeDomain("portal.Example.test.")
		if raw.Value != want {
			t.Errorf("value = %q, want %q (normalized)", raw.Value, want)
		}
		if raw.Confidence != "medium" {
			t.Errorf("confidence = %q, want medium (recursive answers may be cached)", raw.Confidence)
		}
		if raw.Extra["ptr"] == nil {
			t.Errorf("evidence missing the ptr block: %+v", raw.Extra)
		}
		if b.Notes["lookups"] != 1 || b.Notes["answers"] != 1 {
			t.Errorf("notes = %+v, want lookups=1 answers=1", b.Notes)
		}
	})

	t.Run("invalid seed flows to the pipeline as a dead-letter record", func(t *testing.T) {
		lookupAddr = func(ctx context.Context, r *net.Resolver, addr string) ([]string, error) {
			return nil, errors.New("must not be queried for an invalid seed")
		}
		b, err := c.Collect(Ctx{
			Context: context.Background(),
			Config:  SourceConfig{SeedMode: "config", Seeds: []string{"not-an-ip"}},
			Now:     func() time.Time { return now },
		})
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		if len(b.Raws) != 1 || b.Raws[0].AssetKind != "ip" || b.Raws[0].Value != "not-an-ip" {
			t.Fatalf("batch = %+v, want the invalid seed passed through unchanged", b)
		}
	})

	t.Run("every lookup failing is a retryable transport error", func(t *testing.T) {
		lookupAddr = func(ctx context.Context, r *net.Resolver, addr string) ([]string, error) {
			return nil, errors.New("resolver down")
		}
		_, err := c.Collect(Ctx{
			Context: context.Background(),
			Config:  SourceConfig{SeedMode: "config", Seeds: []string{"203.0.113.50", "203.0.113.51"}},
			Now:     func() time.Time { return now },
		})
		if err == nil || !strings.Contains(err.Error(), "all 2 reverse lookups failed") {
			t.Errorf("err = %v, want the all-lookups-failed transport error", err)
		}
	})
}
