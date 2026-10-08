-- Postgres-table datasets as a second Dataset format extension (ADR 0102): a format:
-- "postgres" row is registered by hand through the catalog's existing write API — unlike
-- format: "iceberg" (event-sourced only, ADR 0085) — and carries postgres_table_schema/name:
-- the schema and table name of a real table in the workspace's own database (one database
-- per workspace, ADR 0081, so no database name is stored here). There is no required
-- location: a Postgres table isn't a booth-storage reference, so backend_id/path stay '' for
-- these rows. The catalog neither verifies the table exists nor reads its data (ADR 0102
-- item 4) — whoever reads it (booth-api) introspects the real table and treats this as
-- descriptive only.
ALTER TABLE datasets
    ADD COLUMN postgres_table_schema TEXT NOT NULL DEFAULT '',
    ADD COLUMN postgres_table_name   TEXT NOT NULL DEFAULT '';

-- 0002_iceberg_tables.sql's format CHECK only allowed ('file', 'iceberg'); widen it rather
-- than add a second, redundant constraint. Postgres has no ALTER CONSTRAINT, so drop and
-- recreate under the same (its default, unnamed-constraint) name.
ALTER TABLE datasets DROP CONSTRAINT datasets_format_check;
ALTER TABLE datasets ADD CONSTRAINT datasets_format_check CHECK (format IN ('file', 'iceberg', 'postgres'));
