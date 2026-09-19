-- The dashboard catalog (ADR 0018): dashboards indexed from dashboard.* events published by
-- booth-superset, booth-metabase and booth-streamlit, scoped by workspace (ADR 0008).
--
-- A dashboard's identity is (workspace, source_module, external_id): the publishing module and
-- the dashboard's ID inside that tool. id is the catalog's own stable ID, what the API and UI
-- use.
CREATE TABLE dashboards (
    workspace        TEXT        NOT NULL,
    id               TEXT        NOT NULL,
    source_module    TEXT        NOT NULL,
    external_id      TEXT        NOT NULL,
    name             TEXT        NOT NULL DEFAULT '',
    description      TEXT        NOT NULL DEFAULT '',
    owner            TEXT        NOT NULL DEFAULT '',
    path             TEXT        NOT NULL DEFAULT '',
    lineage_complete BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at       TIMESTAMPTZ NOT NULL,
    -- The publishedAt of the latest event applied. Events arrive at-least-once and possibly
    -- out of order, so an event is applied only if it is not older than this.
    updated_at       TIMESTAMPTZ NOT NULL,
    -- Set by a dashboard.deleted event. The row is kept as a tombstone so that a stale
    -- created/updated arriving after the delete (out-of-order redelivery) cannot resurrect it.
    deleted_at       TIMESTAMPTZ,
    PRIMARY KEY (workspace, id),
    UNIQUE (workspace, source_module, external_id)
);

-- What a dashboard reads, as the publisher named it (see internal/dashboards.Source). The
-- catalog resolves these to datasets at read time, so a dataset registered after the
-- dashboard was indexed is connected without reprocessing anything. Replaced wholesale on
-- every created/updated event.
CREATE TABLE dashboard_sources (
    workspace    TEXT NOT NULL,
    dashboard_id TEXT NOT NULL,
    ordinal      INT  NOT NULL,
    type         TEXT NOT NULL CHECK (type IN ('dataset', 'location', 'external')),
    dataset_id   TEXT NOT NULL DEFAULT '',
    backend_id   TEXT NOT NULL DEFAULT '',
    -- Normalized like datasets.path: no leading or trailing '/', '' is the backend root.
    path         TEXT NOT NULL DEFAULT '',
    system       TEXT NOT NULL DEFAULT '',
    name         TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (workspace, dashboard_id, ordinal),
    FOREIGN KEY (workspace, dashboard_id) REFERENCES dashboards (workspace, id) ON DELETE CASCADE
);

-- "Which dashboards read this dataset?" looks up by dataset ID, or by backend for locations.
CREATE INDEX dashboard_sources_dataset_idx  ON dashboard_sources (workspace, dataset_id) WHERE type = 'dataset';
CREATE INDEX dashboard_sources_location_idx ON dashboard_sources (workspace, backend_id) WHERE type = 'location';
CREATE INDEX dashboards_owner_idx           ON dashboards (workspace, lower(owner)) WHERE deleted_at IS NULL;
