# AGENTS.md

## Project

AgentSession is a lightweight, local-first explorer for coding-agent sessions. It turns records from tools such as Claude Code, Codex CLI, and OpenCode into searchable, repository-aware timelines of messages, commands, file changes, tests, errors, recovery attempts, and outcomes.

The application is an observer, not an agent runner. It must remain offline-capable, read-only with respect to source sessions and repositories, and distributable as a single native executable.

## Current state

This repository is at an early scaffold stage. Before changing code, inspect the repository and follow the commands and conventions that actually exist. Do not introduce speculative infrastructure or broad abstractions solely to match the intended layout below.

## Architecture

Build a modular Go monolith with one shared application layer and two presentation layers:

```text
session sources -> discovery/adapters -> canonical events -> importer/analysis
                                                        -> SQLite/search
                                                        -> services -> TUI and web
```

Intended technology choices:

- Go
- Bubble Tea for the TUI
- `net/http`, templ, and htmx with minimal JavaScript for the web UI
- SQLite with FTS5 for persistence and search
- `go:embed` for migrations and static assets
- the installed `git` executable for a small, controlled set of read-only operations
- GoReleaser for cross-platform releases

Prefer packages under `internal/`, grouped by capability: `app`, `model`, `adapter`, `discovery`, `importer`, `analysis`, `git`, `storage`, `search`, `redaction`, `export`, `tui`, and `web`. Put the executable entry point under `cmd/agentsession/`.

## Non-negotiable boundaries

- Source adapters own all source-specific parsing and normalization. Source checks do not belong in storage, services, analysis rules, the TUI, or the web UI.
- Both interfaces call the same application services. Do not duplicate business, search, outcome, or analysis logic in presentation code.
- Stream large session files; do not read complete logs into memory.
- Preserve the original raw record alongside normalized data, including unknown records.
- Generate stable event identifiers and retain source ordering.
- Imports must be incremental, idempotent, batched, and safe when a source is truncated or replaced.
- Original session files and inspected repositories are read-only. Store AgentSession indexes and settings separately.
- Git integration must use an allowlisted set of read-only commands. Never expose arbitrary command execution through the web interface.
- The web server listens on localhost by default.
- Keep the default product deterministic and offline. Do not add an LLM, telemetry, cloud dependency, account system, or upload path.

## Evidence and analysis

Analysis is rule-based. Every outcome or finding must include a human-readable explanation and references to the events that support it. Never infer success solely from an assistant's final claim.

Use these outcome values: `Successful`, `Partially successful`, `Failed`, `Abandoned`, and `Unknown`. Prefer `Unknown` when the available evidence is insufficient. A failed test remains unresolved unless a later relevant run succeeds.

Treat session content, command output, patches, paths, environment values, and Git data as untrusted input. Escape rendered content, validate paths and identifiers, redact secrets in exports, and avoid logging sensitive raw records.

## Go conventions

- Keep packages cohesive and dependencies directed toward the canonical model and application services.
- Accept `context.Context` on I/O and long-running operations; honor cancellation during discovery and import.
- Wrap errors with useful operation and source context. Preserve causes for `errors.Is` and `errors.As`.
- Prefer explicit types and small interfaces defined by their consumers. Avoid global mutable state and premature generic frameworks.
- Keep database behavior behind storage interfaces. Use transactions for import batches and migrations for schema changes.
- Format changed Go files with `gofmt`. Do not manually edit generated templ output.

## Testing

- Add focused unit tests beside implementation code.
- Test every adapter with small, sanitized fixtures, including malformed, unknown, partial, and very large-record cases.
- Assert stable IDs, event ordering, resumable imports, duplicate prevention, truncation handling, and preservation of raw data.
- Use table-driven tests for deterministic analysis and outcome rules, covering contradictory and incomplete evidence.
- Test storage against a temporary database; do not depend on a developer's real session directories, repository, home directory, network, locale, or clock.
- Add parity tests at the service boundary when a feature is exposed by both TUI and web.
- Run the narrowest relevant tests while iterating, then the complete project checks documented by the repository before handing off.

## Change discipline

- Keep changes small and scoped to the request. Do not combine unrelated cleanup with feature work.
- Do not silently change the canonical event model, database schema, outcome semantics, CLI behavior, or privacy guarantees. Document consequential architecture decisions under `docs/decisions/`.
- New source formats begin as isolated adapters with fixtures; they must not leak source-specific fields into shared layers without an explicit model decision.
- New findings must be deterministic, explainable, and backed by tests and event references.
- Maintain single-binary distribution: embed required migrations, templates, CSS, and static assets; do not introduce a runtime Node or external-service requirement.
- Update user-facing documentation when commands, configuration, supported sources, or behavior changes.

## Completion checklist

Before declaring work complete:

1. Confirm the implementation respects read-only, local-first behavior.
2. Run formatting, generation, tests, and static checks that exist in the repository.
3. Check that errors and partial evidence are represented honestly rather than collapsed into success.
4. Verify both interfaces use shared services when the change is user-visible in both.
5. Report what changed, which checks ran, and any remaining limitations.
