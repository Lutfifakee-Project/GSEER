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

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- BuildFacts (the data-quality pipeline) ----

func TestBuildFactsNormalizesAndDedupes(t *testing.T) {
	raws := []RawFact{
		{AssetKind: "ip", Value: "203.0.113.7", ObservedAt: time.Now()},
		{AssetKind: "ip", Value: " 203.0.113.7 ", ObservedAt: time.Now()},         // dup after trim
		{AssetKind: "domain", Value: "EXAMPLE.com.", ObservedAt: time.Now()},      // case + dot
		{AssetKind: "domain", Value: "example.com", ObservedAt: time.Now()},       // dup after fold
		{AssetKind: "hostname", Value: "München.example", ObservedAt: time.Now()}, // IDNA
		{AssetKind: "ip", Value: "2001:0DB8:0000::1", ObservedAt: time.Now()},     // IPv6 canonical
	}
	facts, bad := BuildFacts("dns", raws)
	if len(bad) != 0 {
		t.Fatalf("expected no bad records, got %v", bad)
	}
	if len(facts) != 4 {
		t.Fatalf("expected 4 deduped facts, got %d: %+v", len(facts), facts)
	}
	got := map[string]string{}
	for _, f := range facts {
		got[string(f.Asset.Kind)+"|"+f.Asset.Value] = f.Observation.Provenance
	}
	for _, want := range []string{
		"ip|203.0.113.7", "domain|example.com",
		"hostname|xn--mnchen-3ya.example", "ip|2001:db8::1",
	} {
		if got[want] != "dns" {
			t.Errorf("missing fact %s (got %+v)", want, got)
		}
	}
}

func TestBuildFactsInvalidRecordsAreBadRecords(t *testing.T) {
	raws := []RawFact{
		{AssetKind: "ip", Value: "not-an-ip"},
		{AssetKind: "domain", Value: "bad label!.example"},
		{AssetKind: "ip", Value: "999.1.1.1"},
		{AssetKind: "banana", Value: "x"},
	}
	facts, bad := BuildFacts("dns", raws)
	if len(facts) != 0 {
		t.Fatalf("expected zero facts, got %d", len(facts))
	}
	if len(bad) != 4 {
		t.Fatalf("expected 4 bad records, got %d", len(bad))
	}
	for _, b := range bad {
		if b.Reason == "" {
			t.Error("bad record without reason")
		}
	}
}

func TestBuildFactsAssetLevelOnly(t *testing.T) {
	// P1 honesty contract 3: the pipeline can never emit a service.
	raws := []RawFact{{AssetKind: "ip", Value: "203.0.113.9"}, {AssetKind: "domain", Value: "a.example"}}
	facts, _ := BuildFacts("ip2asn", raws)
	for _, f := range facts {
		if f.Service != nil {
			t.Fatalf("pipeline produced a service fact: %+v", f.Service)
		}
	}
}

func TestBuildFactsEnrichmentAndConfidence(t *testing.T) {
	facts, bad := BuildFacts("ip2asn", []RawFact{{
		AssetKind: "ip", Value: "203.0.113.7", ASN: 64500, ASNOrg: "EXAMPLE-AS",
		GeoCountry: "US", Confidence: "medium",
		Extra: map[string]any{"ip2asn": map[string]any{"prefix": "203.0.113.0/24"}},
	}})
	if len(bad) != 0 || len(facts) != 1 {
		t.Fatalf("unexpected pipeline result: %d facts, %d bad", len(facts), len(bad))
	}
	a := facts[0].Asset
	if a.ASN != 64500 || a.ASNOrg != "EXAMPLE-AS" || a.GeoCountry != "US" {
		t.Errorf("enrichment lost: %+v", a)
	}
	if a.IP == nil || *a.IP != "203.0.113.7" {
		t.Errorf("typed ip pointer not set: %+v", a)
	}
	if facts[0].Observation.Evidence["confidence"] != "medium" {
		t.Errorf("confidence not carried into evidence: %+v", facts[0].Observation.Evidence)
	}
	if facts[0].Observation.Provenance != "ip2asn" {
		t.Errorf("provenance must be the connector kind, got %q", facts[0].Observation.Provenance)
	}
}

