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

package collect

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// connector_http_test.go — the rdap and ct connectors against local
// httptest endpoints (the test suite NEVER contacts real providers).

// ---- RDAP ----

func TestRDAPConnectorAgainstBootstrapAndRIR(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Every endpoint — bootstrap AND RIR — stays on the test server;
		// the test suite never contacts a real provider.
		switch r.URL.Path {
		case "/ipv4.json":
			_, _ = w.Write([]byte(`{"description":"RDAP bootstrap file for IPv4 Address Space","services":[
                [["203.0.113.0/24"],["` + srv.URL + `/"]],
                [["10.0.0.0/8"],["` + srv.URL + `/"]]
            ],"version":"1.0"}`))
		case "/ip/203.0.113.7":
			_, _ = w.Write([]byte(`{"handle":"NET-203-0-113-0-1","name":"EXAMPLE-NET","type":"ALLOCATED PA",
                "country":"US","startAddress":"203.0.113.0","endAddress":"203.0.113.255",
                "events":[{"eventAction":"registration","eventDate":"2002-04-23T00:00:00Z"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(ClientConfig{RequestsPerSecond: 1000})
	base := testBase(t, srv)
	// Point the bootstrap URLs at the test server.
	oldV4 := rdapBootstrapV4
	rdapBootstrapV4 = base + "/ipv4.json"
	t.Cleanup(func() { rdapBootstrapV4 = oldV4 })

	ctx := ctxFor(SourceConfig{SeedMode: "config", Seeds: []string{"203.0.113.7"}})
	ctx.Client = c
	b, err := rdapConnector{}.Collect(ctx)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(b.Raws) != 1 {
		t.Fatalf("expected 1 enrichment record, got %d", len(b.Raws))
	}
	r := b.Raws[0]
	if r.GeoCountry != "US" {
		t.Errorf("country not extracted: %+v", r)
	}
	if r.Confidence != "high" {
		t.Errorf("registry records are high confidence, got %q", r.Confidence)
	}
	rd, _ := r.Extra["rdap"].(map[string]any)
	if rd["handle"] != "NET-203-0-113-0-1" || rd["registered"] != "2002-04-23T00:00:00Z" {
		t.Errorf("rdap evidence incomplete: %+v", rd)
	}
}

func TestRDAPConnectorFailsOnBootstrapMiss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"services":[]}`))
	}))
	defer srv.Close()
	oldV4 := rdapBootstrapV4
	rdapBootstrapV4 = testBase(t, srv) + "/ipv4.json"
	t.Cleanup(func() { rdapBootstrapV4 = oldV4 })

	ctx := ctxFor(SourceConfig{SeedMode: "config", Seeds: []string{"203.0.113.7"}})
	ctx.Client = NewClient(ClientConfig{RequestsPerSecond: 1000})
	_, err := rdapConnector{}.Collect(ctx)
	if err == nil || !strings.Contains(err.Error(), "no endpoint for family") {
		t.Fatalf("expected bootstrap-miss error, got %v", err)
	}
}

// ---- CT (crt.sh shape) ----

func TestCTConnectorParsesCertificates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "example.com" || r.URL.Query().Get("output") != "json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[
          {"issuer_name":"Let's Encrypt","common_name":"example.com",
           "name_value":"example.com\nwww.example.com\n*.dev.example.com",
           "not_before":"2026-01-01T00:00:00","not_after":"2026-04-01T00:00:00",
           "serial_number":"04a2","id":101},
          {"issuer_name":"Let's Encrypt","common_name":"www.example.com",
           "name_value":"www.example.com","not_before":"2026-02-01T00:00:00",
           "not_after":"2026-05-01T00:00:00","serial_number":"04a3","id":102}
        ]`))
	}))
	defer srv.Close()

	ctx := ctxFor(SourceConfig{SeedMode: "config", Seeds: []string{"example.com"}, Endpoint: srv.URL + "/"})
	ctx.Client = NewClient(ClientConfig{RequestsPerSecond: 1000})

	b, err := ctConnector{}.Collect(ctx)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	kinds := map[string][]string{}
	for _, r := range b.Raws {
		kinds[r.AssetKind] = append(kinds[r.AssetKind], r.Value)
	}
	if len(kinds["domain"]) != 1 || kinds["domain"][0] != "example.com" {
		t.Errorf("apex must be one domain fact, got %v", kinds["domain"])
	}
	if len(kinds["hostname"]) != 2 {
		t.Fatalf("expected www + dev hostnames, got %v", kinds["hostname"])
	}
	for _, r := range b.Raws {
		if r.Confidence != "high" {
			t.Errorf("CT names are high confidence, got %q", r.Confidence)
		}
		ev := r.Extra["ct"].(map[string]any)
		if ev["issuer"] != "Let's Encrypt" {
			t.Errorf("issuer not carried: %+v", ev)
		}
	}
	// Wildcards stripped, intra-batch dedup: 3 distinct names from cert 1
	// and 1 overlap from cert 2 → exactly 3 records total.
	if len(b.Raws) != 3 {
		t.Errorf("expected 3 deduped records, got %d: %+v", len(b.Raws), b.Raws)
	}
}

// ---- helpers ----

func testBase(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	return srv.URL
}
