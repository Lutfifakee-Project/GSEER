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

	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Client is the shared, rate-limited HTTP fetcher for passive
// connectors.  Defaults are deliberately conservative: passive means the
// provider never feels us.
type Client struct {
	http    *http.Client
	limiter *limiter
	ua      string
	maxBody int64
}

// ClientConfig tunes one Client.
type ClientConfig struct {
	// RequestsPerSecond caps the request rate (0 = DefaultRPS).
	RequestsPerSecond float64
	// Timeout bounds one request (0 = DefaultTimeout).
	Timeout time.Duration
	// MaxBodyBytes bounds one response body (0 = DefaultMaxBodyBytes).
	MaxBodyBytes int64
	// UserAgent identifies GSEER honestly (0 value = DefaultUserAgent).
	UserAgent string
}

// Politeness defaults (documented in docs/SOURCES.md per source; a
// source's own documented limit may be lower — the connector applies it
// through its ClientConfig).
const (
	DefaultRPS          = 1.0
	DefaultTimeout      = 15 * time.Second
	DefaultMaxBodyBytes = 8 << 20 // 8 MiB — no provider answer is that big
	DefaultUserAgent    = "GSEER/0.0.2 (passive connector; https://github.com/Lutfifakee-Project/GSEER)"
)

// NewClient builds a Client.
func NewClient(cfg ClientConfig) *Client {
	if cfg.RequestsPerSecond <= 0 {
		cfg.RequestsPerSecond = DefaultRPS
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUserAgent
	}
	return &Client{
		http:    &http.Client{Timeout: cfg.Timeout},
		limiter: newLimiter(cfg.RequestsPerSecond),
		ua:      cfg.UserAgent,
		maxBody: cfg.MaxBodyBytes,
	}
}

// GetJSON fetches url and decodes the JSON body into out.
func (c *Client) GetJSON(ctx context.Context, url string, out any) error {
	body, err := c.Get(ctx, url)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	if err := json.NewDecoder(body).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %v", redactURL(url), err)
	}
	return nil
}

// Get fetches url with rate limiting and the body-size cap.  The body
// must be closed by the caller.  Only https/http URLs are honored.
func (c *Client) Get(ctx context.Context, url string) (io.ReadCloser, error) {
	if scheme := urlScheme(url); scheme != "https" && scheme != "http" {
		return nil, fmt.Errorf("refusing non-HTTP(S) url %q", redactURL(url))
	}
	if err := c.limiter.wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %v", err)
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %v", redactURL(url), err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: provider rate limit (HTTP 429) — slow down or raise the interval", redactURL(url))
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: unexpected status %d", redactURL(url), resp.StatusCode)
	}
	return limitedReader(resp.Body, c.maxBody), nil
}

// limiter is a minimal fixed-interval token bucket (one token per 1/rps).
type limiter struct {
	mu   sync.Mutex
	next time.Time
	iv   time.Duration
}

func newLimiter(rps float64) *limiter {
	iv := time.Duration(float64(time.Second) / rps)
	if iv < time.Millisecond {
		iv = time.Millisecond
	}
	return &limiter{iv: iv, next: time.Now()}
}

func (l *limiter) wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	wait := l.next.Sub(now)
	l.next = l.next.Add(l.iv)
	l.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
