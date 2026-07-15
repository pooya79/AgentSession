# AgentSession architecture

This document describes the target architecture for AgentSession v0.1. It explains the system boundaries, data flow, component ownership, and dependency rules contributors should preserve as the application grows.

For the decision behind this design, see [ADR-001](decisions/001-modular-go-application.md). For day-to-day engineering rules, see [AGENTS.md](../AGENTS.md).

## Goals and constraints

AgentSession is a local forensic explorer for coding-agent activity. Its architecture prioritizes:

- **Local operation:** indexing, search, and analysis work without an account, network service, API key, or model connection.
- **Read-only inputs:** original session records and inspected repositories are never modified.
- **Evidence-backed results:** outcomes and findings link to the events that support them.
- **Deterministic behavior:** v0.1 analysis uses explicit, testable rules rather than generated judgments.
- **Streaming imports:** large logs are processed incrementally instead of loaded fully into memory.
- **Shared behavior:** the TUI and web interface use the same application services.
- **Portable distribution:** each release is one native executable containing its runtime assets and migrations.

AgentSession does not run or resume coding agents, restore repository state, replace Git, or upload session data.

## System overview

AgentSession is a modular monolith: one executable, one local database, and one application layer behind two presentation layers.

```text
Read-only inputs

  Claude Code sessions    Codex CLI sessions    OpenCode sessions
           |                      |                     |
           +----------------------+---------------------+
                                  |
                                  v
                      +------------------------+
                      | Discovery and adapters |
                      +-----------+------------+
                                  |
                         canonical event stream
                                  |
                                  v
                      +------------------------+
                      | Incremental importer   |
                      +-----+-------------+----+
                            |             |
                            v             v
                 +-------------+   +------------------+
                 | Repository  |   | Deterministic    |
                 | correlation |   | analysis         |
                 +------+------+   +---------+--------+
                        |                    |
                        +---------+----------+
                                  |
                                  v
                      +------------------------+
                      | SQLite storage + FTS5  |
                      +-----------+------------+
                                  |
                                  v
                      +------------------------+
                      | Application services   |
                      +-----------+------------+
                                  |
                           +------+------+
                           |             |
                           v             v
                      +---------+   +----------+
                      | TUI     |   | Web UI   |
                      | Bubble  |   | net/http |
                      | Tea     |   | + templ  |
                      +---------+   +----------+
```

Modules are package boundaries inside the executable, not separately deployed services. Calls between modules remain ordinary Go calls, and long-running or I/O-bound operations accept `context.Context`.

## Core data flow

### Import flow

```text
discover source
      |
      v
probe format and identity
      |
      v
stream source records ---- malformed record ----> import diagnostic
      |
      v
normalize to canonical events
      |
      +---- unknown record ----> canonical Unknown event + preserved raw data
      |
      v
correlate repository and Git evidence
      |
      v
write events and import checkpoint in one batch transaction
      |
      v
update full-text index and deterministic findings
```

An import is incremental and idempotent. The importer records enough source identity and progress to avoid duplicating completed work. It detects when a source has been truncated or replaced and chooses a safe re-import path rather than trusting a stale byte offset.

Stable identifiers are derived from stable source identity and record identity or position. Source ordering is retained separately as a sequence number. Timestamps enrich the timeline but do not define identity or ordering because they may be absent, duplicated, or unreliable.

The importer batches writes to bound memory use and database overhead. A committed batch contains its events and corresponding checkpoint together, so interruption cannot advance progress beyond durable data.

### Query flow

```text
TUI or HTTP request
        |
        v
application service
        |
        +---- structured filters ----+
        |                            |
        v                            v
SQLite queries                  FTS5 queries
        |                            |
        +-------------+--------------+
                      |
                      v
       canonical view data and evidence
                      |
                      v
             presentation rendering
```

Presentation packages request repositories, sessions, timelines, file impact, commands, findings, and search results through application services. They do not interpret raw source records, calculate outcomes, or issue Git commands.

## Components and ownership

### Discovery

Discovery locates supported session sources at platform-specific default locations and user-configured paths. It reports inaccessible or malformed sources without preventing other sources from being indexed. Discovery determines where a source is; it does not interpret the source's records.

