# 0001: The `dashboard.created` / `updated` / `deleted` event payload

Status: **proposed** — needs the coordinator's sign-off, and agreement from the
`booth-superset`, `booth-metabase` and `booth-streamlit` agents, before it becomes a
contract addition. `ARCHITECTURE.md` §7 item 11 asks for exactly this: propose a shape and
bring it back rather than each side guessing. This repo already implements it
(`internal/events`, `internal/dashboards`), so the proposal is testable, not hypothetical —
but it is cheap to change now and expensive after three publishers ship against it.

## Context

ADR 0018 fixed the mechanism (each dashboard module pushes `dashboard.created` / `updated` /
`deleted` over the event bus) and ADR 0026 fixed the transport (subject
`booth.<workspace>.dashboard.<verb>`, envelope `{workspace, eventType, publishedAt,
publishedBy, data}`). What's left is `data`: how a dashboard is identified, and — the hard
part — how it says which datasets it reads.

The three publishers can say very different things about that:

- **Superset / Metabase** know a dashboard's charts and the database tables/queries behind
  them. Those are tables in a database the tool connects to, not catalog datasets. Only where
  the connection was set up from a catalog dataset can they name that dataset.
- **Streamlit** apps are arbitrary Python. Lineage is "whatever the app declares" (ADR 0018),
  most naturally the booth-storage locations it reads.

ADR 0018 also says a module that can't produce reliable lineage should flag it rather than
fabricate it. So the schema has to let a publisher say "I read this, and I can't tell you
which catalog dataset that is" without the catalog guessing.

## Proposed decision

### `dashboard.created` and `dashboard.updated` — identical shape, full current state

```json
{
  "workspace": "acme-analytics",
  "eventType": "dashboard.updated",
  "publishedAt": "2026-09-19T18:00:00.123456Z",
  "publishedBy": "superset",
  "data": {
    "dashboardId": "42",
    "name": "Quarterly Revenue",
    "description": "Revenue by region and product line",
    "owner": "alice@example.com",
    "path": "/superset/dashboard/42",
    "lineageComplete": true,
    "sources": [
      { "type": "dataset",  "datasetId": "6f1c2e0a-…" },
      { "type": "location", "backendId": "lake", "path": "warehouse/orders" },
      { "type": "external", "name": "public.orders", "system": "postgres://analytics" }
    ]
  }
}
```

| Field | Required | Meaning |
|---|---|---|
| `dashboardId` | yes | The dashboard's ID **inside the publishing tool**, as a string (≤200 chars). Identity is `(workspace, publishedBy, dashboardId)`. |
| `name` | yes | Display name (≤200). |
| `description` | no | Free text (≤4000). |
| `owner` | recommended | Who owns it, as the tool reports it (see [0003](0003-owner-identity.md)). |
| `path` | no | Shell-relative path that opens the dashboard, under the publisher's own `navPath` — publishers know their `navPath`, not the shell's origin. Must start with a single `/`; anything else is refused. |
| `lineageComplete` | no, default `false` | The publisher's assertion that `sources` lists **everything** the dashboard reads. Leave it `false` unless it is actually true. |
| `sources` | no, default `[]` | What the dashboard reads; see below. At most 200. |

`created` and `updated` are **the same upsert**: each carries the dashboard's *entire*
current state, and a consumer treats them identically. That is deliberate — it makes the
protocol survive the failures at-least-once delivery actually produces (a missed `created`,
a catalog installed after the dashboards existed, redelivery): an `updated` for a dashboard
the consumer has never seen simply creates it. Publishers should still send `created` first,
for consumers that care about the distinction.

`sources` **replaces** the previous set wholesale; it is not a patch. Omitting it means
"reads nothing I know of".

### `dashboard.deleted`

```json
{ "workspace": "acme-analytics", "eventType": "dashboard.deleted", "publishedAt": "…",
  "publishedBy": "superset", "data": { "dashboardId": "42" } }
```

### Source references — three kinds, none of them guessed

| `type` | Fields | Publisher says | Catalog does |
|---|---|---|---|
| `dataset` | `datasetId` | "this catalog dataset" — only possible if the tool got the dataset from the catalog | resolves to that dataset, if it's still registered |
| `location` | `backendId`, `path` | "this booth-storage location" ([ADR 0045](../../../booth-architecture/decisions/0045-storage-location-reference-format.md)) — natural for an app reading files | resolves to every registered dataset whose location **contains** it |
| `external` | `name`, optional `system` | "this thing, which I can't map to the catalog" — a table in a database the tool connects to itself | **never resolves it.** Shows it as an unresolved source. |

