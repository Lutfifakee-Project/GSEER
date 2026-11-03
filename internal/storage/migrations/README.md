# GSEER migrations — authoring guidelines

The SQL files in this directory are embedded in the `gseer` binary
(`embed.go`) and applied by the golang-migrate runner
(`internal/storage/postgres/migrate.go`) on `gseer setup --self-hosted`
and `gseer server start`.  Users never hand-run SQL (SPEC-05 §1).

## Rules (normative)

1. **One atomic transaction per migration.**  The pgx/v5 driver runs
   with `MultiStatementMode=false`: every statement in a `.up.sql` file
   commits together or rolls back together.  Author every file so it is
   safe to run inside a transaction.

2. **Forbidden statements.**  These cannot run inside a PostgreSQL
   transaction and will fail the migration:
   `CREATE DATABASE`, `DROP DATABASE`, `CREATE TABLESPACE`,
   `DROP TABLESPACE`, `VACUUM`, `CLUSTER`, `REINDEX`,
   `CREATE INDEX CONCURRENTLY`, `ALTER SYSTEM`.
   If a future migration truly needs one, it must go in its own file and
   the runner must be extended deliberately (out of v0.0.1 scope).

3. **Naming.**  `NNNN_name.up.sql` / `NNNN_name.down.sql`.  The numeric
   prefix is the migration version (golang-migrate parses it).  Never
   renumber or edit an applied migration; always add a new one.

4. **schema_migrations is NOT ours.**  The golang-migrate driver creates
   and owns the `schema_migrations` table.  Application migrations must
   never create, drop, or modify it.  Recovery from a dirty state uses
   `gseer db migrate force --version N` (metadata-only, confirmation-
   gated) after manual database inspection via
   `gseer db migrate diagnose`.

5. **Every `.up.sql` has a matching `.down.sql`** that reverses it in
   FK-safe order, so `migrate force` recovery and test round-trips work.

6. **Collector-side tables are v0.0.2** (owner decisions D-M3-1/D-M3-3):
   `source_checkpoints`, `ingestion_dead_letter`, `scopes`,
   `collection_jobs`, `suppressions`.  `suppressions` MUST land before
   any first probe (v0.0.2 release gate).