### Source adapters

Each coding-agent format has an isolated adapter. An adapter owns probing, streaming parsing, source-specific identifiers, and normalization into canonical events.

The conceptual contract is:

```go
type Adapter interface {
	Name() string
	Discover(ctx context.Context) ([]Source, error)
	Probe(ctx context.Context, source Source) (ProbeResult, error)
	Import(ctx context.Context, source Source, sink EventSink) error
}
```

This contract documents the intended boundary, not a promise that these exact exported Go types already exist. Interfaces should ultimately be defined by their consumers and kept as small as real implementations allow.

No package outside an adapter may branch on a source name to interpret source-specific structure. Adding a source begins with a new adapter and sanitized fixtures, not conditionals in shared packages.

### Canonical event model

The canonical model is the lossless boundary between source-specific input and shared behavior. The event envelope contains:

```text
ID                 stable event identifier
Session ID         owning canonical session
Sequence           source order within the session
Timestamp          optional recorded time
Kind               normalized event category
Summary            concise human-readable description
Searchable text    normalized text for search
Normalized data    typed shared fields for the event kind
Raw data           original source record
```

Initial event categories are user and assistant messages, tool calls and results, command executions, file reads and mutations, patches, usage records, errors, summaries, and unknown source events.

Raw records are retained so an adapter improvement can recover previously unrecognized information. Raw content is untrusted and must not be rendered as HTML or terminal control sequences without appropriate escaping or sanitization.

### Importer

The importer coordinates adapters, transactions, checkpoints, search indexing, and post-import analysis. It owns import lifecycle and progress reporting but delegates source parsing to adapters and persistence details to storage.

Both interfaces observe the same progress model. Neither presentation layer implements a separate import path.

### Repository and Git correlation

Repository correlation connects a session's working directory and file evidence to a repository root, branch, nearby HEAD, and relevant commits when those facts are available.

Git integration invokes only a small allowlist of non-interactive, read-only commands with explicit arguments and working directories. User-controlled text is never interpolated into a shell command, and the web interface cannot request arbitrary Git execution. Missing Git metadata reduces available evidence; it does not invalidate the imported session.

### Storage and search

SQLite is the durable store for canonical sessions, events, import checkpoints, repository associations, findings, settings, and diagnostics. FTS5 indexes searchable event text and related repository and session metadata.

Storage details remain behind repository-style interfaces consumed by application services and import workflows. Schema changes use ordered embedded migrations. Import batches use transactions, foreign keys are enabled, and tests use isolated temporary databases.

The pure-Go SQLite driver preserves CGO-free cross-compilation at the cost of a larger executable.

### Analysis

Analysis runs deterministic rules over canonical events. A finding contains a rule identifier, severity, explanation, related event IDs, and supporting metadata. Rules may identify evidence such as unresolved failures, repeated failed commands, missing verification, excessive loops, or a success claim following an unresolved error.

Session outcomes are:

```text
Successful
Partially successful
Failed
Abandoned
Unknown
```

Classification is conservative. A final assistant claim is evidence but never proof by itself. A failed verification remains unresolved until a later relevant run succeeds. When evidence is incomplete or contradictory and no reliable rule applies, the outcome is `Unknown`.

### Application services

Application services form the shared use-case boundary for both interfaces. They coordinate storage, search, analysis results, repository evidence, redaction, and exports. Expected capabilities include:

- list repositories and sessions;
- read a session timeline and event details;
- inspect file impact, commands, failures, and findings;
- search across imported sessions;
- initiate imports and observe progress;
- export a redacted report.

Services return canonical view data rather than UI-specific components. Presentation concerns stay in `tui` and `web`.

### Presentation layers

The Bubble Tea TUI is the default interface. The web interface uses `net/http`, templ, htmx partial updates where useful, and minimal JavaScript. Both expose the same underlying evidence even when their navigation and rendering differ.

The HTTP server listens on localhost by default. Session IDs, file paths, queries, and all rendered source content are untrusted inputs. Handlers validate identifiers and paths, templ escapes dynamic HTML content, and exported reports pass through redaction.

## Dependency direction

