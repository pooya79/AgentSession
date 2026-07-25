# ADR-013: Maintain bounded session exploration fields

## Status

Accepted

## Context

The session list is ordered by evidence-derived last activity and displays the
event count and earliest normalized user message. Deriving all three values
while reading a page requires aggregating and probing events for every session
before SQLite can apply the keyset cursor and limit.

## Decision

SQLite stores `last_activity_at`, `first_user_message`, and `event_count` on the
AgentSession-owned session row. Each canonical import batch refreshes these
fields in the same transaction as its events and checkpoint. A migration
backfills existing indexes from canonical events.

Last activity retains the existing precedence of session end, latest
timestamped event, then session start, and is stored with fixed nanosecond
precision for indexed lexical ordering. A dedicated event role column preserves
the normalized role even when a large payload is detached; the first user
message is selected from that role rather than adapter-generated summary text
and remains bounded to 1,024 characters.

## Consequences

- Session pages use an indexed keyset query without re-aggregating the library.
- Derived fields cannot become visible ahead of their canonical import batch.
- Reconciliation recomputes the fields while promoting replacement evidence.
- The session row contains rebuildable display data in addition to canonical
  session metadata.
