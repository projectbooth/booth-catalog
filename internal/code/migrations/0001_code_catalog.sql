-- The code catalog (ADR 0043): entries with an immutable, ordered version history, scoped
-- by workspace (ADR 0008). A registry only — nothing here executes or packages anything.
CREATE TABLE code_entries (
    workspace   TEXT        NOT NULL,
    id          TEXT        NOT NULL,
    name        TEXT        NOT NULL,
    description TEXT        NOT NULL DEFAULT '',
    owner       TEXT        NOT NULL,
    language    TEXT        NOT NULL DEFAULT '',
    created_by  TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (workspace, id),
    UNIQUE (workspace, name)
);

CREATE INDEX code_entries_owner_idx ON code_entries (workspace, lower(owner));

-- One row per published version. Rows are only ever inserted, never updated: a published
-- version is immutable, which is what lets a consumer pin one. seq is the publication
-- order within the entry ("latest" = highest seq), assigned under a lock on the entry row.
CREATE TABLE code_versions (
    workspace    TEXT        NOT NULL,
    entry_id     TEXT        NOT NULL,
    version      TEXT        NOT NULL,
    seq          INT         NOT NULL,
    notes        TEXT        NOT NULL DEFAULT '',
    source       TEXT        NOT NULL,
    published_by TEXT        NOT NULL DEFAULT '',
    published_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (workspace, entry_id, version),
    UNIQUE (workspace, entry_id, seq),
    FOREIGN KEY (workspace, entry_id) REFERENCES code_entries (workspace, id) ON DELETE CASCADE
);
