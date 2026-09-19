-- The dataset catalog (ADR 0042): name, description, storage location (ADR 0045), schema,
-- tags and owner, scoped by workspace (ADR 0008).
--
-- The location is stored as the {backend_id, path} pair and nothing more: no URI, no
-- credentials, and no foreign key into booth-storage's database — resolving it is a call to
-- booth-storage's API, and the reference is allowed to go stale (ADR 0045).
CREATE TABLE datasets (
    workspace   TEXT        NOT NULL,
    id          TEXT        NOT NULL,
    name        TEXT        NOT NULL,
    description TEXT        NOT NULL DEFAULT '',
    backend_id  TEXT        NOT NULL,
    -- Normalized: no leading or trailing '/', and '' means the backend root.
    path        TEXT        NOT NULL,
    -- The JSON field is called "schema"; the column isn't, to stay clear of a SQL keyword.
    columns     JSONB       NOT NULL DEFAULT '[]',
    tags        TEXT[]      NOT NULL DEFAULT '{}',
    owner       TEXT        NOT NULL,
    created_by  TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (workspace, id),
    UNIQUE (workspace, name)
);

-- Location lookups (which datasets contain this path?) narrow by backend first.
CREATE INDEX datasets_location_idx ON datasets (workspace, backend_id);
CREATE INDEX datasets_owner_idx    ON datasets (workspace, lower(owner));
CREATE INDEX datasets_tags_idx     ON datasets USING GIN (tags);
