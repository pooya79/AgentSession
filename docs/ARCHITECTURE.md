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
                      | Source discovery       |
                      +-----------+------------+
                                  |
                           discovered sources
                                  |
                                  v
                      +------------------------+
                      | Adapters + importer    |
                      | parse and normalize    |
                      +-----------+------------+
                                  |
                    atomic canonical batch + checkpoint
                                  v
                      +------------------------+
                      | Authoritative SQLite   |
                      | data                   |
                      +-----------+------------+
                                  |
                                  v
                      +------------------------+
                      | Rebuildable projections |
                      | FTS, Git, analysis     |
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
atomically persist event batch, session updates, and checkpoint
      |
      v
update search projection
      |
      v
correlate repository and Git evidence
      |
      v
run deterministic analysis and update aggregates
```

An import is incremental and idempotent. Only canonical event batches, their session updates, and the corresponding checkpoint share the import transaction. FTS indexing, repository and Git correlation, findings, outcomes, and aggregates run after commit. Failure in any of those projections is recorded and retried without invalidating otherwise durable imported evidence.

The importer records enough source identity and progress to distinguish an append from truncation, replacement, or mutation before the checkpoint. A conceptual checkpoint is:

```go
type ImportCheckpoint struct {
	SourceID       string
	ByteOffset     int64
	RecordSequence int64
	PrefixHash     string
	LastRecordHash string
	SourceSize     int64
}
```

The exact fingerprinting algorithm is adapter-aware and must remain streaming. A byte offset or record sequence alone is not a valid checkpoint. On an append, import resumes from verified prior state. On truncation, replacement, or mutation before the checkpoint, the importer safely reconciles or re-imports the authoritative records without leaving stale canonical events or producing duplicates.

Event identifiers are deterministic. Adapters choose the strongest available identity in this order:

1. A globally stable native event ID.
2. A native session ID combined with a native event ID.
3. Source identity, record sequence, and record hash.
4. Source identity, byte range, and record hash.

Source ordering is retained separately as a sequence number. Timestamps enrich the timeline but do not define identity or ordering because they may be absent, duplicated, or unreliable. Random identifiers are not used for imported events.

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
       summaries, details, and evidence
                      |
                      v
             presentation rendering
```

Presentation packages request repositories, sessions, timeline summaries, event details, file impact, commands, findings, and search results through application services. Timeline lists return lightweight records such as:

```go
type EventSummary struct {
	ID       string
	Sequence int64
	Kind     EventKind
	Summary  string
}
```

Large command output, tool output, patches, normalized payloads, and retained raw data are fetched only when an event detail view requires them. Presentation code does not interpret raw source records, calculate outcomes, or issue Git commands.

## Components and ownership

### Discovery

Discovery locates supported session sources at platform-specific default locations and user-configured paths. It reports inaccessible or malformed sources without preventing other sources from being indexed. Discovery determines where a source is; it does not interpret the source's records.

The conceptual discovery contract is:

```go
type SourceDiscoverer interface {
	Discover(ctx context.Context) ([]Source, error)
}
```

Platform-specific discoverers may identify candidate type hints, but adapters remain responsible for confirming formats. Discovery implementations do not parse records or normalize events.

### Source adapters

Each coding-agent format has an isolated adapter. An adapter owns probing, streaming parsing, source-specific identifiers, and normalization into canonical events.

The conceptual contract is:

```go
type Adapter interface {
	Name() string
	Probe(ctx context.Context, source Source) (ProbeResult, error)
	Import(ctx context.Context, source Source, sink EventSink) error
}
```

This contract documents the intended boundary, not a promise that these exact exported Go types already exist. Interfaces should ultimately be defined by their consumers and kept as small as real implementations allow.

No package outside an adapter may branch on a source name to interpret source-specific structure. Adding a source begins with a new adapter and sanitized fixtures, not conditionals in shared packages.

### Authoritative data and the canonical event model

