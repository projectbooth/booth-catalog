# 0008: `format: "postgres"` field names and API shape (ADR 0102)

Status: **proposed**. ADR 0102 fixed the shape in prose ("carries `table: {schema, name}`…
its own field, not a reuse of the Iceberg `table` block") and asked this repo to report the
final field names for the coordinator to update `contracts/` with. This is that report.

## Decision

- **`Dataset.postgresTable`**, not a reuse of `table` (Iceberg's field, ADR 0085). The two
  blocks are never interchangeable — a reader branches on `format` to know which one (if
  either) is populated — and Go's `encoding/json` can't map two struct fields onto one JSON
  key anyway, so "its own field" became a literal, separate key: `postgresTable: {schema,
  name}`. `table` is untouched; existing Iceberg consumers see no change.
- **`Dataset.format` gains `"postgres"`** alongside the existing `"file"` and `"iceberg"`.
  Still a plain string: an older client, or a future format this build doesn't know about
  either way, round-trips it untouched (ADR 0102 item 3's "must be ignorable, never an
  error" — already true structurally, nothing new needed for it).
- **`Input` (the write API body) gains `format` and `postgresTable`**, mirroring `Dataset`:
  - `format`: `"file" | "postgres"` — `""`/omitted means `"file"` (every existing client that
    predates this field keeps working unchanged). `"iceberg"` is refused with a 422 on
    `field: "format"`: that format is event-sourced only (`ApplyTable`), never created or
    edited through this write API, now or after this change.
  - `postgresTable: {schema, name}` — **required** when `format: "postgres"`, and **refused**
    (422, `field: "postgresTable"`) when `format` is anything else. Symmetrically, a
    non-empty `location` is refused (422, `field: "location"`) when `format: "postgres"` —
    ADR 0102 item 1's "no required location" is enforced as "no location, period" for this
    format, not merely optional, so a client never wonders why a location it sent had no
    effect.
  - `schema`/`name` inside `postgresTable` are each required, ≤63 bytes (Postgres's own
    identifier limit), and must look like an unquoted Postgres identifier
    (`^[A-Za-z_][A-Za-z0-9_]*$`). The catalog never connects to the real database (ADR 0102
    item 4), so this is deliberately just "could this plausibly be one" — a quoted, Unicode or
    mixed-case-preserved identifier is rejected rather than guessed at; `booth-database`/
    `booth-api` validate against the real object when they introspect it.
- **Format is as mutable as any other field via `PUT`.** A `format: "postgres"` row can be
  edited back to `"file"` (and vice versa) through the same write API — both are manually
  managed, unlike `"iceberg"`, which no `PUT`/`DELETE` ever touches (`asset.ErrManagedExternally`,
  unchanged). Switching clears the other format's fields: `location` resets to the zero value
  switching to `"postgres"`, `postgresTable` clears to absent switching to `"file"`.

## Wire example

```json
POST /api/datasets
{
  "name": "orders",
  "format": "postgres",
  "postgresTable": { "schema": "public", "name": "orders" }
}
```

```json
201
{
  "id": "…", "name": "orders", "description": "", "owner": "ed@example.com",
  "format": "postgres",
  "postgresTable": { "schema": "public", "name": "orders" },
  "location": { "backendId": "", "path": "" },
  "schema": [], "tags": [],
  "createdBy": "sub-ed", "createdAt": "…", "updatedAt": "…"
}
```

`location` is present (it's not an optional field on `Dataset`) but always the zero value for
this format — there is no database name stored (ADR 0102 item 1: one database per workspace,
ADR 0081), and no backend/path, since this isn't a `booth-storage` reference.

## Consequences

- `internal/data`: `Format` gains `FormatPostgres`; `PostgresTableRef{Schema, Name}`;
  `Dataset.PostgresTable`/`Input.Format`/`Input.PostgresTable`; `Input.Normalize()`
  cross-validates both formats strictly. `Service.Create`/`Update` carry the new fields
  through; `Update`'s existing `FormatIceberg` → `ErrManagedExternally` guard is unchanged
  and untouched by this.
- Migration `0003_postgres_tables.sql`: `postgres_table_schema`/`postgres_table_name` columns
  (both default `''`), and the `format` `CHECK` constraint (from `0002_iceberg_tables.sql`)
  widened to include `'postgres'`.
- Web UI (`DatasetForm.tsx`): a Format picker; picking "postgres" swaps the location picker
  for two text inputs (schema, table name) and clears the other format's fields on switch.
  `DataList.tsx`/`DataDetail.tsx` show a "Postgres" badge and the schema.name in place of a
  location; Edit/Delete stay enabled (unlike Iceberg).
- `contracts/` (the catalog's dataset shape, if captured there) needs `format: "postgres"` and
  `postgresTable: {schema, name}` added alongside the ADR 0085 `table` entry.

## Alternatives considered

- **Reusing `Dataset.table` for both formats** (a tagged union keyed by `format`) — rejected by
  ADR 0102's own text, and impossible to do cleanly in Go/JSON without hand-written
  (un)marshaling anyway, for a benefit (one fewer field) that doesn't offset the lost clarity.
- **A nested database name in `postgresTable`** — rejected: ADR 0102 item 1 is explicit that
  there's one database per workspace (ADR 0081), so a database name would always be redundant
  and could drift from the truth if a workspace's database were ever reprovisioned under a
  different name.
- **Allowing a location alongside `postgresTable`** (treating ADR 0102's "no required
  location" as "optional" rather than "inapplicable") — rejected: nothing a location would
  describe for a database table, and allowing one invites exactly the confusion ADR 0102's
  "no database name is stored" line is already guarding against for the database case.
