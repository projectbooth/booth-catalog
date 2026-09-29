-- Iceberg tables as a Dataset format extension (ADR 0085): a format: "iceberg" row is
-- registered by booth-lakehouse's table.created/table.updated events and identified by
-- (workspace, source_module, table_uuid) rather than by name — a rename is just another
-- update, exactly as internal/dashboards identifies a dashboard by (source_module,
-- external_id) rather than by name. format: "file" rows (every dataset registered before
-- this, and everything the manual write API still creates) are untouched: the new columns
-- default to values that mean "not an Iceberg table".
ALTER TABLE datasets
    ADD COLUMN format TEXT NOT NULL DEFAULT 'file' CHECK (format IN ('file', 'iceberg')),
    -- Set only when format = 'iceberg'; empty otherwise. table_current_snapshot_id is NULL
    -- both when format = 'file' and when an Iceberg table has no snapshot yet (never
    -- committed to).
    ADD COLUMN table_namespace           TEXT NOT NULL DEFAULT '',
    ADD COLUMN table_name                TEXT NOT NULL DEFAULT '',
    ADD COLUMN table_uuid                TEXT NOT NULL DEFAULT '',
    ADD COLUMN table_current_snapshot_id BIGINT,
    -- The publishing module's manifest ID (e.g. "lakehouse"), the table.* event envelope's
    -- publishedBy. Empty for format = 'file'.
    ADD COLUMN source_module TEXT NOT NULL DEFAULT '',
    -- Set by a table.deleted event; the row is kept as a tombstone (internal/dashboards'
    -- identical pattern) so a stale created/updated arriving after the delete (out-of-order
    -- redelivery) cannot resurrect it. Always NULL for format = 'file': Delete removes that
    -- row outright, as it always has.
    ADD COLUMN deleted_at TIMESTAMPTZ;

-- The identity ApplyTable/RemoveTable upsert on. Partial: only format = 'iceberg' rows
-- participate, so it says nothing about the all-'' triple every format = 'file' row shares.
CREATE UNIQUE INDEX datasets_table_identity_idx ON datasets (workspace, source_module, table_uuid) WHERE format = 'iceberg';

-- Every existing read query gains "AND deleted_at IS NULL"; this is the index that keeps it cheap.
CREATE INDEX datasets_live_idx ON datasets (workspace) WHERE deleted_at IS NULL;
