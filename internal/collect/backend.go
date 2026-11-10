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

// backend.go — the storage contract behind the collector engine
// (v0.3.0).  Historically the Runner was wired to the CONCRETE
// postgres.Store, which made collection impossible in personal mode.
// The contract is now an interface over the storage-package canonical
// types, satisfied by BOTH engines:
//
//   - postgres.Store  — the self-hosted profile (PostgreSQL 16+);
//   - sqlite.Store    — personal mode (one embedded SQLite file).
//
// The method set is exactly what the runner and the re-drive use —
// nothing more.  Compile-time assertions live in the cli package (the
// only place that binds both engines).

import (
	"context"

	"github.com/Lutfifakee-Project/GSEER/internal/storage"
)

// Backend is the collection bookkeeping + ingestion contract the Runner
// and Redrive need.  Implementations must return an error matching
// storage.ErrSourceNotFound from GetSource for an unknown source name.
type Backend interface {
	// GetSource resolves one registered source by name.
	GetSource(ctx context.Context, name string) (storage.DataSourceRow, error)
	// StartCollectionJob opens a 'running' job row and returns its id.
	StartCollectionJob(ctx context.Context, sourceID int64, kind string) (int64, error)
	// FinishCollectionJob closes a job with status + counters + error text.
	FinishCollectionJob(ctx context.Context, jobID int64, status string, stats map[string]any, errMsg string) error
	// IndexSeedValues returns up to limit asset values of the given kind,
	// freshest first (seed_mode "index").
	IndexSeedValues(ctx context.Context, kind string, limit int) ([]string, error)
	// GetCheckpoint reads one source checkpoint ("" when unset).
	GetCheckpoint(ctx context.Context, sourceID int64, key string) (string, error)
	// SetCheckpoint upserts one source checkpoint.
	SetCheckpoint(ctx context.Context, sourceID int64, key, cursor string) error
	// AddDeadLetter records one failed record (preserved, never dropped).
	AddDeadLetter(ctx context.Context, sourceID int64, payload map[string]any, reason string) error
	// IngestFacts applies a batch through the suppression-gated,
	// idempotent fact pipeline.
	IngestFacts(ctx context.Context, facts []storage.Fact) (storage.IngestStats, error)
	// IngestFact applies one fact (the poison-isolation re-drive path).
	IngestFact(ctx context.Context, f *storage.Fact) (storage.FactResult, error)
	// PendingDeadLetters lists pending dead letters (payload decoded).
	PendingDeadLetters(ctx context.Context, sourceID int64, limit int) ([]storage.DeadLetter, error)
	// ResolveDeadLetter marks one dead letter resolved.
	ResolveDeadLetter(ctx context.Context, id int64, note string) error
	// FailDeadLetter marks one dead letter failed again.
	FailDeadLetter(ctx context.Context, id int64, failure string) error
	// DiscardDeadLetters marks up to limit pending rows discarded.
	DiscardDeadLetters(ctx context.Context, sourceID int64, limit int) (int64, error)
}

// RegistryBackend is the `gseer source` management surface: the Backend
// contract plus the registry CRUD and the status table.  Both engines
// implement it; the CLI binds whichever profile's config selects.
type RegistryBackend interface {
	Backend
	// EnsureSource registers a data source (idempotent by name).
	EnsureSource(ctx context.Context, name, connector, termsRef string, cfg map[string]any) (int64, error)
	// ListSources returns every registered source.
	ListSources(ctx context.Context) ([]storage.DataSourceRow, error)
	// SetSourceEnabled flips the enablement flag (explicit operator action).
	SetSourceEnabled(ctx context.Context, name string, enabled bool) error
	// CollectionStatus returns the `gseer collect status` table.
	CollectionStatus(ctx context.Context) ([]storage.CollectionStatusRow, error)
}