Dependencies point inward toward shared models and use cases:

```text
cmd/agentsession
       |
       v
      cli --------------+
       |                 |
       v                 v
      tui               web
       |                 |
       +--------+--------+
                |
                v
          app services
                |
       +--------+-------------------+
       |        |          |        |
       v        v          v        v
   importer   search    analysis   export
       |        |          |        |
       +--------+----------+--------+
                |
                v
        canonical model
                ^
                |
  adapters   storage   Git integration
```

The diagram expresses architectural ownership, not a requirement for every package to import `model` directly. In particular:

- `model` imports no adapter, storage, analysis, or presentation package.
- adapters do not import storage or presentation packages.
- storage does not interpret source formats or depend on presentation.
- analysis consumes canonical evidence and does not parse raw source formats.
- `tui` and `web` depend on application-facing interfaces, not concrete storage or adapters.
- `cli` performs process wiring; business rules do not live there.

Cycles between capability packages are architectural defects and should be resolved by moving shared types inward or defining a consumer-owned interface.

## Target repository layout

```text
cmd/agentsession/        process entry point
internal/
  app/                   shared application services and wiring
  model/                 canonical domain and event types
  adapter/
    claude/              Claude Code format
    codex/               Codex CLI format
    opencode/            OpenCode format
  discovery/             source location and change detection
  importer/              import orchestration and checkpoints
  analysis/              deterministic findings and outcomes
  git/                   allowlisted read-only Git operations
  storage/sqlite/         SQLite repositories and migrations
  search/                query parsing and FTS coordination
  redaction/             secret detection and safe rendering data
  export/                redacted report generation
  tui/                   Bubble Tea presentation
  web/                   HTTP, templ components, and static assets
migrations/              embedded ordered database migrations
fixtures/                 sanitized adapter and analysis fixtures
docs/decisions/           architecture decision records
```

Directories should be introduced with working code rather than created as empty placeholders. A capability remains within its owning package until a demonstrated boundary justifies splitting it further.

## Cross-cutting concerns

### Data ownership and privacy

Agent session files and repositories belong to the user and are immutable inputs. AgentSession owns only its configuration, indexes, database, caches, and exports. These are stored outside source session directories and inspected repositories.

Session content can contain secrets in prompts, environment output, commands, patches, and paths. Logs avoid raw records by default. User-facing exports are redacted, explicitly initiated, and written only to the requested local destination.

### Errors and partial evidence

A malformed record, inaccessible repository, unavailable Git executable, or unsupported source field becomes a diagnostic or missing-evidence state where possible. One bad source does not stop discovery or import of unrelated sources.

Errors retain operation and source context and preserve their causes for programmatic inspection. Interfaces distinguish complete results, partial results with diagnostics, and unavailable results rather than presenting partial evidence as success.

### Concurrency and cancellation

Background discovery and imports are bounded and cancellable. A single source is not imported concurrently by multiple workers. Database writes are serialized or retried with bounded backoff, while read operations remain responsive. Shutdown stops accepting new work, cancels active operations, commits or rolls back the current batch, and closes resources cleanly.

### Distribution

Templates, CSS, migrations, and required static assets are embedded with `go:embed`. The released executable does not require a Node runtime, external database, or frontend service. GoReleaser produces native binaries for supported Linux, macOS, and Windows targets with build metadata injected at link time.

## Extending the system

To add a source adapter:

1. Add sanitized fixtures for valid, malformed, partial, and unknown records.
2. Implement probing and streaming normalization in an isolated adapter package.
3. Preserve unknown records and produce stable identifiers and ordering.
4. Register the adapter in application composition.
5. Verify shared storage, analysis, TUI, and web behavior without source-specific branches.

To add an analysis rule:

1. Define the precise canonical evidence the rule consumes.
2. Produce a stable rule identifier, severity, explanation, and related event IDs.
3. Cover success, failure, contradictory evidence, and insufficient evidence with table-driven tests.
4. Expose the result through shared application services so both interfaces remain consistent.

Changes to module boundaries, the canonical model, database ownership, privacy guarantees, or outcome semantics require an architecture decision record in `docs/decisions/`.
