# 0005: Nothing verifies who published a `dashboard.*` event

Status: **flagged** — a real gap found while building the subscriber, in `booth-core`'s
territory rather than this repo's. Not fixable here; needs a coordinator decision.

## What was found

The dashboard catalog trusts the event envelope's `publishedBy` to say which module
published an event, and `workspace` (from the subject) to say whose dashboard it is. Nothing
checks either:

- `booth-core`'s bundled NATS is deployed with **no authentication and no per-subject
  permissions** (`charts/booth-core/values.yaml`: only `jetstream` is configured), and modules
  connect to it directly (ADR 0021 — the bus deliberately isn't proxied like the sync path).
- So any pod in the cluster that can reach NATS can publish
  `booth.<any workspace>.dashboard.created` with `publishedBy: "superset"` and index — or
  delete, by tombstoning — an arbitrary dashboard in **any workspace**.

This is the event-bus counterpart of the header-trust gap ADR 0041 closed for the HTTP path,
and the same shape of finding: the boundary the architecture *says* exists (ADR 0007 — "the
trust boundary at core consistently for both sync and async traffic") is not enforced on the
async side.

## What this repo does about it

Only what it can: the subject is authoritative for workspace and event type, and an envelope
that contradicts its subject is refused (`internal/events`); input is validated and bounded;
`path` values that would leave the shell are rejected so a forged dashboard can't become an
open redirect. It does **not** and cannot authenticate the publisher.

## What's needed (not decided here)

Some combination of, in `booth-core`:

1. NATS accounts/users so each module authenticates to the bus, with **publish permissions
   scoped to the subjects that module legitimately owns** (`booth-superset` may publish
   `booth.*.dashboard.*` and nothing else, and ideally only for workspaces it serves);
2. a `NetworkPolicy` restricting who can reach NATS at all;
3. the credential-provisioning path for (1), which is ADR 0020's Secret mechanism — so this
   likely wants an ADR before any module agent implements client-side auth for NATS.

Worth raising now because every module that will publish or subscribe (`booth-pipeline`,
`booth-spark`'s `dataset.written`, …) currently connects unauthenticated, and retrofitting
credentials into each is exactly the churn ADR 0041 had to do for role derivation.

## Consequences until then

Anyone who can reach the event bus can forge catalog entries. Deploy with a restrictive
`NetworkPolicy` on NATS in any environment where in-cluster workloads aren't fully trusted.
