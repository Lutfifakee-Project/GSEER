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

// registry.go — the built-in connector registry.  Each connector file
// registers itself via register() in init(); the runner looks connectors
// up by the data_sources.connector value.  Registering the same kind
// twice is a programming error (panic at startup, never at runtime).

var registry = map[string]Collector{}

// register adds one built-in connector.
func register(c Collector) {
	if _, dup := registry[c.Kind()]; dup {
		panic("collect: duplicate connector kind " + c.Kind())
	}
	registry[c.Kind()] = c
}

// Registry returns a copy of the built-in connector registry keyed by
// connector kind.  Review verdicts live with the connectors (see
// docs/SOURCES.md for the full review register).
func Registry() map[string]Collector {
	out := make(map[string]Collector, len(registry))
	for k, v := range registry {
		out[k] = v
	}
	return out
}
