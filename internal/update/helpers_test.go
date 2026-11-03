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

package update

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// helpers_test.go — test infrastructure for the update suite.

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func timeNow() time.Time { return time.Now().UTC() }

func jsonMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// fakeHTTPClient serves a fixed URL→body table (the update fetcher's
// transport in tests — no real network).
func fakeHTTPClient(t *testing.T, table map[string]string) *http.Client {
	t.Helper()
	mux := http.NewServeMux()
	for rawURL, body := range table {
		path := rawURL
		if i := strings.Index(rawURL, "://"); i >= 0 {
			path = rawURL[i+3:]
			if j := strings.IndexByte(path, '/'); j >= 0 {
				path = path[j:]
			} else {
				path = "/"
			}
		}
		servedBody := body
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, servedBody)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	// Rewrite recorded URLs to the actual test server address.
	client := &http.Client{Transport: rewriteTransport{base: srv.URL}}
	return client
}

// rewriteTransport maps example.test URLs onto the httptest server.
type rewriteTransport struct{ base string }

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Host, "update-channel.test") || strings.Contains(req.URL.Host, "example.test") {
		req.URL.Host = strings.TrimPrefix(t.base, "http://")
		req.URL.Scheme = "http"
	}
	// The test server handles everything under its own mux by path.
	req.URL.Path = "/" + filepathBase(req.URL.Path)
	return http.DefaultTransport.RoundTrip(req)
}

func filepathBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

var _ io.Reader = (*bytes.Reader)(nil)
