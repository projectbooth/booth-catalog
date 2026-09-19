# 0002: Registering a dataset does not verify its storage location; the UI checks it live

Status: **accepted as-is** by the coordinator; no ADR needed. It is a consequence of ADR 0045 and ADR 0039,
recorded because it is a visible behavior a future reader (or `booth-pipeline`) may expect to be otherwise.

## Context

A dataset's location is a `{backendId, path}` pair (ADR 0045), resolved only by calling
`booth-storage`'s own API — never by connecting to the backend directly (ADR 0039). The
question is *who* makes that call, and *when*.

The obvious design is for the catalog's backend to call `booth-storage` when a dataset is
registered, and reject a location that doesn't exist. That needs the backend to call another
module synchronously, which per ADR 0007 goes through `booth-core`'s gateway carrying the
caller's identity — so the catalog would have to forward the user's bearer token, learn
core's gateway URL, and depend on `booth-storage` being up for every registration.

## Decision

- **The catalog backend never calls `booth-storage`.** Registration validates only the
  *shape* of a location (backend-ID grammar, path rules — the same ones `booth-storage`
  enforces) and stores it. `internal/data` has no dependency on `booth-storage` at all, and
  this repo needs no core gateway URL.
- **The UI does the live check**, in the user's browser, with the user's own token, through
  the gateway at `/modules/storage/api/...` — exactly how `@projectbooth/storage-ui` calls its
  own backend. The registration form offers a backend picker (`GET …/backends`) and a "Check
  location" action (`GET …/backends/{id}/objects?prefix=…`); dataset detail can re-check any
  time. If `booth-storage` isn't installed the picker degrades to free text and the check is
  unavailable, and nothing else breaks.
- A location that fails the check **can still be registered.** A dataset legitimately gets
  registered before the pipeline that writes it has run, and ADR 0045 says a reference is
  never a promise the data exists.

## Consequences

- The catalog keeps working when `booth-storage` is down or absent; `booth-storage` is a
  *UX* dependency of the catalog, not an operational one.
- `booth-storage`'s own authorization applies to the check, automatically — a viewer who
  can't read a backend can't verify against it.
- **Nothing server-side ever knows whether a registered location currently exists.** A module
  that wants a validated location (`booth-pipeline`, `booth-notebooks`) must call
  `booth-storage` itself with the `{backendId, path}` it read from the catalog. That is what
  ADR 0045 already says; it is stated here because it is the sharp edge.
- Sources named in a dashboard event are likewise never verified against storage.

## Alternatives considered

- **Backend-side verification through the gateway with a forwarded token** — rejected for the
  coupling and failure-mode reasons above; it also can't work for dashboard-event lineage,
  which arrives with no user token at all.
- **Backend-side verification with a service identity** — would need a credential this module
  has no provisioning path for (ADR 0020) and would bypass the user's own storage permissions.
