# ADR-010: Expose bounded canonical evidence exploration services

## Status

Accepted.

## Context

The first TUI and web navigation need imported sessions and timelines before
search, Git correlation, analysis, or exports exist. The authoritative reader
previously returned complete slices and full events, which could load detached
normalized payloads and allowed presentation code to depend on infrastructure
packages.

## Decision

The application exposes shared `Explorer` and `Ingestion` interfaces using
application-owned requests and results. Session and timeline lists use opaque,
versioned keyset cursors, a default limit of 50, and a maximum of 200. Sessions
sort by recorded start time descending with nulls last and session ID as the
tie-breaker. Timelines sort only by preserved source sequence.

Timeline rows and payload-excluded event details use SQL projections that do
not select normalized payload columns, detached payload rows, searchable text,
or retained raw records. Event detail requires both its session and event ID;
normalized data is loaded only when explicitly requested.

Results distinguish complete, partial, unavailable, and not-found evidence.
Usable evidence accompanied by canonical diagnostics is partial. A known
session with no usable requested evidence and diagnostics is unavailable. A
clean empty result is complete. Invalid identifiers and cursors are validation
errors, while database failures and context cancellation remain errors with
their causes preserved. Diagnostic synopses contain exact totals and at most
ten ordered entries.

Discovery summaries and import initiation are mapped to application types.
Import subscriptions retain application-owned coalescing and detachment
semantics; a presentation observer never owns the import worker context.

## Consequences

- TUI and web can share one deterministic service contract without importing
  discovery, adapters, importer internals, SQLite, Git, or projections.
- Large timelines and diagnostic sets remain bounded, and retained raw content
  never crosses this boundary.
- Cursors do not create a database snapshot across concurrent reimports;
  keyset ordering avoids offset drift but consumers may observe newer evidence.
- Search and derived projections can be added later without changing this
  canonical exploration path.
