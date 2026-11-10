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

package sqlite

// collection.go — the source registry + collection bookkeeping, a 1:1
// port of the postgres.Store's collection.go subset the collect runner
// interface needs (SQ-06 keeps these inside the SAME enforcement
// model: the registry feeds the gate, jobs/checkpoints/dead letters
// are pure bookkeeping).

import (
        "context"
        "database/sql"
        "encoding/json"
        "fmt"
        "time"

        "github.com/Lutfifakee-Project/GSEER/internal/storage"
)

// DataSourceRow is the registry row — the storage-package canonical type
// (v0.3.0: both engines satisfy collect.Backend with one vocabulary).
type DataSourceRow = storage.DataSourceRow

// ErrSourceNotFound — registry lookup miss (storage-level sentinel).
var ErrSourceNotFound = storage.ErrSourceNotFound

// EnsureSource registers a data source (idempotent by name).
func (s *Store) EnsureSource(ctx context.Context, name, connector, termsRef string, cfg map[string]any) (int64, error) {
        cfgJSON := "{}"
        if cfg != nil {
                b, err := json.Marshal(cfg)
                if err != nil {
                        return 0, fmt.Errorf("E_DB_USAGE: source config not JSON-serializable: %w", err)
                }
                cfgJSON = string(b)
        }
        var id int64
        err := withTx(ctx, s.DB, func(tx Querier) error {
                err := tx.QueryRowContext(ctx, `SELECT id FROM data_sources WHERE name = ?`, name).Scan(&id)
                switch {
                case err == nil:
                        _, err = tx.ExecContext(ctx, `UPDATE data_sources
                                SET connector = ?, terms_ref = ?, config = ? WHERE id = ?`,
                                connector, termsRef, cfgJSON, id)
                        return err
                case isNoRows(err):
                        res, ierr := tx.ExecContext(ctx, `INSERT INTO data_sources
                                (name, connector, terms_ref, config, enabled, created_at)
                                VALUES (?, ?, ?, ?, 0, ?)`,
                                name, connector, termsRef, cfgJSON, FormatTS(time.Now().UTC()))
                        if ierr != nil {
                                return ierr
                        }
                        id, ierr = res.LastInsertId()
                        return ierr
                default:
                        return err
                }
        })
        if err != nil {
                return 0, fmt.Errorf("sqlite: cannot ensure source %q: %w", name, err)
        }
        return id, nil
}

// GetSource resolves one registered source by name.
func (s *Store) GetSource(ctx context.Context, name string) (DataSourceRow, error) {
        row, ok, err := scanSource(s.DB.QueryRowContext(ctx, `
SELECT id, name, connector, terms_ref, config, enabled, created_at
FROM data_sources WHERE name = ?`, name))
        if err != nil {
                return DataSourceRow{}, err
        }
        if !ok {
                return DataSourceRow{}, ErrSourceNotFound
        }
        return row, nil
}

// ListSources returns every registered source.
func (s *Store) ListSources(ctx context.Context) ([]DataSourceRow, error) {
        rows, err := s.DB.QueryContext(ctx, `
SELECT id, name, connector, terms_ref, config, enabled, created_at
FROM data_sources ORDER BY id`)
        if err != nil {
                return nil, fmt.Errorf("sqlite: cannot list sources: %w", err)
        }
        defer func() { _ = rows.Close() }()
        var out []DataSourceRow
        for rows.Next() {
                r, ok, err := scanSourceRow(rows)
                if err != nil {
                        return nil, err
                }
                if ok {
                        out = append(out, r)
                }
        }
        return out, rows.Err()
}

// SetSourceEnabled flips a source's enabled flag (the enable/disable
// gate the runner checks before collecting).
func (s *Store) SetSourceEnabled(ctx context.Context, name string, enabled bool) error {
        res, err := s.DB.ExecContext(ctx, `UPDATE data_sources SET enabled = ? WHERE name = ?`, boolToInt(enabled), name)
        if err != nil {
                return fmt.Errorf("sqlite: cannot update source %q: %w", name, err)
        }
        n, err := res.RowsAffected()
        if err != nil {
                return err
        }
        if n == 0 {
                return ErrSourceNotFound
        }
        return nil
}

func scanSource(row *sql.Row) (DataSourceRow, bool, error) {
        var (
                r        DataSourceRow
                enabled  int
                createdS string
        )
        err := row.Scan(&r.ID, &r.Name, &r.Connector, &r.TermsRef, &r.ConfigJSON, &enabled, &createdS)
        if isNoRows(err) {
                return DataSourceRow{}, false, nil
        }
        if err != nil {
                return DataSourceRow{}, false, err
        }
        r.Enabled = enabled != 0
        if r.CreatedAt, err = ParseTS(createdS); err != nil {
                return DataSourceRow{}, false, err
        }
        return r, true, nil
}

