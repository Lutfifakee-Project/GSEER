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

package plannersqlite

import (
	"net/netip"
)

// funcs.go — compile-time helpers shared by the SQL emission paths.
//
// The runtime counterparts of these helpers are registered as SQLite
// scalar functions by internal/storage/sqlite (gseer_ip_in_cidr,
// gseer_ts, gseer_now_micro); this file holds the pure-Go validation
// the planner needs to fail a bad IN_CIDR value at COMPILE time — the
// PG dialect fails at execution with a SQLSTATE, and personal mode
// must not silently turn a typo into an empty result.

// parsePrefix parses and masks a CIDR prefix ("198.51.100.0/24").
// A bare IP is NOT accepted: the operator writes the network explicitly.
func parsePrefix(s string) (netip.Prefix, netip.Addr, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, netip.Addr{}, err
	}
	m := p.Masked()
	return m, m.Addr(), nil
}

// ipInPrefix reports whether the canonical address text addrText is
// inside the masked prefix p.  Invalid inputs return false (the SQL
// function maps invalid → NULL → no match; here it feeds the compile-
// time error path only for the PREFIX value, never for column data).
func ipInPrefix(addrText string, p netip.Prefix) bool {
	a, err := netip.ParseAddr(addrText)
	if err != nil {
		return false
	}
	return p.Contains(a.Unmap())
}
