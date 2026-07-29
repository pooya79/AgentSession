# ADR-011: Coordinate all-source indexing in the application layer

## Status

Accepted

## Context

The web interface should index supported histories automatically and report one
coherent status. Coordinating discovery and a browser-side queue would make
work dependent on a tab remaining connected, duplicate lifecycle rules across
presentations, and make aggregate counts vulnerable to cumulative progress
snapshots being counted more than once.

Discovery and individual imports can also fail independently. A clean-looking
terminal state would be misleading when usable evidence was imported alongside
failures or record diagnostics.

## Decision

The application runtime owns one active import-all run. Each run discovers
sources afresh, schedules at most two source imports concurrently, and observes
the existing application-owned per-source import subscriptions. Repeated start
requests while active join the same run. A request after termination starts a
new incremental, idempotent run.

Aggregate counts are recomputed from the latest cumulative snapshot for each
source, preventing double-counting. The status retains exact source, record,
event, session, unchanged, failure, and diagnostic totals. It retains at most
32 recent diagnostics with source attribution and event or raw-record evidence
references, plus an exact omitted count.

Independent source failures do not stop other sources. Terminal status is:

- `up_to_date` only when discovery and all imports finish without diagnostics
  or failures;
- `completed_with_issues` when any discovery/import diagnostic or source
  failure exists; or
- `unavailable` when discovery cannot run or shutdown cancels the workflow.

The web server starts one run asynchronously at process startup. Manual rescans
invoke the same operation. HTTP handlers only start/join work and render status;
browser cancellation never owns or cancels the run. Runtime shutdown stops new
scheduling, cancels the all-source observer workflow, then settles the
underlying imports before SQLite closes.

## Consequences

- TUI and other clients retain the existing single-source import operations.
- Web indexing continues when a browser navigates away or disconnects.
- There is no filesystem watcher or periodic scan; changed histories require a
  manual rescan or a new web process.
- Bounded status is safe for late observers, while omitted detail is explicit.
- Partial evidence and independent failures cannot be presented as clean
  success.
