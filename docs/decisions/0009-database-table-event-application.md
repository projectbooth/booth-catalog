# 0009: `database.table.*` event payload and application (ADR 0116)

Status: **implemented, matches booth-database's real shape**. First built against a provisional
shape (ADR 0116 §1, before booth-database's session reported one); revised once the ADR's
"Built as" section recorded booth-database PR #13's real event shape and deep-link routes. The
notes below describe what was actually built, plus the five real implementation decisions ADR
0116 left open — modeled on how `docs/decisions/0007` was flagged before ADR 0086 ratified it.

## Decision

1. **Wire shape, exactly as ADR 0116's "Built as" section records it:**
   ```json
   { "workspace": "acme", "eventType": "database.table.created", "publishedAt": "…",
     "publishedBy": "database",
     "data": {
       "schema": "nfl_data", "name": "players", "kind": "table", "comment": "the NFL roster",
       "columns": [{ "name": "player_id", "type": "bigint", "nullable": false, "comment": null },
                   { "name": "display_name", "type": "text", "nullable": true, "comment": "shown in the UI" }],
       "approxRows": 1696, "schemaHash": "sha256:…" } }
   ```
   `database.table.deleted` carries only `schema` and `name`. `kind` is `"table"` (partitioned
   tables included), `"view"` or `"materialized_view"` (partitions and foreign tables are
   never published at all). A column's `comment` maps onto `data.Column.Description` — the
   same concept under `table.*`'s and a hand-typed schema's own naming, so no new JSON key was
   introduced for it. `schemaHash` is read nowhere here (last-writer-wins by `publishedAt`
   already orders events correctly, and delivery is at-least-once with a stable `Nats-Msg-Id`,
   so this consumer's existing idempotent upsert — matching by identity, applying only a
   genuinely newer `publishedAt` — already handles redelivery for free); it round-trips as an
   ordinary unknown field.
2. **The table/view's own `comment` becomes the row's `Description` — but only for a row
   nobody hand-typed a description for.** Not in the original ADR 0116 build item explicitly,
   but a natural reading of "merged, not duplicated" (point 5): a hand-registered row's own
   typed description is never touched (`source = 'manual'` keeps it, in both `MemoryStore` and
   the SQL `CASE` in `PostgresStore.ApplyPostgresTable`); an auto row's `Description` tracks
   the comment on every event, including a `null` comment clearing it. This is reversible —
   nothing currently reads `Description` as authoritative for an auto row in a way a later
   correction couldn't fix.
3. **`kind` stayed an opaque, non-empty string (`data.TableKind`), not a closed Go enum**,
   even once the real three values were known — harmless either way now, and it costs nothing
   to stay lenient if booth-database ever adds a fourth kind. `TableKindMaterializedView`'s
   value is `"materialized_view"` (underscore), matching booth-database's real spelling, not
   the earlier provisional guess (`"materialized view"`, a space).
4. **Identity is `(workspace, schema, name)`, never a UUID** — unlike `table.*`'s
   `(workspace, source_module, table.uuid)` (ADR 0086 item 1): a real Postgres table has no
   UUID, so a rename is a new identity here, the same as if a person renamed a hand-registered
   `format: "postgres"` row by hand. No rename-tracking was built; out of scope unless raised.
5. **Merging into an existing row — hand-registered or previously auto-created — never
   changes `Name`, `Tags`, `Owner`, or `Source`.** Only `Columns` (the JSON field `schema`),
   `Kind`, `ApproxRows` and (point 2's carve-out aside) `Description` refresh, and `Missing`
   clears. An alternative was letting a merge adopt the introspected name over a hand-typed
   one, rejected because ADR 0116 point 5 says hand-registered rows are "merged into", read
   here as *enriched*, not *overwritten* — the person's own naming survives exactly as
   ADR 0102's existing fields already do.
6. **A hand-registered row's manual editability survives being merged into.** `Source` stays
   whatever it already was; a brand new identity (no row to merge into) gets `Source: "auto"`.
   `Service.Update`/`Delete` refuse a `Source: "auto"` row exactly like a `format: "iceberg"`
   one (`asset.ErrManagedExternally`), with ADR 0116's one named exception: deleting a
   `Missing: true` auto row requires the caller's workspace role to be `owner`, not merely
   `editor` — a narrower rule than the route's existing editor-or-owner gate, so it's enforced
   inside `Service.Delete` itself (a new `asset.ErrForbidden` sentinel → HTTP 403) rather than
   by widening that gate.
7. **`Missing` keeps the row fully visible — `Get`/`List`/`Search`/`ContainingLocation` all
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
8. **Deep links, exactly as ADR 0116's "Built as" section records them** (schema and name each
   URL-encoded): `/database/tables/<schema>/<name>` for "Preview rows" (the table page, which
   includes a 100-row preview) and `/database/sql?schema=<schema>&name=<name>` for "Open in
   SQL console" — note `name`, not `table`, as the second query parameter; an earlier
   provisional build guessed both paths wrong (a nested `/preview` suffix, and `table=` instead
   of `name=`) before this was corrected. Both stay in exactly `web/src/format.ts`'s
   `previewRowsPath`/`sqlConsolePath` — nowhere else in this package builds one by hand — so a
   future route change is a one-place edit. Whether to show the links at all is decided by
   calling `booth-core`'s `GET /api/modules` (the same registry `booth-module-store` and the
   shell itself read) and checking for an entry with `id: "database"` — the same "unavailable
   degrades to hidden" shape `checkLocation` already uses for `booth-storage`.

