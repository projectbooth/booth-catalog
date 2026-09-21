# 0006: Code versions store their source inside the catalog

Status: **accepted as-is** by the coordinator; no ADR needed. A design choice within the brief's scope,
recorded because `booth-pipeline` will want to know where code actually lives.

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

## Guarantees a consumer may rely on (confirmed to `booth-pipeline`, 2026-09-21)

`booth-pipeline` resolves a code reference `{entryId, version}` at pipeline-save time, server-side, with the
caller's own token through the gateway, and snapshots the source plus a hash — runs never call the catalog. That
needs nothing new from this repo; it relies on the following, each covered by an existing test:

- **A published version's source is immutable.** Republishing a label is refused (409); there is no update path.
  Source is stored byte for byte, so a hash over its UTF-8 bytes is stable across reads.
- **`GET /api/code/{id}/versions/latest` returns the concrete `version` label** (with `seq`, `notes`, `sizeBytes`,
  `publishedBy`, `publishedAt`, `source`). "Latest" is the most recently published version (highest `seq`), not the
  highest-numbered label; the label `latest` is reserved.
- **A deleted entry is 404 on every read**, and an entry in another workspace is indistinguishable from a missing one.

What is *not* promised: immutability is per version, not per entry — deleting an entry deletes all its versions, and
individual versions cannot be deleted. Entry IDs are UUIDs, stable across renames and never reused (a *name* can be
reused, so key on the ID). The 1 MiB source cap is a default the operator can change; it is advertised at
`GET /api/config` (`maxCodeSourceBytes`). `language` is an optional free label, lowercased, and mutable. The gateway's
own "module not found" 404 is plain text while the catalog's is JSON `{"error": …}` — tell them apart by the body.
No digest is exposed; `booth-pipeline` hashes its own snapshot. Changing any of the promised behavior would be a
change to a consumer's contract and should go through the coordinator.
