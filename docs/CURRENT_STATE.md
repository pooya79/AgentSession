# AgentSession current state

This document records the observed state of AgentSession at one point in its
development. It describes what exists in the repository, not what the product
eventually should become. Product intent is defined separately in
[PRODUCT.md](PRODUCT.md).

## Snapshot

- Date: 2026-07-29
- Commit: `9f403f0`
- Phase: pre-v0.1, with a usable exploration and search foundation

This snapshot is based on repository code, tests, fixtures, and documentation.
It is not a release-readiness assessment or a substitute for validation with a
representative collection of real sessions.

## Working capabilities

### Source discovery and import

- Default and explicitly configured sources can be discovered for Codex CLI,
  Claude Code, and OpenCode.
- Source adapters own probing, parsing, identity, and normalization.
- Codex and Claude JSONL inputs are processed as streams. OpenCode databases are
  read through a query-only snapshot and expanded into logical session sources.
- Imports preserve source order and stable canonical identities.
- Canonical sessions, events, original records, diagnostics, and checkpoints
  are stored in an AgentSession-owned SQLite database.
- Imports are incremental and batched. Previously imported source state is
  verified, and changed, truncated, or replaced sources use staged
  reconciliation.
- Malformed complete records and unsupported evidence can be retained without
  preventing later valid records from being imported.
- Failures in one discovered source do not prevent unrelated sources from being
  attempted.

### Canonical exploration

- The application exposes shared services for library totals, bounded session
  lists, ordered timelines, event details, interpretation coverage, and import
  diagnostics.
- Sessions can expose normalized messages, tool calls and results, commands,
  file reads and mutations, patches, usage, errors, summaries, and Unknown
  evidence when supplied and understood by an adapter.
- Evidence state distinguishes complete, partial, unavailable, and not-found
  results.
- Interpretation coverage separately reports unsupported events and malformed
  records.
- Retained content for an Unknown event can be inspected through an explicit,
  bounded, redacted action.
- Timeline payloads are summary-only by default. The terminal interface opts
  into bounded normalized payload batches and appends 50-event chunks into one
  continuously scrollable card timeline; raw records remain excluded.

### Search

- Full-text search is implemented as a rebuildable projection over canonical
  evidence.
- Search is available through the same application service to both interfaces.
- Text terms and phrases can be combined with supported structured filters for
  session, event kind, time, file, tool, and command.
- Search results use bounded pagination and link back to canonical event
  context.
- Search availability reports complete, partial, or unavailable projection
  coverage rather than silently using stale data.
- Retained raw records and unknown raw payloads are not implicitly indexed.

### Interfaces and commands

- The executable provides a terminal interface, a localhost web interface, an
  explicit import command, version information, and guarded local-database
  removal.
- Both interactive interfaces start application-owned discovery and import
  work and can observe indexing progress without owning the worker lifecycle.
- Both interfaces provide session browsing, timelines, event inspection,
  indexing diagnostics, projection status and actions, and search. The TUI
  presents ordinary sessions as a conversation with distinct user and
  assistant messages, while technical activity stays compact until expanded;
  search results retain standalone event-detail navigation.
- The web interface is server-rendered and remains functional for its main
  operations without JavaScript. Embedded htmx provides bounded partial
  updates when available.
- Presentation packages consume shared application services. Contract tests
  exercise service parity between the TUI and web layers.

### Local operation

- Normal operation uses local source records and an application-owned local
  SQLite index.
- The web server listens on localhost by default.
- Runtime templates, migrations, styles, and browser assets needed by the
  application are embedded or compiled into the executable.
- No account, API key, external model, telemetry service, or runtime Node
  dependency is required.
- Source sessions and OpenCode databases are opened through read-only
  capabilities; the application does not run recorded commands.

## Partial capabilities

### Source interpretation

The three adapters recognize a meaningful set of observed and fixture-backed
records, but normalization is not complete for every source version or nested
variant. Valid unsupported information becomes Unknown evidence, and malformed
known structures become diagnostics.

Some fixture coverage is necessarily synthetic. In particular, the documented
OpenCode storage generations were derived from supplied schemas and fixtures
rather than a broad collection of observed production databases.

### Session understanding

The application provides ordered evidence, summaries, diagnostics, and search,
but it does not yet reduce a long session into a concise explanation of what
was important, what changed, or what remained unresolved.

Session titles, summaries, previews, and timestamps depend on evidence that
varies by source. Their usefulness across a representative real-world library
has not yet been established.

