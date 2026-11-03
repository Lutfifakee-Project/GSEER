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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// pool.go — the application pgxpool lifecycle (owner decision D-M3-9).
//
// The pool is created ONLY AFTER migrations succeed (server start
// ordering, D-M3-8): the pool's prepared-statement machinery assumes
// the schema exists.  Shutdown drains in-flight queries via Close().

// Default pool settings (conservative for the v0.0.1 dev/preview;
// owner decision D-M3-9).
const (
	DefaultPoolMaxConns        = 4
	DefaultPoolMinConns        = 1
	DefaultPoolMaxConnLifetime = time.Hour
	DefaultPoolMaxConnIdleTime = 15 * time.Minute
	DefaultPoolHealthCheck     = 30 * time.Second
)

// NewPool builds a pgxpool with the GSEER defaults.  The DSN is the
// operator's raw connection string (never logged — redact.go).
func NewPool(ctx context.Context, rawDSN string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(rawDSN)
	if err != nil {
		return nil, newDBError(CodeDBUsage, "cannot parse the database DSN for the pool").
			withWhy(err.Error()).
			withNext("use the URL form: postgres://user:password@host:port/dbname?sslmode=disable")
	}
	cfg.MaxConns = DefaultPoolMaxConns
	cfg.MinConns = DefaultPoolMinConns
	cfg.MaxConnLifetime = DefaultPoolMaxConnLifetime
	cfg.MaxConnIdleTime = DefaultPoolMaxConnIdleTime
	cfg.HealthCheckPeriod = DefaultPoolHealthCheck

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, unreachableError(err, rawDSN)
	}
	pingCtx, cancel := context.WithTimeout(ctx, reachabilityTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, unreachableError(err, rawDSN)
	}
	return pool, nil
}
