# 0006: Code versions store their source inside the catalog

Status: **implemented** — a design choice within the brief's scope; recorded because
`booth-pipeline` will want to know where code actually lives.

## Context

The brief asks for "a registry where a user can publish and browse reusable code/functions"
with version history, and ADR 0043 says versions are browsable. It doesn't say where a
version's *content* lives. Two options: hold it in the catalog's database, or hold a reference
to a file in `booth-storage` (`{backendId, path}`, ADR 0045) like a dataset does.

## Decision

**The catalog stores the source text itself**, in its own database, byte for byte. A
version's `source` is capped (1 MiB by default, `BOOTH_CATALOG_MAX_CODE_BYTES`), must be
UTF-8 text with no NUL bytes, and is immutable once published.

## Why not a storage reference

- A reference has no immutability guarantee: ADR 0045 explicitly says a location may change or
  vanish, and anything in storage can be overwritten. A version that can change underneath a
  consumer isn't a version. Immutability is the one thing a consumer pinning "1.2.0" needs.
- It would make browsing code depend on `booth-storage` being installed and reachable, for
  what are small text files. (Datasets *have* to point outside the catalog — their bytes are
  huge and live elsewhere; code doesn't.)
- It would force the catalog to fetch content on read, i.e. to call `booth-storage` on the
  server side, which [0002](0002-location-verification-stays-in-the-ui.md) avoids.

## Consequences

- The database grows with published code; the cap bounds any one version. This is a registry of
  functions and snippets, not an artifact repository — large or binary payloads are out of
  scope, consistent with ADR 0043 ("not a package manager").
- **What this leaves open, on purpose:** how another module references a cataloged version *to
  run it*. The catalog exposes stable identifiers (entry ID, version label, `latest` alias) and
  returns the source through `GET /api/code/{id}/versions/{version}`; it defines no "runnable"
  contract, no entrypoint, no dependency or environment metadata. Per ADR 0010 and
  `ARCHITECTURE.md` §7 item 6, that shape is `booth-pipeline`'s to settle narrowly first, and a
  second consumer (`booth-notebooks`, `booth-streamlit`) is what would trigger a shared
  contract. Nothing here should be read as pre-empting that. `booth-pipeline` isn't started
  yet, so there is nothing to coordinate against — when its agent begins, this is the surface
  to build against and the point to negotiate from.
