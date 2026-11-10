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

package postgres

// collection.go — the collector-side bookkeeping repositories (M14):
// collection_jobs, source_checkpoints and ingestion_dead_letter.
//
// The data_sources registry CRUD (EnsureSource) also lives here — the
// v0.0.2 connectors register reviewed sources with their terms_ref, and
// enablement stays an explicit operator action (nothing auto-enables).

import (
        "context"
        "encoding/json"
        "errors"
        "fmt"
        "time"

        "github.com/jackc/pgx/v5"

        "github.com/Lutfifakee-Project/GSEER/internal/storage"
)

// CollectionJob is one connector run row.
type CollectionJob struct {
        ID         int64
        SourceID   int64
        Kind       string
        Status     string
        StartedAt  time.Time
        FinishedAt *time.Time
        Stats      map[string]any
        Error      string
}

// StartCollectionJob opens a 'running' job row and returns its id.
func (s *Store) StartCollectionJob(ctx context.Context, sourceID int64, kind string) (int64, error) {
        var id int64
        err := s.pool.QueryRow(ctx, `
INSERT INTO collection_jobs (source_id, kind) VALUES ($1, $2) RETURNING id`,
                sourceID, kind).Scan(&id)
        return id, err
}

// FinishCollectionJob closes a job with status + counters + error text.
func (s *Store) FinishCollectionJob(ctx context.Context, jobID int64, status string, stats map[string]any, errMsg string) error {
        if stats == nil {
                stats = map[string]any{}
        }
        payload, err := json.Marshal(stats)
        if err != nil {
                return err
        }
        _, err = s.pool.Exec(ctx, `
UPDATE collection_jobs
SET status = $2, finished_at = now(), stats = $3::jsonb, error = $4
WHERE id = $1`, jobID, status, payload, errMsg)
        return err
}

// GetCheckpoint returns the stored cursor for (source, key); "" when none.
func (s *Store) GetCheckpoint(ctx context.Context, sourceID int64, key string) (string, error) {
        var cursor string
        err := s.pool.QueryRow(ctx,
                `SELECT cursor FROM source_checkpoints WHERE source_id = $1 AND key = $2`,
                sourceID, key).Scan(&cursor)
        if errors.Is(err, pgx.ErrNoRows) {
                return "", nil
        }
        return cursor, err
}

// SetCheckpoint stores the cursor for (source, key) upsert-style.
func (s *Store) SetCheckpoint(ctx context.Context, sourceID int64, key, cursor string) error {
        _, err := s.pool.Exec(ctx, `
INSERT INTO source_checkpoints (source_id, key, cursor, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (source_id, key) DO UPDATE SET cursor = EXCLUDED.cursor, updated_at = now()`,
                sourceID, key, cursor)
        return err
}

// AddDeadLetter preserves a record that could not be ingested.  An
// identical pending payload (same source + reason + payload hash) bumps
// attempts/last_seen instead of adding a new row (bounded growth under
// retry storms).
func (s *Store) AddDeadLetter(ctx context.Context, sourceID int64, payload map[string]any, reason string) error {
        raw, err := json.Marshal(payload)
        if err != nil {
                return err
        }
        // One statement, two behaviors (atomic; no read-modify-write race):
        // an identical pending payload bumps attempts/last_seen; otherwise
        // the record inserts fresh.
        _, err = s.pool.Exec(ctx, `
WITH bumped AS (
    UPDATE ingestion_dead_letter
       SET attempts = attempts + 1, last_seen = now()
     WHERE source_id = $1 AND status = 'pending' AND reason = $3 AND payload = $2::jsonb
    RETURNING 1
)
INSERT INTO ingestion_dead_letter (source_id, payload, reason)
SELECT $1, $2::jsonb, $3
WHERE NOT EXISTS (SELECT 1 FROM bumped)
  AND NOT EXISTS (
    SELECT 1 FROM ingestion_dead_letter
    WHERE source_id = $1 AND status = 'pending' AND reason = $3 AND payload = $2::jsonb
)`, sourceID, raw, reason)
        return err
}

