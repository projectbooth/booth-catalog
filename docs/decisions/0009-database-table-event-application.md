# 0009: `database.table.*` event payload and application (ADR 0116)

Status: **proposed**. ADR 0116 fixed the shape in prose (schema/name/kind/columns/approxRows/
schemaHash, identity `(workspace, schema, name)`, merge-not-duplicate, missing-not-deleted) and
is explicit that the field names are provisional until `booth-database`'s own session reports
them. This repo built against fixtures (`hack/publish-database-table-event`) rather than a real
publisher. This is that report — the field names and shapes to update `contracts/` with, plus
the five real implementation decisions this build made that the ADR left open, modeled on how
`docs/decisions/0007` was flagged before ADR 0086 ratified it.

## Decision

1. **Wire shape assumed**, pending `booth-database`'s own report:
   ```json
   { "workspace": "acme", "eventType": "database.table.created", "publishedAt": "…",
     "publishedBy": "database",
     "data": {
       "schema": "nfl_data", "name": "players", "kind": "table",
       "columns": [{ "name": "player_id", "type": "bigint", "nullable": false },
                   { "name": "display_name", "type": "text", "nullable": true, "comment": "shown in the UI" }],
       "approxRows": 1696, "schemaHash": "sha256:…" } }
   ```
   `database.table.deleted` carries only `schema` and `name`. A column's `comment` maps onto
   `data.Column.Description` — the same concept under `table.*`'s and a hand-typed schema's
   own naming, so no new JSON key was introduced for it. `schemaHash` is read nowhere here
   (last-writer-wins by `publishedAt` already orders events correctly); it round-trips as an
   ordinary unknown field.
2. **`kind` is an opaque, non-empty string (`data.TableKind`), not a closed enum.** The ADR
   names "table", "view" and "materialized view" today, but this build's `isRetryable`-style
   strictness felt wrong for a value whose full vocabulary isn't settled — a kind this build
   doesn't recognize yet still catalogs and displays rather than being dropped as invalid.
   Required and non-empty, bounded at 50 bytes (`maxKind`).
3. **Identity is `(workspace, schema, name)`, never a UUID** — unlike `table.*`'s
   `(workspace, source_module, table.uuid)` (ADR 0086 item 1): a real Postgres table has no
   UUID, so a rename is a new identity here, the same as if a person renamed a hand-registered
   `format: "postgres"` row by hand. No rename-tracking was built; out of scope unless raised.
4. **Merging into an existing row — hand-registered or previously auto-created — never
   changes `Name`, `Description`, `Tags`, `Owner`, or `Source`.** Only `Columns` (the JSON
   field `schema`), `Kind` and `ApproxRows` refresh, and `Missing` clears. This is stricter
   than it had to be: an alternative was letting a merge adopt the introspected name over a
   hand-typed one, rejected because ADR 0116 point 5 says hand-registered rows are "merged
   into", which this build read as *enriched*, not *overwritten* — the person's own naming
   survives exactly as ADR 0102's existing fields already do.
5. **A hand-registered row's manual editability survives being merged into.** `Source` stays
   whatever it already was; a brand new identity (no row to merge into) gets `Source: "auto"`.
   `Service.Update`/`Delete` refuse a `Source: "auto"` row exactly like a `format: "iceberg"`
   one (`asset.ErrManagedExternally`), with ADR 0116's one named exception: deleting a
   `Missing: true` auto row requires the caller's workspace role to be `owner`, not merely
   `editor` — a narrower rule than the route's existing editor-or-owner gate, so it's enforced
   inside `Service.Delete` itself (a new `asset.ErrForbidden` sentinel → HTTP 403) rather than
   by widening that gate.
