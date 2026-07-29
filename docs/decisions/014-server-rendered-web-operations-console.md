# ADR-014: Server-rendered web operations console

## Status

Accepted

## Context

The initial web scaffold loaded its useful dashboard regions after page load,
used query-oriented timeline routes, and retained an observer-only
server-sent-events helper alongside htmx polling. That made JavaScript a
practical requirement for the primary workflow and gave import observation two
production mechanisms.

Canonical evidence, indexing state, and projection lifecycle already belong to
shared application services. The web interface needs dense, bounded navigation
without acquiring source, storage, or projection business logic.

## Decision

The web interface is a server-rendered operations console. `GET /` renders
exact library metrics and a bounded session page, `GET /indexing` renders the
latest application-owned discovery/import state, and path-oriented session and
event routes provide bounded timelines and deep links.

Ordinary links and CSRF-protected forms provide every core navigation and
mutation. Embedded htmx is optional enhancement: it swaps bounded fragments and
polls only while application-owned import or projection work is active. No SSE
endpoint or JSON progress representation is part of the web boundary.

Canonical event references are resolved in bounded batches through the shared
exploration service. Resolved references link to focused timeline windows;
missing, mismatched, excess, and raw-record references remain escaped text.
Projection state remains a secondary disclosure and rebuild-all requires both
complete builder availability and a server-rendered confirmation.

## Consequences

- Initial pages remain useful when JavaScript is unavailable or blocked.
- Browser disconnection only stops observation; application-owned work
  continues.
- Web handlers remain consumers of shared services and do not parse sources,
  query SQLite, or build projections directly.
- Polling is the sole production live-update mechanism.
