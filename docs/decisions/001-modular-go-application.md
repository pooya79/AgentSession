# ADR-001: Modular Go application with two presentation layers

## Status

Accepted

## Context

AgentSession must process large local session files, provide terminal and browser interfaces, run across Linux, macOS, and Windows, and ship without external services or runtime dependencies. One developer should be able to maintain it.

## Decision

AgentSession will be a modular Go monolith with one process, one local SQLite database, and one shared application layer. Source adapters normalize records into a canonical event model; import, deterministic analysis, storage, and search sit behind shared services consumed by both interfaces.

The intended stack is Bubble Tea for the TUI, `net/http` and templ for the web interface, htmx for later partial updates, SQLite FTS5 for storage and search, `go:embed` for runtime assets, and an allowlisted set of read-only calls to the installed Git executable.

## Consequences

- The project can ship as one native executable with shared behavior across both interfaces.
- Source-specific interpretation stays inside adapters.
- TUI and HTML presentation remain separate even though their business logic is shared.
- A pure-Go SQLite implementation will increase binary size.
- Platform-specific discovery and Git behavior require cross-platform tests.
