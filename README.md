# booth-catalog

Project Booth's catalog module (nav group **View**). One place to find three kinds of asset,
each browsable, searchable and connected by lineage: **datasets** (with where their bytes live in
storage), **code** (reusable functions with a version history), and **dashboards** (indexed from
what Superset, Metabase and Streamlit publish). Brief:
`../booth-architecture/agent-briefs/catalog.md`.

## What v0 delivers

| Definition-of-done item | Where |
|---|---|
| Register/browse datasets: name, description, storage location `{backendId, path}` (ADR 0045), schema, tags, owner (ADR 0042) | `internal/data`, `web/src/views/Data*.tsx`, `DatasetForm.tsx` |
| Code catalog: publish and browse code with owner/description and **multiple, immutable, browsable versions** (ADR 0043); no execution or packaging | `internal/code`, `web/src/views/Code*.tsx` |
| Cross-asset text search over names and descriptions (ADR 0044) | `internal/search`, `web/src/views/SearchView.tsx` |
| Dashboard catalog: subscribes to `dashboard.*` events, indexes with owner and **lineage to source datasets**, native browsing UI in the View section (ADR 0018) | `internal/dashboards`, `internal/events`, `web/src/views/DashboardViews.tsx` |
| Manifest + health check per contract | `charts/booth-catalog/templates/boothmodule.yaml`, `/healthz` |
| CI per `contracts/testing-strategy.md` | `.github/workflows/` |

Lineage runs both ways: a dashboard shows what it reads (resolved against the dataset catalog as
it is *now*, so a dataset registered later connects with no reprocessing), and a dataset shows
which dashboards read it.

## Stack

Matches booth-core / booth-storage: **Go** + `chi` + `go-oidc` backend with PostgreSQL via `pgx`;
**React + TypeScript + Vite + Tailwind** UI in `web/`, published as `@projectbooth/catalog-ui`
(ADR 0030); a Helm chart with a `BoothModule` manifest (ADR 0019). Additions: `nats.go` for the
event subscription (ADR 0021). Go floor is **1.26** (matching booth-storage).

```
cmd/catalog/           entrypoint
internal/asset/        the small shared kernel: errors, page/hit shapes, {backendId,path}
                       normalization, text matching. The ONLY thing the three asset packages share.
internal/data/         dataset catalog   ─┐
internal/code/         code catalog       ├ never import each other (ADR 0001: a folder boundary,
internal/dashboards/   dashboard catalog ─┘  so a future split is a move, not a untangle)
internal/search/       fans a query out to the three
internal/app/          the one place that knows more than one asset package (lineage adapter)
internal/events/       NATS/JetStream subscriber + the event processor
internal/api/          HTTP routes        internal/auth/  OIDC verification + role derivation
internal/db/           pool + per-package migrations
web/                   the UI package
charts/booth-catalog/  Helm chart, BoothModule manifest (no RBAC: it never calls the k8s API)
docs/decisions/        judgment calls the ADRs didn't settle — READ THESE
hack/                  test Postgres compose file, and a dashboard-event publisher for dev
test/contract, test/integration
```

Each asset package has a `Store` interface with an **in-memory** and a **PostgreSQL**
implementation, and one contract test suite that both must pass, so they cannot drift apart.

## API (what other modules call)

Reached through core's gateway at `/modules/catalog/api/...`. Every request needs
`Authorization: Bearer …` and `X-Workspace`. Errors are `{"error": "...", "field"?: "..."}`
(422 validation, 409 conflict, 404). Lists take `?q=&limit=&offset=` and return
`{"items": [...], "total": n}`.

| Route | Role | |
|---|---|---|
| `GET /api/datasets` `?q&tag&owner` · `GET /api/datasets/{id}` · `GET /api/tags` | any | browse |
| `POST /api/datasets` · `PUT`/`DELETE /api/datasets/{id}` | editor, owner | register / edit / delete |
| `GET /api/datasets/{id}/lineage` | any | `{"dashboards": [...]}` — what reads it |
| `GET /api/code` `?q&owner&language` · `GET /api/code/{id}` · `GET /api/code/{id}/versions` | any | browse; history newest first |
| `GET /api/code/{id}/versions/{version}` | any | one version **with source**; `latest` is an alias |
| `POST /api/code` (entry + first version) · `PUT`/`DELETE /api/code/{id}` · `POST /api/code/{id}/versions` | editor, owner | publish / edit / delete |
| `GET /api/dashboards` `?q&owner&source` · `GET /api/dashboards/{id}` | any | detail includes resolved `lineage` |
| `GET /api/search?q=&types=data,code,dashboard` | any | one query across all three |
| `GET /api/config` | any | limits and event-subscription state, for the UI |
| `GET /healthz` · `GET /livez` | none | readiness (checks the database; what core polls) · liveness |

There is **no write API for dashboards** at any role: they exist only because a dashboard module
published an event.

**Roles are derived from the token's `groups` claim, never trusted from `X-Booth-Role`** — an
over-claiming header is rejected with 403 (ADR 0041; verified against real Keycloak).
`oidc.groupsClaim` must match booth-core's or every request is refused.

## The dashboard event contract

The payload for `dashboard.created` / `updated` / `deleted` is **ratified as ADR 0046** (this repo's
[docs/decisions/0001](docs/decisions/0001-dashboard-event-payload.md) is the reasoning behind it):
full-state upserts, three kinds of lineage source (`dataset`, `location`, `external`),
last-writer-wins by `publishedAt`, tombstoned deletes. `internal/events` and `internal/dashboards`
implement it as ratified. `go run ./hack/publish-dashboard-event -h` sends one by hand.