6. **`Missing` keeps the row fully visible — `Get`/`List`/`Search`/`ContainingLocation` all
   still return it — unlike `table.*`'s `RemoveTable`, which hides a deleted row behind a
   tombstone.** This is ADR 0116 fork C taken literally ("visible, greyed, with its lineage
   intact"), and it meant `RemovePostgresTable` could not reuse `RemoveTable`'s tombstone
   pattern at all: a new `missing BOOLEAN` column, plus `source`/`kind`/`approx_rows`, and a
   new partial unique index `datasets_postgres_table_identity_idx` on
   `(workspace, postgres_table_schema, postgres_table_name) WHERE format = 'postgres'` — the
   `ON CONFLICT` target both `ApplyPostgresTable` and `RemovePostgresTable` upsert against,
   which a hand-registered row already participates in (it already carries
   `postgres_table_schema`/`name`, ADR 0102), and that's the mechanism that makes merging
   possible at all. A `database.table.deleted` for an identity never seen before still records
   a `Missing: true` placeholder (named `schema.name`, checked against the same
   `UNIQUE(workspace, name)` collision as a fresh `ApplyPostgresTable` would be), so a later
   out-of-order `created`/`updated` has a row to merge into rather than creating a duplicate.
7. **Deep links are a single, overridable pair of functions, not per-event URLs.** The ADR's
   payload carries no path field, and `booth-database`'s own route shapes for row preview and
   the SQL console weren't available to build against. `web/src/format.ts`'s
   `previewRowsPath`/`sqlConsolePath` assume `booth-database`'s `navPath` is `/database`
   (the fleet's one-word convention) and a plausible nested route underneath it — both
   constants live in exactly those two functions, nowhere else, so correcting them once
   `booth-database`'s real routes exist is a one-place change. Whether to show the links at
   all is decided by calling `booth-core`'s `GET /api/modules` (the same registry
   `booth-module-store` and the shell itself read) and checking for an entry with
   `id: "database"` — the same "unavailable degrades to hidden" shape `checkLocation` already
   uses for `booth-storage`.

## Consequences

- `internal/data`: `Dataset` gains `Source`/`Kind`/`ApproxRows`/`Missing` (meaningful only for
  `FormatPostgres`); `Column` gains `Nullable`; `PostgresTableUpsert`/`Normalize()`;
  `Store.ApplyPostgresTable`/`RemovePostgresTable`, implemented over both `MemoryStore` and
  `PostgresStore`; `Service.Create` stamps `Source: "manual"` for a hand-registered
  `format: "postgres"` row; `Service.Update`/`Delete` gain the `Source: "auto"` guard
  (`Delete`'s signature gains an `isOwner bool` parameter for the one exception above).
- Migration `0004_database_table_events.sql`: `source`/`kind`/`approx_rows`/`missing` columns
  and the new partial unique index described in point 6.
- `internal/events/database_tables.go`: a third `Subscriber`/durable consumer/`Processor`,
  parallel to `tables.go`'s `table.*` handling, over the `booth.*.database.table.*` subject
  filter (five dot-separated tokens, one more than `dashboard.*`/`table.*`, since
  `database.table.<verb>` is itself a two-dot event type per ADR 0026).
- `charts/booth-catalog/templates/boothmodule.yaml`: `events.subscribe` gains
  `database.table.*`.
- `hack/publish-database-table-event`: a dev-tooling event publisher mirroring
  `hack/publish-dashboard-event`, for building and demonstrating this before
  `booth-database`'s own publisher exists.
- Web UI: `DataDetail.tsx`/`DataList.tsx` show kind, an "about N rows" count (never an exact
  claim, per ADR 0116's own risk note on planner-estimate staleness), and a greyed "Missing
  from the database" state; the two deep links per point 7; Edit/Delete follow point 5's rules.
- `contracts/` (the catalog's dataset shape and the `database.table.*` event shape, if captured
  there) needs all of the above added once `booth-database`'s session reports its real field
  names and routes — at which point this file's "proposed" items get checked against reality
  the way ADR 0086 checked `docs/decisions/0007`.

## Alternatives considered

- **Reusing `table.*`'s `(source_module, uuid)` identity shape for database tables** —
  rejected: a real Postgres table has no UUID, and inventing one here would be the catalog
  fabricating identity `booth-database` never asserted.
- **Tombstoning a deleted database table the way `RemoveTable` does** — rejected by ADR 0116
  fork C directly: dashboards and APIs may still reference a table that disappeared, and a
  tombstone's whole point (hide it from every read) is the opposite of what's needed.
- **A closed `kind` enum, rejecting an unrecognized value** — rejected: the ADR's own three
  named values read as illustrative ("as booth-database's introspection reports it") rather
  than exhaustive, and a hard rejection would make a real table permanently uncatalogable the
  first time `booth-database` adds a fourth kind.
- **Letting a merge overwrite a hand-registered row's name with the introspected schema.name**
  — rejected: see point 4; a person's own naming is exactly what "merged, not duplicated"
  (ADR 0116 point 5) is protecting.
