# ADR-007: Own import lifecycles in the application layer

## Status

Accepted.

## Context

Imports are long-running shared work. TUI and HTTP requests may independently
start observing the same source, run at different speeds, or disconnect. If a
presentation owns the worker context or publishes through blocking channels,
client behavior can duplicate or interrupt canonical importing.

Progress may also contain an unbounded number of malformed-record diagnostics.
Retaining every update for replay would undermine the importer's bounded-memory
design.

## Decision

The application layer owns one active import job per canonical top-level source
ID. Duplicate requests subscribe to that job. Subscriber contexts and detach
operations affect only observation; only application shutdown cancels work.

The importer emits source-neutral cumulative progress synchronously. The
application publishes immutable latest snapshots through one-element observer
buffers, replacing superseded snapshots rather than blocking the worker. Late
observers immediately receive the latest snapshot, and every connected observer
receives the terminal completion or failure snapshot before its channel closes.

Progress retains cumulative diagnostic counts, the 32 most recent diagnostics,
and a count of omitted diagnostics. Container imports are keyed by the physical
source while also reporting the active logical child.

Shutdown rejects new requests, cancels active job contexts, and waits for each
runner to return so an in-flight SQLite transaction can finish committing or
rolling back and staged reconciliation can clean up.

## Consequences

- TUI and web packages adapt subscriptions but do not coordinate imports.
- A slow or disconnected observer cannot stall or cancel canonical work.
- Intermediate snapshots and older diagnostic details may be coalesced, while
  cumulative counts and terminal state remain available.
- A new request after a job has terminated starts a fresh incremental import.
