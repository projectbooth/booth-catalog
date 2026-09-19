# 0004: Who may change the catalog — workspace `editor`/`owner`; `viewer` reads

Status: **proposed** — follows ADR 0038's precedent rather than inventing anything, but the
brief doesn't state it, so it is written down for the coordinator to confirm.

## Context

The brief says users "register/browse datasets" and "publish and browse" code, but never who
may write. `booth-storage` faced the same gap for its file browser and ADR 0038 answered it
with the existing workspace roles (ADR 0025): `editor` and `owner` write, `viewer` is
read-only, no new permission model.

## Decision

The catalog applies the same rule, server-side, on every write route (`auth.Require(CanWrite)`),
with the role derived from the verified token per ADR 0041 — never from the forwarded header
alone:

| Action | `viewer` | `editor` | `owner` |
|---|---|---|---|
| Browse, search, read datasets / code / dashboards, read code source | ✓ | ✓ | ✓ |
| Register / edit / delete a dataset | | ✓ | ✓ |
| Create / edit / delete a code entry, publish a version | | ✓ | ✓ |
| Create / edit / delete a dashboard | — dashboards are not user-editable at all — | | |

Any `editor` or `owner` may edit **any** asset in the workspace, whoever registered it. The UI
hides write controls from viewers, but the server refuses regardless (403). Dashboards have no
write API: they exist only through events (ADR 0018).

## Consequences

- No per-asset ACLs, no "only the owner can edit". That would need a user directory to be
  enforceable at all ([0003](0003-owner-identity.md)), and nobody has asked for it.
- An editor can delete a dataset another editor registered. Deletion only removes the catalog
  entry — never the data in storage, which this module can't touch — but it does drop the
  dataset from any dashboard's lineage. Acceptable for v0; a soft-delete/undo is the obvious
  follow-up if it proves painful.
- Deleting a code entry deletes all its versions. Anything that pinned one will find it gone.
  The registry keeps no reference count, because nothing references code yet (ADR 0010's
  reference shape is `booth-pipeline`'s to define).

## Alternatives considered

- **Owners-only writes** — stricter, but an ownership model we can't back with identity.
- **Any authenticated user can write** — contradicts ADR 0025's whole role scheme.
