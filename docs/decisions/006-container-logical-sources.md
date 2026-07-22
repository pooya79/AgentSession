# ADR-006: Expand database containers into logical session sources

## Status

Accepted.

## Context

Stream adapters map one discovered file to one canonical session. OpenCode's
current SQLite database contains multiple sessions and may change through WAL
commits while it is being observed. Treating the database as one session would
leak source-specific multiplexing into the canonical model and make per-session
checkpoints, reconciliation, and deletion ambiguous.

## Decision

Adapters may optionally prepare a physical source as a read-only container.
The prepared container owns one consistent snapshot and enumerates stable
logical child sources, each of which uses the existing prepared-source import
lifecycle. OpenCode child source IDs are derived from the physical source ID
and native session ID.

Storage tracks generic container membership separately from canonical data.
The coordinator updates that inventory only after every enumerated child
import succeeds. The inventory replacement and deletion of AgentSession-owned
data for missing children occur in one transaction. Source databases and
repositories are never modified.

For OpenCode, authoritative records are deterministic serializations of
logical `session`, `message`, and `part` rows, not SQLite page bytes. Each
serialized column carries its name, SQLite value type, and an exact recoverable
TEXT or BLOB representation. Checkpoints use a versioned logical record count,
last key, and streaming digest of those serializations.

## Consequences

- Canonical sessions, events, and storage remain source-neutral.
- Existing stream adapters and one-session imports keep their existing path.
- One failed child prevents inventory publication, so absent sessions are not
  deleted on a partial container run.
- Child imports already committed before a later child failure remain valid,
  but membership cleanup waits for a complete successful snapshot.
- Database adapters must keep snapshot lifetime bounded and stream logical rows
  through synchronous sink backpressure.