Authoritative imported data consists of canonical sessions and events, retained raw records, and import checkpoints. This data reflects the source records that have been durably imported and is not discarded merely to refresh a query or analysis implementation.

The canonical model provides stable shared semantics, while preserved raw records prevent unsupported source information from being discarded. The combination is loss-preserving; the normalized model alone is not necessarily lossless. The event envelope contains:

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

Each imported session also records the adapter name and version, detected source-format version, canonical schema version, and normalization version. This metadata identifies data that may require re-normalization after an adapter or model change.

Raw records are retained so an adapter improvement can recover previously unrecognized information. Raw retention is a deliberate privacy and storage policy, not an incidental copy of every payload. In v0.1 the retention mode is `full`: records up to 256 KiB may be stored inline with the event, while larger records are compressed and stored as separately fetched payloads in SQLite so timeline reads remain bounded. Large command and tool output follows the same payload path and is never silently truncated in authoritative storage. These thresholds are centralized and versioned rather than adapter-specific.

Raw records are never added to the search index. A future configurable retention policy may support `unknown-only` and `none`; changing the default or allowing retention to be disabled requires an explicit privacy and recoverability decision because those modes prevent loss-preserving re-normalization. Deleting an imported session deletes its retained raw copies and associated projections, but never its read-only source files.

Raw content is untrusted and must not be rendered as HTML or written to a terminal without the sanitization appropriate to that output.

### Importer

The importer coordinates adapters, transactions, checkpoints, search indexing, and post-import projection work. It owns import lifecycle and progress reporting but delegates source parsing to adapters and persistence details to storage.

A central application-level import coordinator owns active work. It permits one active import per source, coalesces duplicate requests, and publishes one shared progress stream to both interfaces. An import is application work rather than request-scoped work: closing a TUI view or HTTP connection stops that observer but does not cancel the shared import. Process shutdown stops new requests, cancels active work, and lets the current transaction commit or roll back safely. Neither presentation layer implements a separate import path.

### Repository and Git correlation

Repository correlation connects a session's working directory and file evidence to a repository root, branch, nearby HEAD, and relevant commits when those facts are available.

Git integration invokes only a small allowlist of non-interactive, read-only commands with explicit arguments and working directories. User-controlled text is never interpolated into a shell command, and the web interface cannot request arbitrary Git execution. Correlation consumes durably stored canonical data and may use session-level context. Missing Git metadata or a correlation failure reduces available evidence and produces a projection diagnostic; it does not roll back or invalidate the imported session.

### Storage, projections, and search

SQLite stores two classes of data:

```text
Authoritative data
- sessions
- canonical events
- retained raw records
- import checkpoints

Rebuildable projections
- FTS indexes
- repository and Git correlations
- findings
- outcomes
- statistics and aggregates
```

Projection data can be deleted and reconstructed from authoritative database data without re-reading the original session source. Git projections may additionally consult the still read-only repository. Projection schemas record the version that produced them and whether rebuilding is pending or failed.

Storage details remain behind small, consumer-owned use-case interfaces rather than one interface per table. Examples include an `ImportStore` that atomically commits a batch, a `SessionReader` that lists sessions and timelines and fetches event details, and an `AnalysisStore` that replaces versioned findings and outcomes. Application code depends on these interfaces, not concrete SQLite types. Schema changes use ordered embedded migrations. Import batches use transactions, foreign keys are enabled, and tests use isolated temporary databases.

The `search` capability parses user input, validates filters, and produces a storage-neutral `SearchQuery`. The SQLite storage implementation translates that query into SQL and FTS5 expressions and owns ranking, snippets, and pagination.

Indexing favors useful evidence over payload volume:

```text
Always index
- messages, commands, and errors
- file paths and tool names
- event summaries

Conditionally index within configured size and type limits
- command and tool output
- patches

Never index
- retained raw source records
- binary content
- excessively large output
```

The pure-Go SQLite driver preserves CGO-free cross-compilation at the cost of a larger executable.

### Analysis

