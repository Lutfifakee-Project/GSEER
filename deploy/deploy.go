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

// Package deploy embeds the Rev 6.7.2 hardened-deployment SQL artifacts
// (M-A materialization) so the gseer binary can execute them itself
// (M-B bootstrap/grants, M-C audit gate) while the same byte-identical
// files stay available for manual psql-based deployments.
//
// Every file below is PINNED: its SHA-256 is part of the owner register
// (Rev 6.7.2 §1 Table B; DEPLOYMENT.md §5) and is asserted byte-for-byte
// by internal/storage/migrations/artifacts_test.go (the AC-117
// artifact-hash check).  The files must never be edited in place; a new
// revision replaces them wholesale with new register hashes.
//
//	deploy/sql/gseer_v0672_bootstrap_roles.sql           step 1 — role bootstrap (refuse-first)
//	deploy/sql/gseer_v0672_runtime_grants_setup.sql      step 3 — the §5.2 grant matrix
//	deploy/sql/gseer_v0672_effective_privilege_audit.sql step 4 — the fail-closed audit (read-only)
package deploy

import (
	_ "embed"
)

// BootstrapRolesSQL is the pinned role-bootstrap script (step 1 of the
// hardened deployment order).  Refuse-first: it creates gseer_policy_owner
// (NOLOGIN) and gseer_app (LOGIN) when absent, validates unsafe attribute
// sets on pre-existing roles, and grants the D-47 membership
// (gseer_policy_owner TO CURRENT_USER WITH SET TRUE, INHERIT TRUE).
//
//go:embed sql/gseer_v0672_bootstrap_roles.sql
var BootstrapRolesSQL string

// RuntimeGrantsSQL is the pinned §5.2 runtime grant matrix (step 3).
// The pinned artifact spells the default database name (`ON DATABASE
// gseer`); deployment databases with another name get exactly one
// controlled substitution of that token by the storage runner — logged
// when it happens (internal/storage/postgres/hardened.go).
//
//go:embed sql/gseer_v0672_runtime_grants_setup.sql
var RuntimeGrantsSQL string

// EffectivePrivilegeAuditSQL is the pinned fail-closed pre-runtime
// audit (step 4).  Read-only: it mutates nothing; failures raise
// ERRCODE 'GSUPF' with the complete failure list in the message.
//
//go:embed sql/gseer_v0672_effective_privilege_audit.sql
var EffectivePrivilegeAuditSQL string