## Consequences

- `internal/data`: `Dataset` gains `Source`/`Kind`/`ApproxRows`/`Missing` (meaningful only for
  `FormatPostgres`); `Column` gains `Nullable`; `PostgresTableUpsert` (including `Comment`) and
  its `Normalize()`; `Store.ApplyPostgresTable`/`RemovePostgresTable`, implemented over both
  `MemoryStore` and `PostgresStore`; `Service.Create` stamps `Source: "manual"` for a
  hand-registered `format: "postgres"` row; `Service.Update`/`Delete` gain the
  `Source: "auto"` guard (`Delete`'s signature gains an `isOwner bool` parameter for the one
  exception above).
- Migration `0004_database_table_events.sql`: `source`/`kind`/`approx_rows`/`missing` columns
  and the new partial unique index described in point 7. No separate `comment` column was
  added — point 2's mapping reuses the existing `description` column.
- `internal/events/database_tables.go`: a third `Subscriber`/durable consumer/`Processor`,
  parallel to `tables.go`'s `table.*` handling, over the `booth.*.database.table.*` subject
  filter (five dot-separated tokens, one more than `dashboard.*`/`table.*`, since
  `database.table.<verb>` is itself a two-dot event type per ADR 0026).
- `charts/booth-catalog/templates/boothmodule.yaml`: `events.subscribe` gains
  `database.table.*`.
- `hack/publish-database-table-event`: a dev-tooling event publisher mirroring
  `hack/publish-dashboard-event`, matching the real shape above (including `-comment`).
- Web UI: `DataDetail.tsx`/`DataList.tsx` show kind, an "about N rows" count (never an exact
  claim, per ADR 0116's own risk note on planner-estimate staleness), and a greyed "Missing
  from the database" state; the two deep links per point 8; Edit/Delete follow point 6's
  rules.
- `contracts/` (the catalog's dataset shape and the `database.table.*` event shape, if
  captured there) can now be written up from this file directly rather than needing a further
  round of correction — the shape here matches what booth-database actually ships.

## Alternatives considered

- **Reusing `table.*`'s `(source_module, uuid)` identity shape for database tables** —
  rejected: a real Postgres table has no UUID, and inventing one here would be the catalog
  fabricating identity `booth-database` never asserted.
- **Tombstoning a deleted database table the way `RemoveTable` does** — rejected by ADR 0116
  fork C directly: dashboards and APIs may still reference a table that disappeared, and a
  tombstone's whole point (hide it from every read) is the opposite of what's needed.
- **Never touching `Description` from `comment` at all** (treating the two as fully
  unrelated) — rejected: the table-level `comment` is exactly the kind of fact a `Description`
  already exists to hold, and a brand-new auto row would otherwise always start blank even
  when `booth-database` has something useful to say about it.
- **Letting `comment` overwrite a hand-registered row's `Description` unconditionally** —
  rejected: see point 2; a person's own typed description is exactly what "merged, not
  duplicated" (ADR 0116 point 5) is protecting, the same reasoning that already applies to
  `Name`/`Tags`/`Owner`.
- **A closed `kind` enum, rejecting an unrecognized value** — rejected: even with the real
  vocabulary now known, a hard rejection would make a real table permanently uncatalogable the
  first time `booth-database` adds a fourth kind.
