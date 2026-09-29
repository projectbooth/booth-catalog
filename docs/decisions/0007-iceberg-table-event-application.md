# 0007: Applying `table.*` events — identity, naming, tombstoning, and the manual write API

Status: **proposed**. ADR 0085 fixed the shape (`Dataset` gains `format`/`table`, event-bus push
mirroring `dashboard.*`) and this repo's brief; the choices below are the implementation-level
decisions that shape needed but didn't spell out. Flagged for the coordinator the same way
[0001](0001-dashboard-event-payload.md) was before ADR 0046 ratified it.

## Context

ADR 0085 says `booth-lakehouse` publishes `table.created`/`updated`/`deleted`, payload "the table
summary minus `snapshots`, plus `workspace` from the envelope", and that this repo upserts or
deletes the corresponding `Dataset` row "the same way you subscribe to `dashboard.*` today". That
leaves several concrete questions `internal/dashboards`' precedent answers for dashboards but that
don't transfer to `Dataset` unchanged, because `Dataset` has a constraint dashboards never had:
`UNIQUE(workspace, name)`.

## Decisions

- **Identity is `(workspace, source_module, table.uuid)`, not name.** Mirrors
  `internal/dashboards`' `(workspace, sourceModule, externalId)` exactly, and for the same reason:
  a rename must be an update to the same row, not a new one plus an orphan. `source_module` is the
  event envelope's `publishedBy` ("lakehouse"), carried on the `Dataset` row as an unexported field
  — present for identity and provenance, never serialized (mirrors `SourceModule` on `Dashboard`).
- **The catalog name is `<namespace>.<table name>`.** Both are names within `booth-lakehouse`'s own
  catalog, so joining them is exactly what keeps two tables in different namespaces from colliding
  here the same way they don't collide there. This name is real, live in the UI, filters and search
  — not a placeholder.
- **A computed name colliding with an unrelated, already-registered dataset is `asset.ErrExists`,
  surfaced as a dropped event, not a retry.** `internal/dashboards` never had this case (no name
  uniqueness at all). `ApplyTable`'s `INSERT … ON CONFLICT` targets only the identity index; a
  conflict on `UNIQUE(workspace, name)` surfaces as a distinct Postgres error, mapped to
  `asset.ErrExists` and then `Drop` (never `Retry` — nothing about redelivering fixes a genuine
  name collision) by `events.TableProcessor`. This needs a human to rename one side; not attempted
  automatically. Expected to be rare (a hand-registered "file" dataset happening to share a
  `booth-lakehouse` table's `namespace.name`), and it's an intentionally loud failure that a
  Grafana log line records (`events: dropping …`) rather than a silent overwrite either way.
- **Deletes are tombstoned, exactly as `dashboard.deleted` is.** A hard delete was considered and
  rejected: at-least-once, out-of-order delivery means a `table.deleted` can race a late
  `table.created`/`updated` for the same table, and without a tombstone the late event would
  recreate a row the catalog just correctly removed. The tombstone (`deleted_at`) hides the row
  from every read path (`Get`/`List`/`Search`/`ContainingLocation`/`Tags`) the identical way
  `dashboards.deleted_at` does. A table the catalog never saw before its `deleted` event still gets
  a tombstone row, under a synthetic placeholder name (`__deleted_iceberg_table__:<uuid>`, never
  shown) — needed only because `UNIQUE(workspace, name)` means even a tombstone needs *some* unique
  name, unlike a dashboard's tombstone.
- **The manual write API (`PUT`/`DELETE /api/datasets/{id}`) refuses a `format: "iceberg"` row —
  `asset.ErrManagedExternally`, HTTP 409.** Not required by ADR 0085's text, but it exists only
  because `booth-lakehouse` published an event about it, so a manual edit would either be silently
  overwritten by the next `table.updated` or fight it. `internal/dashboards` reaches the same
  outcome by having no write API at all; `Dataset`'s manual API existed first, for `file` rows, so
  this is a per-row refusal rather than a route that was never built. The read side (browse,
  search, lineage, permissions) is completely unchanged, per ADR 0085.
- **A separate `Subscriber`/durable consumer/`Processor` for `table.*`, not folded into the
  existing dashboard one.** `nats.go`'s `SubscriberConfig` gained a `SubjectFilter` field (default:
  the dashboard pattern) so a second `Subscriber` can run the same transport code against
  `booth.*.table.*` and its own consumer name (`booth-catalog-tables`). Two independent failure
  domains: a bug or backlog in one event family's processing never blocks or slows the other, and
  each gets its own `/healthz`/`/config` status (`tableEventBus`/`tableEvents`, alongside the
  existing `eventBus`/`dashboardEvents`).
- **The manifest declares `events.subscribe: [dashboard.*, table.*]`.** Both subscriptions need
  their own NATS permission grant (ADR 0050) — this is not optional wiring, it's what makes the
  table subscription connect at all in a real deployment.

## Consequences

- `internal/data` gained `ApplyTable`/`RemoveTable` on its `Store` interface (both
  implementations), `TableUpsert`/`TableRef`/`Format` on the model, and the guard on
  `Service.Update`/`Delete`. Migration `0002_iceberg_tables.sql` adds the columns plus the partial
  unique index the identity upsert targets.
- `internal/events` gained `tables.go` (`TableProcessor`, parallel to `processor.go`'s
  `Processor`) and a `Handler` interface so `Subscriber` serves either.
- The web UI shows a small "Iceberg" badge in the dataset list and an "Iceberg table" section
  (namespace, table name, UUID, current snapshot) on the detail page, and hides Edit/Delete for a
  `format: "iceberg"` row.

## Alternatives considered

- **A separate `tables` table / asset type** — rejected by ADR 0085 itself.
- **Hard delete on `table.deleted`** — rejected above (resurrection risk).
- **Identity by `namespace.name` instead of `(source_module, uuid)`** — rejected: a rename would
  orphan the old row and create a new one, losing the dataset's catalog ID (and anything
  referencing it, e.g. a future lineage edge) across a rename that Iceberg itself considers the
  same table.
- **One `Subscriber` consuming both `dashboard.*` and `table.*` via `FilterSubjects` (plural)** —
  possible with a newer JetStream consumer API, but rejected for v0: it would mean one durable
  consumer's position, redelivery count and backlog conflating two unrelated event families, and
  a bug in one `Processor` (e.g. a panic recovered by consume's error handler) affecting delivery
  of the other. Two consumers is a few more lines of wiring for a real isolation property.