## Running and testing

```sh
docker compose -f hack/docker-compose.emulators.yml up -d --wait   # PostgreSQL (port 15433)
eval "$(sh hack/test-env.sh)"                                        # BOOTH_TEST_POSTGRES_DSN
go test ./...                                                        # unit + contract
(cd web && npm ci && npm run typecheck && npm run lint && npm test -- --run && npm run build)
```

Without the container the Postgres tests **skip** locally (CI sets
`BOOTH_TEST_REQUIRE_EMULATORS=1`, which turns a missing database into a failure). The event
tests embed a real `nats-server` with JetStream in-process — no container. `test/contract`
needs `helm`. `-race` needs cgo (CI runs it; it wasn't available where this was built).

Local run without a database: `BOOTH_CATALOG_DEV_MEMORY=true` (state vanishes on exit); also needs
`BOOTH_OIDC_ISSUER_URL` / `BOOTH_OIDC_CLIENT_ID`. `BOOTH_NATS_URL` enables the dashboard
subscription. For a real login, use `booth-architecture/local-dev`'s Keycloak. `web`'s
`npm run dev` is a dev harness: paste an access token into it; its Vite proxy strips the gateway
prefix **and** turns `X-Workspace` into `X-Booth-Workspace`, as the real gateway does.

CI: `ci.yml` (every push/PR), `integration.yml` (kind, merge-to-main + nightly),
`publish.yml` (`catalog-ui-v*` tags → npm), `release.yml` (`v*.*.*` tags → image + chart).
**Branch protection on `main` is a GitHub setting and has not been configured from here.**

## Wiring into booth-design

Add `@projectbooth/catalog-ui` as a dependency, import `…/dist/style.css`, and register it:
`registerNativeModule("catalog", CatalogApp)`. The manifest's `navPath` is `/catalog`; the shell
matches everything beneath it to this one component, and the UI reads its own sub-route
(`/catalog/data`, `/catalog/code/{id}/v/{version}`, …) from the address bar, since
`NativeModuleProps` carries no route (same workaround as booth-storage's decision 0002).

## Read before deploying

All six were ruled on by the coordinator (2026-09-19):

- [0001](docs/decisions/0001-dashboard-event-payload.md) — the `dashboard.*` payload. **Ratified as ADR 0046**, as proposed.
- [0002](docs/decisions/0002-location-verification-stays-in-the-ui.md) — registering a dataset does not verify its location; the UI checks live through storage. **Accepted as-is.**
- [0003](docs/decisions/0003-owner-identity.md) — "owner" is a free-form string. **Resolved as ADR 0047:** `booth-core` is building a minimal user directory (its action item); the string is correct for v0.
- [0004](docs/decisions/0004-catalog-write-permissions.md) — `editor`/`owner` write, `viewer` reads. **Ratified as ADR 0048.**
- [0005](docs/decisions/0005-event-publisher-trust.md) — **nothing yet authenticates who publishes to the event bus**, so anyone who can reach NATS can forge dashboards in any workspace. **Resolved as ADR 0049**, but the fix lives in `booth-core`; until it ships, restrict NATS with a NetworkPolicy.
- [0006](docs/decisions/0006-code-source-stored-inline.md) — code source lives in the catalog's database, which is what makes versions immutable. **Accepted as-is.**

## Not done

- **Not verified in a real cluster.** `integration.yml` has never run (no kind/k3d here), and the
  module has never been installed by a real `booth-core` or mounted in a real `booth-design`. What *was* run
  end to end: the real binary against real Keycloak + PostgreSQL + NATS (tokens, roles, header forgery,
  lineage, stale/delete events) and the UI in a real browser through the dev harness.
- **The npm package is built and tested but not published**, and `booth-design` doesn't register it yet.
- **No dashboard module exists yet to publish `dashboard.*` events** (the payload is ratified, ADR 0046); the dashboard path has only been exercised with the dev publisher.
- **`booth-pipeline`'s code reference is unsettled**, deliberately: the catalog exposes entry ID, version
  label and a `latest` alias and defines no "runnable" contract (ADR 0010; see 0006). Nothing to coordinate
  against yet — `booth-pipeline` isn't started.
- **Lineage covers dashboard → dataset only.** No dataset → dataset or code lineage: no module publishes it
  (`ARCHITECTURE.md` §7 item 12 — whether the asset model generalizes is not mine to decide).
- **No soft delete / undo**, and no deleting a single code version (delete the entry, or publish a new one).
  Deleting a dataset drops it from dashboards' resolved lineage.
- **Tombstones for deleted dashboards are kept forever** (they are what resists stale resurrection).
- **Search is substring matching**, not full-text: no stemming, no ranking beyond "name matches first".
- **Schema is entered by hand.** Nothing infers it from storage — that needs a table-format decision
  (`ARCHITECTURE.md` §7 item 21).
- **The dev harness needs a pasted token and doesn't refetch when it changes** (the real shell has one before it mounts).
- Multi-replica behaviour is designed for (row-locked upserts, one shared durable consumer) and tested at the
  store level with concurrent appliers, but not exercised with several real pods.
- The UI has had no accessibility audit beyond labelled controls and roles, and no dark-mode visual pass.