Analysis runs deterministic rules over canonical events. A finding is an evidence-level result, such as a repeated failing command. It contains a rule identifier and version, applicability state, severity where applicable, explanation, related event IDs, and supporting metadata. Rule evaluation supports `triggered`, `not triggered`, `not applicable`, and `insufficient evidence`; absence of a detected action is not automatically a defect. For example, a rule should report “No verification command was detected after source-code changes” only when the evidence makes source-code verification applicable.

An outcome is a separate session-level classification produced by a versioned outcome classifier. Findings and outcomes are stored independently so either rule set can be rebuilt after an upgrade without re-importing the source.

Session outcomes are:

```text
Successful
Partially successful
Failed
Abandoned
Unknown
```

Classification is conservative. `Successful` means the recorded session contains positive completion and verification evidence with no known unresolved contradiction. It describes the observed execution outcome; it is not a guarantee that the implementation is semantically correct. A final assistant claim is evidence but never proof by itself. A failed verification remains unresolved until a later relevant run succeeds. When evidence is incomplete or contradictory and no reliable rule applies, the outcome is `Unknown`.

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

The HTTP server listens on localhost by default. Session IDs, file paths, queries, and all rendered source content are untrusted inputs. Handlers validate identifiers and paths, templ escapes dynamic HTML content, and exported reports pass through redaction before output-specific rendering.

## Dependency direction

Dependencies point inward toward the domain and consumer-owned application interfaces:

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
          app services <---------------------------+
                |                                  |
       +--------+-------------------+              |
       |        |          |        |              |
       v        v          v        v              |
   importer   search    analysis   export          |
       |                   |                       |
       +-------------------+                       |
                |                                  |
                v                                  |
              domain                               |
                                                   |
 implementations of consumer-owned interfaces -----+
   adapters      SQLite storage      Git CLI
```

Presentation calls application services; importer and analysis operate on domain types. Adapters implement source-format interfaces, SQLite implements storage and query interfaces, and the controlled Git CLI wrapper implements Git evidence interfaces. Application and domain code do not depend directly on SQLite, FTS5, or process execution.

The diagram expresses architectural ownership, not a requirement for every package to import `model` directly. In particular:

- `model` imports no adapter, storage, analysis, or presentation package.
- adapters do not import storage or presentation packages.
- storage does not interpret source formats or depend on presentation.
- analysis consumes canonical evidence and does not parse raw source formats.
- `tui` and `web` depend on application-facing interfaces, not concrete storage or adapters.
- application services, importer, and analysis define the narrow interfaces they consume.
- SQLite, Git, and adapter implementations are wired to those interfaces at the process boundary.
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
  search/                query parsing and filter validation
  redaction/             secret detection and removal
  sanitization/          output-context safety transformations
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

Redaction and sanitization are separate operations:

- Redaction detects and removes secrets such as API keys, passwords, tokens, authorization headers, and private keys.
- Sanitization prevents an output channel from interpreting untrusted content. It addresses HTML injection and terminal controls including ANSI escapes, OSC clipboard commands, terminal-title changes, deceptive hyperlinks, and bidirectional control characters.

Exports operate on structured data, redact it before rendering, and then use an output-specific renderer for HTML, JSON, or Markdown. Renderers must escape or sanitize for their destination even after redaction; redaction is not an interface-security boundary.

No raw or normalized session content is ever written directly to a terminal. All TUI text, diagnostics containing source content, previews, and terminal-bound exports pass through the terminal sanitizer immediately before rendering. This is a system invariant enforced at terminal output boundaries, not an optional presentation detail.

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
2. Produce a stable rule identifier and version, applicability state, severity where applicable, explanation, and related event IDs.
3. Cover triggered, not-triggered, not-applicable, contradictory, and insufficient-evidence cases with table-driven tests.
4. Expose the result through shared application services so both interfaces remain consistent.

Changes to module boundaries, the canonical model, database ownership, privacy guarantees, or outcome semantics require an architecture decision record in `docs/decisions/`.