func scanSourceRow(rows *sql.Rows) (DataSourceRow, bool, error) {
        var (
                r        DataSourceRow
                enabled  int
                createdS string
        )
        err := rows.Scan(&r.ID, &r.Name, &r.Connector, &r.TermsRef, &r.ConfigJSON, &enabled, &createdS)
        if err != nil {
                return DataSourceRow{}, false, err
        }
        r.Enabled = enabled != 0
        if r.CreatedAt, err = ParseTS(createdS); err != nil {
                return DataSourceRow{}, false, err
        }
        return r, true, nil
}

// StartCollectionJob opens a collection_jobs row (status running).
func (s *Store) StartCollectionJob(ctx context.Context, sourceID int64, kind string) (int64, error) {
        res, err := s.DB.ExecContext(ctx, `INSERT INTO collection_jobs
                (source_id, kind, status, started_at) VALUES (?, ?, 'running', ?)`,
                sourceID, kind, FormatTS(time.Now().UTC()))
        if err != nil {
                return 0, fmt.Errorf("sqlite: cannot start collection job: %w", err)
        }
        return res.LastInsertId()
}

// FinishCollectionJob closes a collection_jobs row.
func (s *Store) FinishCollectionJob(ctx context.Context, jobID int64, status string, stats map[string]any, errMsg string) error {
        statsJSON := "{}"
        if stats != nil {
                if b, err := json.Marshal(stats); err == nil {
                        statsJSON = string(b)
                }
        }
        _, err := s.DB.ExecContext(ctx, `UPDATE collection_jobs
                SET status = ?, stats = ?, error = ?, finished_at = ? WHERE id = ?`,
                status, statsJSON, errMsg, FormatTS(time.Now().UTC()), jobID)
        if err != nil {
                return fmt.Errorf("sqlite: cannot finish collection job %d: %w", jobID, err)
        }
        return nil
}

// GetCheckpoint reads one source checkpoint ("" when unset).
func (s *Store) GetCheckpoint(ctx context.Context, sourceID int64, key string) (string, error) {
        var cursor string
        err := s.DB.QueryRowContext(ctx,
                `SELECT cursor FROM source_checkpoints WHERE source_id = ? AND key = ?`,
                sourceID, key).Scan(&cursor)
        if isNoRows(err) {
                return "", nil
        }
        return cursor, err
}

// SetCheckpoint upserts one source checkpoint.
func (s *Store) SetCheckpoint(ctx context.Context, sourceID int64, key, cursor string) error {
        _, err := s.DB.ExecContext(ctx, `
INSERT INTO source_checkpoints (source_id, key, cursor, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(source_id, key) DO UPDATE SET cursor = excluded.cursor, updated_at = excluded.updated_at`,
                sourceID, key, cursor, FormatTS(time.Now().UTC()))
        if err != nil {
                return fmt.Errorf("sqlite: cannot set checkpoint: %w", err)
        }
        return nil
}

// AddDeadLetter records one failed batch (audit + retry surface).
func (s *Store) AddDeadLetter(ctx context.Context, sourceID int64, payload map[string]any, reason string) error {
        payloadJSON := "{}"
        if payload != nil {
                if b, err := json.Marshal(payload); err == nil {
                        payloadJSON = string(b)
                }
        }
        now := FormatTS(time.Now().UTC())
        _, err := s.DB.ExecContext(ctx, `INSERT INTO ingestion_dead_letter
                (source_id, payload, reason, status, created_at, updated_at)
                VALUES (?, ?, ?, 'pending', ?, ?)`, sourceID, payloadJSON, reason, now, now)
        if err != nil {
                return fmt.Errorf("sqlite: cannot add dead letter: %w", err)
        }
        return nil
}

// DeadLetterStats counts dead letters per source.
func (s *Store) DeadLetterStats(ctx context.Context) (map[int64]int, error) {
        rows, err := s.DB.QueryContext(ctx,
                `SELECT source_id, count(*) FROM ingestion_dead_letter GROUP BY source_id`)
        if err != nil {
                return nil, fmt.Errorf("sqlite: cannot read dead letter stats: %w", err)
        }
        defer func() { _ = rows.Close() }()
        out := map[int64]int{}
        for rows.Next() {
                var id int64
                var n int
                if err := rows.Scan(&id, &n); err != nil {
                        return nil, err
                }
                out[id] = n
        }
        return out, rows.Err()
}

