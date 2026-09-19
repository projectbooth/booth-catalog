# 0003: What an "owner" is — a free-form string, defaulting to the registering user

Status: **flagged** — implemented in the simplest way that works, but it papers over a
platform-level gap (there is no user directory) that the coordinator should decide whether
to close. Not blocking.

## Context

ADR 0042 puts an owner on every dataset from v0, ADR 0018 wants dashboards indexed "with
owner", and the code catalog has one too. None of the ADRs say what an owner *is*.

The platform has identities (an OIDC `sub`, plus whatever `preferred_username`/`email` claims
the IdP issues) but **no user directory**: nothing maps a `sub` to a display name, nothing
lists users, nothing lets a UI say "pick an owner". A `sub` on Keycloak is an opaque UUID —
correct as an identifier, useless as something a person reads in a table.

Dashboards make it sharper: their owner comes from a third-party tool (Superset's user,
Metabase's creator) with its own notion of a user, which won't be the platform's `sub`.

## Decision

- **`owner` is a free-form string** (≤200 characters) on datasets, code entries and
  dashboards. The catalog attaches no meaning to it beyond display and an exact,
  case-insensitive filter.
- **It defaults to the registering user's most readable claim:** `preferred_username`, else
  `email`, else `sub` (`internal/auth`). It can be set to anything — a person, a team
  ("data-platform"), a mailbox.
- **It is not access control.** Being a dataset's `owner` grants nothing; workspace roles do
  (see [0004](0004-catalog-write-permissions.md)). Provenance is recorded separately and
  immutably in `createdBy` (the token `sub`), so "who actually registered this" survives
  someone re-assigning the display owner.
- **Editing a dataset without naming an owner keeps the existing one**, so an editor fixing a
  description can't accidentally take ownership.
- For dashboards, `owner` is whatever the publisher sends, unmodified.

## Consequences

- There is no guarantee two assets owned by "the same person" spell the owner the same way
  (`alice`, `alice@example.com`, `Alice`). The filter is case-insensitive but exact. Good
  enough to browse by; not something to build access rules on.
- A dashboard's owner and a dataset's owner live in different namespaces (the tool's user vs
  the platform's), and the catalog doesn't reconcile them.

## What the coordinator may want to decide

Whether the platform should grow a minimal user directory (a `sub` → display name/email
mapping `booth-core` maintains from token claims), and whether publishers should send a
platform identity rather than their tool's. Either would let `owner` become a real reference
and a UI offer a picker. Until then this is the honest minimum.

## Alternatives considered

- **Owner = token `sub`, always** — stable but unreadable, and impossible for dashboards.
- **A structured owner (`{id, displayName}`)** — the right shape once a directory exists;
  premature before, since nothing could fill `id` meaningfully for a third-party tool's user.
- **Owner-only edit rights** — a permission model nobody asked for; see 0004.
