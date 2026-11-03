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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// httpx.go — small helpers the client and runner share: URL redaction for
// logs (credentials never reach an error message), scheme extraction, the
// body-size cap and the payload hash primitive.

// redactURL renders u for logs: userinfo credentials (user:password@)
// are replaced with "user:REDACTED@", everything else passes through.
// Unparseable input yields "<unparseable-url>".
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<unparseable-url>"
	}
	if u.User != nil {
		name := u.User.Username()
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(name, "REDACTED")
		} else {
			u.User = url.User(name)
		}
	}
	return u.String()
}

// urlScheme extracts the lowercase scheme of raw ("" when absent or
// unparseable).
func urlScheme(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme)
}

// errBodyTooLarge reports a response body exceeding the configured cap.
var errBodyTooLarge = fmt.Errorf("response body exceeds the configured cap")

// limitedBody caps a response body at max bytes; reading past the cap
// fails with errBodyTooLarge (a silently truncated JSON payload would
// parse fine and poison the index — truncation must be an error).
type limitedBody struct {
	rc  io.ReadCloser
	rem int64
}

func limitedReader(rc io.ReadCloser, max int64) io.ReadCloser {
	return &limitedBody{rc: rc, rem: max}
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.rem <= 0 {
		return 0, errBodyTooLarge
	}
	// One extra byte of headroom so an exactly-max body succeeds while
	// max+1 reports overflow.
	if int64(len(p)) > b.rem {
		p = p[:b.rem+1]
	}
	n, err := b.rc.Read(p)
	b.rem -= int64(n)
	if b.rem < 0 {
		return n, errBodyTooLarge
	}
	return n, err
}

func (b *limitedBody) Close() error { return b.rc.Close() }

// sha256Sum is the payload-hash primitive (L2 fact identity, checkpoint
// integrity).
func sha256Sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

// hexSHA256 renders sha256Sum as a hex string.
func hexSHA256(b []byte) string { return hex.EncodeToString(sha256Sum(b)) }