**Containment rule** for `location`: same `backendId`, and the dataset's path equals the
source's path or is a `/`-boundary ancestor of it. A dataset at `warehouse/orders` is read
by a source at `warehouse/orders` and at `warehouse/orders/2026/part-1.parquet`, but not by
`warehouse/orders-archive` (shares a name prefix, not a folder) and not by `warehouse` (the
reader is *wider* than the dataset, so it isn't reading only this dataset). A dataset
registered at a backend's root (`path: ""`) contains everything in that backend.

**Why `external` is never matched by name.** It would be easy to say "table `public.orders`
→ the dataset called `orders`". That is exactly the fabricated lineage ADR 0018 warns
against: a plausible-looking edge that may be wrong. An honest unresolved source is more
useful than a confident false one. If a real need for name-matching appears, it should be a
deliberate, separately-decided rule.

### Consumer semantics (what the catalog implements, and publishers can rely on)

1. **Ordering: last writer wins by `publishedAt`.** Delivery is at-least-once (ADR 0021) and
   several catalog replicas may consume one durable subscription concurrently, so events for
   one dashboard can arrive out of order. An event older than the newest already applied to
   that dashboard is ignored. **Publishers must therefore set `publishedAt` from a clock that
   doesn't run backwards for a given dashboard** (one publisher pod is fine; several with
   skewed clocks can cause a newer change to lose to an older one by the skew).
2. **Deletes are tombstoned.** A `deleted` event leaves a marker carrying its `publishedAt`,
   so a stale `updated` arriving afterwards cannot resurrect the dashboard. A genuinely newer
   `created`/`updated` (the tool recreated it) does bring it back, under the same catalog ID.
3. **Be liberal in what you accept.** Unknown fields are ignored (publishers can add fields
   freely). An individual unusable entry in `sources` is dropped and the dashboard is indexed
   with `lineageComplete` forced to `false`. Only a missing/invalid `dashboardId`, `name`,
   `publishedBy`, `publishedAt` or `path` makes the catalog refuse the whole event — and it
   then logs and terminates it rather than redelivering something that can never succeed.
4. **The subject is authoritative** for workspace and event type; an envelope that disagrees
   with its own subject is refused.
5. **Lineage is resolved when read, not when received.** Registering a dataset *after* a
   dashboard was indexed connects them with no reprocessing; deleting a dataset makes its
   edges unresolved again.

### Proposed addition to `contracts/core-platform-api.md`

Replace "exact `data` payload schema … not yet specified" in the event-bus bullet and the
"Explicitly not decided yet" section with a pointer to this schema (copy the tables above),
under contract version bump per the contracts' own versioning rule.

## Consequences

- Each dashboard module's v0 gains a concrete target: publish the shape above on every
  change. The one place they may struggle is `sources`: **Streamlit** should send
  `lineageComplete: false` and whatever `location` sources it can determine, and flag to the
  coordinator if even that is out of reach (ADR 0018). **Superset/Metabase** will mostly
  produce `external` sources unless their catalog-backed connections can also carry a
  `datasetId` — worth checking early, because it decides how connected the lineage graph
  looks in practice.
- Lineage will be honest but sometimes sparse. That is the intended trade-off.
- Dashboards from a module that publishes nothing (or an older schema) just don't appear.

## Options considered

- **Diff/patch events** (`sources.added`, `sources.removed`) — rejected. A lost or reordered
  patch corrupts state permanently; a full-state snapshot self-heals on the next event.
- **Separate `created` and `updated` shapes** — rejected: no consumer benefits, and it makes
  "missed the create" a hard failure.
- **Name-matching `external` sources to datasets** — rejected, above.
- **Making `owner` a structured object** (`{id, email, displayName}`) — deferred to
  [0003](0003-owner-identity.md); the platform has no user directory to make it meaningful.
- **A `kind` field** ("dashboard" / "app" / "question") — not proposed. `publishedBy` already
  says which module, and nothing in v0 needs finer. Trivial to add later since unknown
  fields are ignored.

## Not decided here

Who is *allowed* to publish these subjects at all — see [0005](0005-event-publisher-trust.md).
