# ADR-009: Compose runnable infrastructure in an application runtime

## Status

Accepted.

## Context

Discovery, adapters, verified importing, authoritative SQLite storage, and
projection lifecycle management existed as independent capabilities. Letting a
CLI, TUI, or HTTP handler assemble those pieces would duplicate policy and make
database closure race active import transactions.

The executable also needs predictable application-owned storage without
creating files for informational commands or writing beside inspected sessions
and repositories.

## Decision

The application package owns a runtime that composes the discoverer, source
catalog, Codex, Claude, and OpenCode adapters, importer coordinator and manager,
SQLite import and projection store, default projection registrations, shared
projection service, and authoritative readers. Presentation packages receive
this runtime and do not construct infrastructure.

The database is `agentsession.db` under a platform data directory. Linux uses
XDG data and configuration conventions, macOS uses Application Support, and
Windows uses local application data for the database and roaming application
data for configuration. Explicit data and configuration overrides may be
relative to the working directory. Opening the runtime creates only the data
directory with private permissions; the resolved configuration directory is
not created until configuration exists.

Discovered paths become narrowly scoped read-only source-opening capabilities.
Discovery kinds remain advisory, and every adapter still probes content. The
runtime catalogs candidates by stable discovery ID and processes the batch CLI
workflow sequentially. Independent source failures do not suppress remaining
imports. Terminal results contain bounded diagnostics and session summaries,
not checkpoints or raw source records. Projection failures are warnings because
canonical evidence is already durable.

Shutdown first rejects new discovery and import requests, then cancels active
jobs and waits for their current SQLite transactions to commit or roll back.
Only after imports settle does it close SQLite. If the shutdown context expires,
the database remains open and a later shutdown call can retry safely.

The executable opens a runtime only for TUI, web, and import execution. Parsing
help and printing version information have no storage side effects.

## Consequences

- All runnable interfaces share one composition and lifecycle boundary.
- Session sources and repositories remain read-only; owned indexes stay in a
  separate platform location.
- Missing projection builders leave registered lifecycle states pending without
  weakening canonical import success.
- Configuration parsing, projection implementations, and new navigation remain
  separate work.
