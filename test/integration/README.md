# Real-cluster integration tests (layer 3)

Per `contracts/testing-strategy.md` and ADR 0024. Runs on merge to `main` and nightly
(`.github/workflows/integration.yml`), not on every push.

**Status: written, never executed.** No `kind`/`k3d` was available when this was built. The
workflow's steps were checked by hand and the chart is exercised by `test/contract`, but expect
first-run issues.

## What this covers

Deploys the chart into an ephemeral `kind` cluster alongside throwaway PostgreSQL and NATS
(JetStream) stand-ins and verifies:

- The pod becomes `Ready` and `/healthz` returns 200 — which only happens once all three
  asset packages' migrations ran and Postgres is reachable.
- The chart's `BoothModule` custom resource registers (ADR 0019) with `navPath: /catalog`,
  `navGroup: view`, against a vendored copy of booth-core's CRD (`fixtures/boothmodule-crd.yaml`).
- **The catalog starts cleanly before the `BOOTH_EVENTS` stream exists** (as at install time,
  when booth-core may not have created it yet): `/healthz` is 200, reporting
  `waiting-for-stream`. Then the stream is created the way booth-core creates it, and the
  subscription must attach (`eventBus: subscribed`) — a real durable JetStream consumer in a real
  cluster.
- Every API route refuses unauthenticated requests from inside the cluster.
- The service account can do nothing against the Kubernetes API (no Secrets, no pods).

Everything else — the stores against real PostgreSQL, the HTTP API and its roles, the event
processor, and the JetStream consumer's durability/retry/poison-message behaviour against an
embedded nats-server — is covered by the per-push unit/contract layer.

## What this doesn't cover yet

- **Deploying alongside a real, pinned `booth-core`** and exercising the catalog through its
  gateway (`/modules/catalog/*`), with core's controller reconciling our `BoothModule` and
  polling `/healthz`. This is the layer-3 scenario the testing contract describes and the main
  gap. It needs a cross-repo credential to pull core's published build, and a real OIDC issuer
  in the cluster to mint tokens.
- **A real dashboard event flowing end to end.** Publishing one needs a token-free path in;
  what's verified in-cluster is that the subscription attaches. The event → index → API path is
  covered by `internal/events`' tests (real embedded JetStream) and `internal/api`'s (real
  services), but not stitched together across a cluster.
