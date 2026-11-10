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

// helpers_test.go — shared test helpers.

import (
	"errors"
	"testing"

	"github.com/Lutfifakee-Project/GSEER/internal/gql/planner"
	"github.com/Lutfifakee-Project/GSEER/internal/gql/validator"
)

func mustEncode(t *testing.T, c planner.CursorPayload) string {
	t.Helper()
	return c.Encode()
}

func asPlannerError(err error, target **planner.Error) bool {
	return errors.As(err, target)
}

// pgFreeTextList pins the free-text field list to the validator's
// exported list — the single source of truth both dialect planners
// mirror (the PG planner pins itself to the same list).
func pgFreeTextList(t *testing.T) []string {
	t.Helper()
	return validator.FreeTextFields
}