func TestBuildFactsPayloadHashStable(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := RawFact{AssetKind: "ip", Value: "203.0.113.7", ObservedAt: at, Confidence: "high"}
	f1, _ := BuildFacts("dns", []RawFact{r})
	f2, _ := BuildFacts("dns", []RawFact{r})
	if f1[0].Observation.PayloadHash != f2[0].Observation.PayloadHash {
		t.Error("identical records must produce identical L2 payload hashes")
	}
	if !strings.HasPrefix(f1[0].Observation.PayloadHash, "sha256:") {
		t.Errorf("payload hash not sha256-prefixed: %q", f1[0].Observation.PayloadHash)
	}
}

// ---- HTTP client ----

func TestClientRateLimitAndJSON(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("User-Agent") == "" {
			t.Error("request carried no User-Agent")
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewClient(ClientConfig{RequestsPerSecond: 1000, Timeout: 2 * time.Second})
	var out struct{ OK bool }
	if err := c.GetJSON(context.Background(), srv.URL, &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if !out.OK || hits != 1 {
		t.Fatalf("decode failed: ok=%v hits=%d", out.OK, hits)
	}
}

func TestClientRefusesNonHTTP(t *testing.T) {
	c := NewClient(ClientConfig{RequestsPerSecond: 1000})
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/x", "gopher://x"} {
		if _, err := c.Get(context.Background(), u); err == nil {
			t.Errorf("Get(%q) must refuse non-HTTP(S) schemes", u)
		}
	}
}

func TestClientBodyCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer srv.Close()
	c := NewClient(ClientConfig{RequestsPerSecond: 1000, MaxBodyBytes: 1024})
	body, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	buf := make([]byte, 4096)
	n, rerr := body.Read(buf)
	body.Close()
	if rerr == nil || n != 1025 || (!errors.Is(rerr, errBodyTooLarge)) {
		t.Fatalf("expected errBodyTooLarge after cap, got n=%d err=%v", n, rerr)
	}
}

func TestClient429IsExplicitError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := NewClient(ClientConfig{RequestsPerSecond: 1000})
	_, err := c.Get(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("expected explicit 429 error, got %v", err)
	}
}

func TestRedactURLHidesPassword(t *testing.T) {
	got := redactURL("https://user:secret@example.com/p?q=1")
	if strings.Contains(got, "secret") {
		t.Fatalf("password leaked into %q", got)
	}
	if !strings.Contains(got, "user:REDACTED@example.com/p") {
		t.Fatalf("redaction wrong: %q", got)
	}
	if got := redactURL("::::"); got != "<unparseable-url>" {
		t.Fatalf("unparseable url handling wrong: %q", got)
	}
}

// ---- Review verdicts pinned (the terms gate is part of the contract) ----

func TestBuiltinConnectorVerdicts(t *testing.T) {
	reg := Registry()
	for kind, want := range map[string]struct{ verdict, doc string }{
		"dns":    {ReviewEnabled, "docs/SOURCES.md"},
		"ip2asn": {ReviewEnabled, "docs/SOURCES.md"},
		"ptr":    {ReviewEnabled, "docs/SOURCES.md"},
		"rdap":   {ReviewDisabled, "docs/SOURCES.md"},
		"ct":     {ReviewDisabled, "docs/SOURCES.md"},
	} {
		c, ok := reg[kind]
		if !ok {
			t.Fatalf("connector %q not registered", kind)
		}
		rv := c.Review()
		if rv.Verdict != want.verdict {
			t.Errorf("connector %q verdict = %q, want %q", kind, rv.Verdict, want.verdict)
		}
		if !strings.Contains(rv.DocRef, want.doc) {
			t.Errorf("connector %q DocRef = %q, want it anchored in %s", kind, rv.DocRef, want.doc)
		}
		if rv.Why == "" {
			t.Errorf("connector %q has an empty Why — every verdict needs its justification", kind)
		}
	}
}
