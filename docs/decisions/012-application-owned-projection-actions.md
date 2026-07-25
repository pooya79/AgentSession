# ADR-012: Own projection actions in the application runtime

## Status

Accepted.

## Context

Projection lifecycle state is durable, but a retry or rebuild initiated
directly with a TUI or HTTP request context would stop when its presentation
disconnects. That would make otherwise identical controls behave differently
and could leave work pending solely because an observer navigated away.
Presentation code also needs a safe way to distinguish canonical evidence from
derived-data readiness without importing storage contracts.

## Decision

The shared application service exposes presentation-safe per-session projection
status, aggregate counts, retry, and rebuild actions. Durable status remains one
of `pending`, `running`, `failed`, or `ready`. Usability and staleness are
derived by comparing the retained ready version and canonical revision with the
current target; stale output is never usable for the current target.

The runtime owns an operation coordinator around the projection manager.
Actions validate the session and requested kind before admission, return
promptly, and then run on the runtime lifetime rather than the initiating
request lifetime. Work is serialized per session. Duplicate operations are
coalesced, and a queued rebuild-all supersedes queued single-kind rebuilds.
The existing manager remains authoritative for claims, renewable leases, and
flights shared with imports and other callers.

TUI and web views observe status every 500 milliseconds only while the
application reports active work or a durable state is running. Navigating away
ends observation, not work. Runtime shutdown rejects new actions, cancels and
awaits projection workers, and only then closes SQLite.

Only bounded diagnostic codes and explicitly safe summaries cross the
application boundary. Canonical evidence remains available regardless of
projection state. Missing builders remain pending.

## Consequences

- TUI and web controls have the same lifecycle and validation semantics.
- A presentation disconnect cannot cancel admitted projection work.
- Canonical timelines do not imply that derived data is ready.
- No scheduler, builder, database migration, or fifth durable status is
  introduced.
- Process shutdown may wait for a projection builder to honor cancellation.