// PendingDeadLetters returns up to limit pending dead letters of one
// source in the storage-package normalized shape: the payload JSON is
// decoded into the map the re-drive rebuild path consumes, and
// FirstSeen carries the row's creation time (the personal schema keeps
// no separate attempts/last_seen columns — re-drive order is by id,
// oldest first, matching the postgres path's oldest-first policy).
func (s *Store) PendingDeadLetters(ctx context.Context, sourceID int64, limit int) ([]storage.DeadLetter, error) {
        if limit <= 0 {
                limit = 50
        }
        rows, err := s.DB.QueryContext(ctx, `
SELECT id, source_id, payload, reason, created_at
FROM ingestion_dead_letter WHERE source_id = ? AND status = 'pending'
ORDER BY id LIMIT ?`, sourceID, limit)
        if err != nil {
                return nil, fmt.Errorf("sqlite: cannot read pending dead letters: %w", err)
        }
        defer func() { _ = rows.Close() }()
        var out []storage.DeadLetter
        for rows.Next() {
                var (
                r        storage.DeadLetter
                payloadS string
                createdS string
                )
                if err := rows.Scan(&r.ID, &r.SourceID, &payloadS, &r.Reason, &createdS); err != nil {
                        return nil, err
                }
                r.Payload = map[string]any{}
                if payloadS != "" && payloadS != "{}" {
                        if err := json.Unmarshal([]byte(payloadS), &r.Payload); err != nil {
                                return nil, fmt.Errorf("dead letter %d payload: %w", r.ID, err)
                        }
                }
                if r.FirstSeen, err = ParseTS(createdS); err != nil {
                        return nil, err
                }
                out = append(out, r)
        }
        return out, rows.Err()
}

// ResolveDeadLetter marks one dead letter resolved.
func (s *Store) ResolveDeadLetter(ctx context.Context, id int64, note string) error {
        _, err := s.DB.ExecContext(ctx, `UPDATE ingestion_dead_letter
                SET status = 'resolved', resolve_note = ?, updated_at = ? WHERE id = ?`,
                note, FormatTS(time.Now().UTC()), id)
        if err != nil {
                return fmt.Errorf("sqlite: cannot resolve dead letter %d: %w", id, err)
        }
        return nil
}

// FailDeadLetter marks one dead letter failed.
func (s *Store) FailDeadLetter(ctx context.Context, id int64, failure string) error {
        _, err := s.DB.ExecContext(ctx, `UPDATE ingestion_dead_letter
                SET status = 'failed', failure = ?, updated_at = ? WHERE id = ?`,
                failure, FormatTS(time.Now().UTC()), id)
        if err != nil {
                return fmt.Errorf("sqlite: cannot fail dead letter %d: %w", id, err)
        }
        return nil
}

// DiscardDeadLetters drops up to limit dead letters of one source and
// returns how many were removed.
func (s *Store) DiscardDeadLetters(ctx context.Context, sourceID int64, limit int) (int64, error) {
        if limit <= 0 {
                limit = 50
        }
        res, err := s.DB.ExecContext(ctx, `DELETE FROM ingestion_dead_letter
                WHERE source_id = ? AND id IN (SELECT id FROM ingestion_dead_letter WHERE source_id = ? ORDER BY id LIMIT ?)`,
                sourceID, sourceID, limit)
        if err != nil {
                return 0, fmt.Errorf("sqlite: cannot discard dead letters: %w", err)
        }
        return res.RowsAffected()
}

// IndexSeedValues returns up to limit asset values of the given kind,
// freshest first (seed_mode "index" — no third-party target list is
// ever consulted).
func (s *Store) IndexSeedValues(ctx context.Context, kind string, limit int) ([]string, error) {
        if limit <= 0 {
                limit = 100
        }
        rows, err := s.DB.QueryContext(ctx, `
SELECT value FROM assets WHERE kind = ?
ORDER BY last_seen DESC LIMIT ?`, kind, limit)
        if err != nil {
                return nil, fmt.Errorf("sqlite: cannot read index seeds: %w", err)
        }
        defer func() { _ = rows.Close() }()
        var out []string
        for rows.Next() {
                var v string
                if err := rows.Scan(&v); err != nil {
                        return nil, err
                }
                out = append(out, v)
        }
        return out, rows.Err()
}

// CollectionStatus returns the `gseer collect status` table: every
// registered source with its job count and pending dead-letter count
// (same rows the postgres path reports).
func (s *Store) CollectionStatus(ctx context.Context) ([]storage.CollectionStatusRow, error) {
        rows, err := s.DB.QueryContext(ctx, `
SELECT s.name, s.connector, s.enabled,
       (SELECT count(*) FROM collection_jobs j WHERE j.source_id = s.id) AS jobs,
       (SELECT count(*) FROM ingestion_dead_letter d WHERE d.source_id = s.id AND d.status = 'pending') AS pending
FROM data_sources s ORDER BY s.id`)
        if err != nil {
                return nil, fmt.Errorf("sqlite: cannot read collection status: %w", err)
        }
        defer func() { _ = rows.Close() }()
        var out []storage.CollectionStatusRow
        for rows.Next() {
                var r storage.CollectionStatusRow
                var enabled int
                if err := rows.Scan(&r.Name, &r.Connector, &enabled, &r.Jobs, &r.Pending); err != nil {
                        return nil, err
                }
                r.Enabled = enabled != 0
                out = append(out, r)
        }
        return out, rows.Err()
}

func boolToInt(b bool) int {
        if b {
                return 1
        }
        return 0
}