// DeadLetterStats counts the pending dead letters per source.
func (s *Store) DeadLetterStats(ctx context.Context) (map[int64]int, error) {
        rows, err := s.pool.Query(ctx, `
SELECT source_id, count(*) FROM ingestion_dead_letter
WHERE status = 'pending' GROUP BY source_id`)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
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

// EnsureSource upserts a data_sources registry row and returns its id.
// Enablement is NEVER flipped here: it is an explicit operator decision
// (config + review status), never a side effect of registration.
func (s *Store) EnsureSource(ctx context.Context, name, connector, termsRef string, cfg map[string]any) (int64, error) {
        raw, err := json.Marshal(cfg)
        if err != nil {
                return 0, err
        }
        var id int64
        err = s.pool.QueryRow(ctx, `
INSERT INTO data_sources (name, connector, terms_ref, config, enabled)
VALUES ($1, $2, $3, $4::jsonb, false)
ON CONFLICT (name) DO UPDATE SET
    connector = EXCLUDED.connector,
    terms_ref = EXCLUDED.terms_ref,
    config    = EXCLUDED.config
RETURNING id`, name, connector, termsRef, raw).Scan(&id)
        return id, err
}

// GetSource loads one registered source by name.
func (s *Store) GetSource(ctx context.Context, name string) (DataSourceRow, error) {
        var ds DataSourceRow
        err := s.pool.QueryRow(ctx, `
SELECT id, name, connector, terms_ref, config, enabled, created_at
FROM data_sources WHERE name = $1`, name).Scan(
                &ds.ID, &ds.Name, &ds.Connector, &ds.TermsRef, &ds.ConfigJSON, &ds.Enabled, &ds.CreatedAt)
        if errors.Is(err, pgx.ErrNoRows) {
                return ds, ErrSourceNotFound
        }
        return ds, err
}

// ListSources returns every registered source.
func (s *Store) ListSources(ctx context.Context) ([]DataSourceRow, error) {
        rows, err := s.pool.Query(ctx, `
SELECT id, name, connector, terms_ref, config, enabled, created_at
FROM data_sources ORDER BY id`)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        var out []DataSourceRow
        for rows.Next() {
                var ds DataSourceRow
                if err := rows.Scan(&ds.ID, &ds.Name, &ds.Connector, &ds.TermsRef, &ds.ConfigJSON, &ds.Enabled, &ds.CreatedAt); err != nil {
                        return nil, err
                }
                out = append(out, ds)
        }
        return out, rows.Err()
}

// SetSourceEnabled flips the enablement flag — the explicit operator
// action.  The collect runner re-verifies the connector's review verdict
// before actually fetching, so a hand-flipped row cannot bypass the
// terms gate.
func (s *Store) SetSourceEnabled(ctx context.Context, name string, enabled bool) error {
        tag, err := s.pool.Exec(ctx, `UPDATE data_sources SET enabled = $2 WHERE name = $1`, name, enabled)
        if err != nil {
                return err
        }
        if tag.RowsAffected() == 0 {
                return ErrSourceNotFound
        }
        return nil
}

// DataSourceRow mirrors the data_sources row with the config kept as
// raw JSON (decoded by the connector layer).  v0.3.0: the canonical
// type lives in the storage package so both engines satisfy the
// collect.Backend contract with the exact same vocabulary.
type DataSourceRow = storage.DataSourceRow

// ErrSourceNotFound — registry lookup miss (storage-level sentinel).
var ErrSourceNotFound = storage.ErrSourceNotFound

// CollectionStatus returns the `gseer collect status` table: every
// registered source with its job count and pending dead-letter count.
func (s *Store) CollectionStatus(ctx context.Context) ([]storage.CollectionStatusRow, error) {
        rows, err := s.pool.Query(ctx, `
SELECT s.name, s.connector, s.enabled,
       (SELECT count(*) FROM collection_jobs j WHERE j.source_id = s.id) AS jobs,
       (SELECT count(*) FROM ingestion_dead_letter d WHERE d.source_id = s.id AND d.status = 'pending') AS pending
FROM data_sources s ORDER BY s.id`)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        var out []storage.CollectionStatusRow
        for rows.Next() {
                var r storage.CollectionStatusRow
                if err := rows.Scan(&r.Name, &r.Connector, &r.Enabled, &r.Jobs, &r.Pending); err != nil {
                        return nil, err
                }
                out = append(out, r)
        }
        return out, rows.Err()
}

// IndexSeedValues returns up to limit asset values of the given kind,
// freshest first (seed_mode "index": the connector enriches identifiers
// ALREADY in the operator's index — no third-party target list is ever
// consulted; P1 honesty contract 1).
func (s *Store) IndexSeedValues(ctx context.Context, kind string, limit int) ([]string, error) {
        if limit <= 0 {
                limit = 100
        }
        rows, err := s.pool.Query(ctx,
                `SELECT value FROM assets WHERE kind = $1 ORDER BY last_seen DESC, id ASC LIMIT $2`,
                kind, limit)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
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

// ---------------------------------------------------------------------------
// Dead-letter re-drive (v0.1.0 — roadmap "dead-letter re-drive")
// ---------------------------------------------------------------------------

// DeadLetterRecord is one pending dead-letter row handed to the re-drive
// (the storage-package normalized shape: the payload arrives decoded).
type DeadLetterRecord = storage.DeadLetter

// PendingDeadLetters lists pending dead-letter rows, oldest activity
// last (re-drive order: oldest first).  sourceID 0 = every source.
func (s *Store) PendingDeadLetters(ctx context.Context, sourceID int64, limit int) ([]DeadLetterRecord, error) {
        if limit <= 0 {
                limit = 500
        }
        sqlText := `SELECT id, source_id, payload, reason, attempts, first_seen, last_seen
FROM ingestion_dead_letter WHERE status = 'pending'`
        args := []any{}
        if sourceID > 0 {
                sqlText += ` AND source_id = $1`
                args = append(args, sourceID)
        }
        sqlText += fmt.Sprintf(` ORDER BY last_seen ASC LIMIT %d`, limit)
        rows, err := s.pool.Query(ctx, sqlText, args...)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        var out []DeadLetterRecord
        for rows.Next() {
                var r DeadLetterRecord
                var payload []byte
                if err := rows.Scan(&r.ID, &r.SourceID, &payload, &r.Reason, &r.Attempts, &r.FirstSeen, &r.LastSeen); err != nil {
                        return nil, err
                }
                if err := json.Unmarshal(payload, &r.Payload); err != nil {
                        return nil, fmt.Errorf("dead letter %d payload: %w", r.ID, err)
                }
                out = append(out, r)
        }
        return out, rows.Err()
}

// ResolveDeadLetter marks a dead letter resolved after a successful
// re-drive (the note appends to the reason for the audit trail).
func (s *Store) ResolveDeadLetter(ctx context.Context, id int64, note string) error {
        _, err := s.pool.Exec(ctx, `
UPDATE ingestion_dead_letter
   SET status = 'resolved', reason = reason || $2, last_seen = now()
 WHERE id = $1 AND status = 'pending'`, id, note)
        return err
}

// FailDeadLetter records a failed re-drive attempt (attempts bump, still
// pending — the record is never silently dropped).
func (s *Store) FailDeadLetter(ctx context.Context, id int64, failure string) error {
        _, err := s.pool.Exec(ctx, `
UPDATE ingestion_dead_letter
   SET attempts = attempts + 1, last_seen = now(),
       reason = CASE WHEN position(' | redrive:' in reason) = 0
                     THEN reason || ' | redrive:' || $2
                     ELSE reason END
 WHERE id = $1 AND status = 'pending'`, id, failure)
        return err
}

// DiscardDeadLetters marks pending rows discarded (operator's explicit
// judgment that a record will never apply — e.g. a malformed payload
// from a retired source).  Returns the number of rows marked.
func (s *Store) DiscardDeadLetters(ctx context.Context, sourceID int64, limit int) (int64, error) {
        if limit <= 0 {
                limit = 500
        }
        sqlText := `WITH picked AS (
    SELECT id FROM ingestion_dead_letter WHERE status = 'pending'`
        args := []any{}
        if sourceID > 0 {
                sqlText += ` AND source_id = $1`
                args = append(args, sourceID)
        }
        sqlText += fmt.Sprintf(` ORDER BY last_seen ASC LIMIT %d)
UPDATE ingestion_dead_letter d SET status = 'discarded', last_seen = now()
FROM picked WHERE d.id = picked.id`, limit)
        tag, err := s.pool.Exec(ctx, sqlText, args...)
        if err != nil {
                return 0, err
        }
        return tag.RowsAffected(), nil
}
