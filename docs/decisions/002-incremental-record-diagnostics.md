# ADR-002: Persist record diagnostics incrementally

## Status

Accepted

## Context

Adapters stream large session sources one raw record at a time. A malformed or
partially normalized record may produce a diagnostic without producing a
canonical event. Accumulating every such diagnostic in `Session.Diagnostics`
would make import memory usage grow with source size and repeatedly rewrite an
unbounded session snapshot.

## Decision

Session diagnostics are limited to session-level conditions and bounded
summaries. Diagnostics emitted for a raw record are committed with the batch
that retains that record, using the raw-record ID and the diagnostic's
zero-based position within the record envelope as stable identity.

SQLite stores record diagnostics separately from the replaceable session
diagnostic snapshot. Identical batch retries are idempotent; conflicting
content at the same record and ordinal is rejected. Source reconciliation and
session deletion remove record diagnostics through foreign-key cascades.

## Consequences

- Import memory remains bounded by the current batch rather than total
  diagnostic count.
- Malformed-only imports retain explainable evidence without requiring events.
- Record diagnostic ordering must remain deterministic within an adapter's
  normalization version.
- Consumers query session-level and record-level diagnostics separately.
