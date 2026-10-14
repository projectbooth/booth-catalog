-- booth-database's database.table.created/updated/deleted events (ADR 0116): a format:
-- "postgres" row gains source ("manual", the hand-registered write API's default (ADR 0102),
-- or "auto", set only by ApplyPostgresTable when it creates a row from scratch — merging into
-- an existing row, hand-registered or not, never changes this), kind (table/view/materialized
-- view, as booth-database's introspection reports it), approx_rows (Postgres's own planner
-- estimate, NULL when unknown -- ADR 0102 item 4 still holds: nothing here ever queries the
-- real table), and missing (true once database.table.deleted reports the table gone from the
-- real database; the row, its search entry and its lineage stay intact -- unlike format:
-- "iceberg"'s hidden tombstone, this is a visible state, fork C of ADR 0116).
ALTER TABLE datasets
    ADD COLUMN source      TEXT NOT NULL DEFAULT '' CHECK (source IN ('', 'manual', 'auto')),
    ADD COLUMN kind        TEXT NOT NULL DEFAULT '',
    ADD COLUMN approx_rows BIGINT,
    ADD COLUMN missing     BOOLEAN NOT NULL DEFAULT false;

-- Every format: 'postgres' row registered before this migration (ADR 0102, hand-registered
-- only at the time) is manual, same as any new hand-registered row defaults to.
UPDATE datasets SET source = 'manual' WHERE format = 'postgres';

-- The identity ApplyPostgresTable/RemovePostgresTable upsert on (ADR 0116): (workspace,
-- schema, table name), never a UUID -- a real Postgres table has none. This is also what lets
-- a matching hand-registered row be found and merged rather than duplicated: it already
-- carries postgres_table_schema/name (ADR 0102), so it participates in this same partial
-- index alongside an auto-discovered one.
CREATE UNIQUE INDEX datasets_postgres_table_identity_idx ON datasets (workspace, postgres_table_schema, postgres_table_name) WHERE format = 'postgres';