### File-related evidence

Canonical event kinds exist for file reads, file mutations, and patches, and
adapters normalize supported source records into them. Coverage depends on what
each source records and what its adapter currently understands.

This evidence describes recorded session activity. It is not yet correlated
with repository state and does not by itself prove the final state of a file.

### Usage information

Adapters can normalize available token counters into usage events. Coverage and
token categories vary by source, and malformed counters are diagnosed rather
than invented.

There is no user-facing aggregate-usage service or statistics page yet. The
current library overview is limited to exact totals for sessions, events,
distinct agents, and sessions with evidence issues.

### Projection lifecycle

The application durably models lifecycle state for search, Git correlation,
findings, outcomes, and aggregates. Only search currently has an implemented
builder. The remaining projection kinds are reported as unavailable in the
current build rather than treated as ready.

## Not yet implemented

- Repository and Git correlation.
- Deterministic analysis findings.
- Session outcome classification.
- Cross-session aggregate projections and a dedicated statistics experience.
- Aggregate token, tool, command, model, provider, or time-series reporting.
- Structured export workflows.
- A filesystem watcher or periodic background rescan.
- Agent execution, session resumption, or repository modification; these are
  outside the intended product boundary rather than planned omissions.

## Supported sources

### Codex CLI

The adapter supports legacy event-based and current ordinal-bearing JSONL
history represented by the repository fixtures. It streams complete records,
defers incomplete trailing input, preserves unknown and malformed evidence, and
normalizes supported messages, commands, tool activity, file evidence, usage,
errors, and summaries where present.

Coverage of future Codex record shapes is intentionally incomplete and should
degrade to Unknown evidence or diagnostics instead of speculative
normalization.

### Claude Code

The adapter supports JSONL session files, including supported message and tool
content, metadata, snapshots, sidechains, and agent logs represented by the
fixtures. It preserves malformed and unsupported variants and assigns distinct
canonical identities to agent-owned logs.

Coverage varies across Claude record and content-block variants. Unsupported
nested content remains visible as partial interpretation.

### OpenCode

The adapter supports the legacy `session`/`message`/`part`, sequenced
`session_message`, and durable `event_sequence`/`event` storage generations
represented by the repository fixtures. It chooses one generation per logical
session and does not merge incompatible generations.

The adapter operates against a query-only snapshot and retains selected rows
with their SQLite value types. Broader validation against production OpenCode
databases remains necessary.

## Known product gaps

- A user can browse a long timeline, but the product does not yet help them
  identify its most important moments or unresolved work.
- Search is technically capable, but its effectiveness for recovering vaguely
  remembered sessions has not been validated with users or a large personal
  session library.
- File-related events are not yet connected to repository evidence, so the
  product cannot confirm the resulting repository state.
- Normalized usage events exist, but users cannot yet explore meaningful usage
  or activity patterns across sessions.
- Projection infrastructure is substantially ahead of the user-facing
  findings, outcomes, correlations, and aggregates it is intended to support.
- The product contract calls for useful cross-session insight, while the
  current dashboard offers only a small set of operational totals.
- The core promise of being easier and faster than raw-record inspection has
  not yet been measured or validated.

## Known technical constraints

- Source formats are controlled by other tools and may change without notice.
- Normalization quality is limited by available sanitized examples and
  understanding of each source format.
- Different sources may describe similar concepts with meanings that are not
  safely comparable.
- Search covers selected normalized textual fields, not every retained byte.
- Imported raw evidence can contain sensitive information and requires careful,
  explicit handling at presentation and export boundaries.
- The current executable has no scheduler for automatically retrying failed or
  pending projection work.

## Areas needing validation

- Import behavior against large and diverse real-world session libraries.
- Adapter coverage across actively used versions of each supported agent.
- Whether session ordering, previews, and search make remembered sessions easy
  to recover.
- Whether timelines remain understandable and responsive for very long
  sessions and large individual events.
- Whether evidence state, interpretation coverage, and projection readiness
  are understandable without knowledge of the architecture.
- Whether users can distinguish source-recorded errors from AgentSession import
  or projection diagnostics.
- Which cross-session statistics are genuinely useful and safely comparable.
- Cross-platform behavior and release packaging on supported operating systems.

## Keeping this document current

Update this snapshot when a milestone materially changes product capability or
invalidates one of the gaps above. Keep planned behavior in the product
contract, architecture guide, decisions, or roadmap rather than presenting it
here as implemented.
