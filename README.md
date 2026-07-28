# AgentSession

AgentSession is a lightweight, local-first explorer for coding-agent sessions. It turns local records from coding agents into repository-aware evidence about messages, commands, file changes, tests, failures, and outcomes.

The project provides a first local web workflow for discovering, importing,
and browsing normalized session evidence. Read-only source discovery,
authoritative import storage, verified bounded import orchestration, and
adapters for Codex CLI, Claude Code, and OpenCode and a rebuildable full-text
search projection are implemented; further analysis remains under development.

## Supported session sources

- Codex CLI rollout JSONL files, including legacy event-based history and
  current ordinal-bearing history. Imports stream complete records, retain raw
  bytes and unknown variants, defer incomplete trailing records, and verify
  checkpoints before append or reconciliation.
- Claude Code JSONL session files. Imports preserve mixed message and tool
  content in source order, retain snapshots, sidechains, malformed records,
  and unknown variants, and use the same verified append and reconciliation
  guarantees.
- OpenCode SQLite databases using legacy `session`/`message`/`part`, sequenced
  `session_message`, or durable `event_sequence`/`event` storage. Each database
  is read through one query-only snapshot and expands into one stable logical
  source per OpenCode session. A complete durable sequence takes precedence,
  then populated sequenced messages, then legacy rows; generations are never
  merged. Complete selected rows, including unknown columns and exact
  TEXT/BLOB values, are retained.

The application composes discovery and all adapters behind one shared runtime.
Canonical imports are stored locally in SQLite. Both interactive interfaces
index automatically at startup, while the import command remains available for
an explicit command-line run.

## Session source discovery

The discovery package locates candidate session files without parsing their contents. It checks these defaults:

- Codex CLI: `$CODEX_HOME/sessions`, falling back to `~/.codex/sessions`
- Claude Code: `$CLAUDE_CONFIG_DIR/projects`, falling back to `~/.claude/projects`
- OpenCode: `$XDG_DATA_HOME/opencode/opencode.db`, falling back to `~/.local/share/opencode/opencode.db`

Callers may also provide tool-typed files or directories explicitly. Missing default locations are treated as tools that are not installed. Inaccessible or structurally malformed locations produce diagnostics without suppressing valid sources found elsewhere. Discovery opens candidate files only to verify read access; source adapters remain responsible for probing and parsing their formats.

## Requirements

- Go 1.26 or newer
- Git

No Node.js runtime, account, cloud service, or API key is required.

## Getting started

Run the terminal interface:

```bash
go run ./cmd/agentsession
```

The TUI immediately starts or joins an asynchronous scan of every supported
source and loads already imported sessions at the same time. Indexing is
application-owned, so it continues while you browse timelines and event
details. The sessions dashboard reports exact indexed session, event, agent,
and evidence-issue totals independently of the paginated list. Sessions are
ordered by newest recorded activity (session end, latest timestamped event, or
session start), with unknown activity last. Each row identifies the source
agent, such as `CODEX`, `CLAUDE`, or `OPENCODE`, and uses the normalized
session summary or first user message as a bounded preview when available.
The dashboard refreshes when an observed scan finishes and whenever you return
from a timeline.

Use `↑`/`↓` or `j`/`k` to move, Home/End or `g`/`G` to jump to the first or
last item, `Enter` to open a session or event, and `n`/`p` or
PageDown/PageUp to change bounded pages. Press `?` for complete,
screen-specific keyboard help and `Esc` to close help or return to the parent
screen. The interface adapts the metric cards and session table for wide,
narrow, and short terminals while keeping every action available through the
help panel. Times on the terminal dashboard are displayed deterministically in
UTC.

Press `i` to inspect source-level indexing progress and diagnostics. From a
timeline, press `x` to inspect derived projection readiness separately from
canonical evidence. The projection panel distinguishes durable lifecycle state
from builders that are not implemented in the current executable. Use `t` to
retry implemented pending or failed projections and `b` to rebuild an
implemented selected kind. Rebuild-all is offered only when every registered
projection has a builder, preventing invalidation of output the current runtime
cannot reconstruct. Projection work is application-owned and continues after
leaving the panel.

Press `/` to open the search screen. Enter edits the query, `n`/`p` traverse
bounded result pages, and Enter on a result opens its canonical event detail.
Search results are available only for sessions whose search projection is
ready for the current canonical revision; the screen reports complete,
partial, or unavailable coverage rather than falling back to stale rows.

Press `r` on the sessions or indexing screen to rescan all sources and reload
sessions; on timeline and event screens it reloads the current evidence, and
in the projection panel it refreshes status. Refresh failures retain the last
successfully loaded evidence and expose a retry action. Press `q` or Ctrl-C to
exit. Event lists fetch
only lightweight summaries; normalized payload JSON is loaded when an event is
opened, and retained raw record contents are never displayed.

Start the local web interface:

```bash
go run ./cmd/agentsession web
```

The web server listens on `127.0.0.1:8080` by default. Use `--addr` to select another address:

```bash
go run ./cmd/agentsession web --addr 127.0.0.1:9000
```

Starting the web server triggers one asynchronous discovery and incremental
import of every supported source. `GET /` is the sessions operations console:
it renders the canonical-index status, exact Sessions, Events, Agents, and
Evidence Issues totals, and a bounded previous/next session page. `GET
/indexing` is the detailed latest-scan view for sources, phases, totals,
failures, omissions, and bounded diagnostics. Its rescan form starts the same
idempotent workflow after local histories change; there is no filesystem
watcher or periodic background scan.

`GET /search` provides the same shared search service as the TUI. Bare terms
are implicitly combined with AND and double quotes select a phrase. The
following filters are supported; repeated values in one category are ORed,
while different categories are ANDed:

- `session:` and `kind:` select exact canonical identifiers.
- `after:` and `before:` are strict timestamp bounds. Values are RFC3339 or
  UTC `YYYY-MM-DD`; events without timestamps do not match either bound.
- `file:` performs case-folded, slash-normalized path-prefix matching.
- `tool:` uses case-insensitive equality.
- `command:` uses case-insensitive substring matching.

Queries are limited to 4 KiB, 32 clauses, and 1 KiB per filter value. Raw FTS
operators, unknown filters, malformed quotes, and malformed escapes are
rejected. Results use opaque keyset cursors with 50 rows by default and 200 at
most; a projection rebuild safely invalidates existing cursors.

Search is derived exclusively from canonical SQLite evidence. It indexes a
bounded summary and eligible normalized message, error, summary, tool,
command, output, patch, and file-path fields. A textual field containing NUL,
invalid UTF-8, or more than 64 KiB is excluded as a whole; command text is
limited to 16 KiB, paths to 4 KiB, and tool names to 512 bytes. Retained raw
records, unknown raw payloads, binary content, and direct normalized
`data_json` are never searched.

Session timelines use `GET /sessions/{session}`. Event links focus and expand a
bounded timeline window through `?event={event}`; `GET /events/{event}` resolves
the canonical destination and redirects there. Timeline pagination, event
inspection, rescans, projection actions, and rebuild-all confirmation all work
through ordinary links and forms when JavaScript is disabled. When htmx is
available it only refreshes bounded page regions and conditionally polls while
an import or projection operation is active. Polling is the production update
mechanism; there is no event-stream endpoint.

Session and timeline diagnostics remain visible when evidence is partial or
unavailable. Resolved canonical event references link to their focused
timeline; missing or mismatched references and raw-record references remain
non-links. Normalized payloads are fetched only for an opened event, and
retained raw record contents are never exposed by the web UI. Projection
readiness is secondary to canonical evidence and distinguishes usable, pending,
running, failed, stale, and unavailable-in-this-build states. Rebuilding every
projection is offered only when all registered builders are available and uses
a separate server-rendered confirmation page.

The web command accepts the same repeatable, typed source flags as the import
command. Explicit paths supplement default discovery locations:

```bash
go run ./cmd/agentsession web \
  --codex ./saved-codex-sessions \
  --claude ./claude-session.jsonl \
  --opencode ./opencode.db
```

Discover the standard source locations and import every candidate:

```bash
go run ./cmd/agentsession import
```

Additional source files or directories can be supplied with repeatable typed
flags. They supplement the standard locations and overlapping candidates are
deduplicated:

```bash
go run ./cmd/agentsession import \
  --codex ./saved-codex-sessions \
  --claude ./claude-session.jsonl \
  --opencode ./opencode.db
```

By default, the index is stored in the platform application-data directory as
`agentsession.db`. Global `--data-dir` and `--config-dir` flags override the
resolved directories; relative overrides are resolved from the working
directory. Help and version commands do not create either directory.

Print build information:

```bash
go run ./cmd/agentsession version
```

## Development

Common tasks are available through the Makefile:

```bash
make generate  # generate Go code from templ components
make fmt       # format templ and Go sources
make check     # verify generation, vet, and test
make build     # write the executable to bin/agentsession
make run       # run the TUI
make web       # run the web interface
make remove-db # remove the index after all AgentSession processes stop
```

`make remove-db` uses the same platform data-directory resolution as the
application and refuses to remove an index that a running AgentSession process
has open. Set `DATA_DIR` to remove an index created with `--data-dir`.

## Planned architecture

AgentSession is designed as a modular Go monolith. Source-specific adapters stream records into a canonical event model, followed by deterministic analysis and SQLite-backed search. The TUI, web interface, and import command share the same application runtime and services.

See [the architecture guide](docs/ARCHITECTURE.md) for the target system design, [ADR-001](docs/decisions/001-modular-go-application.md) for the decision behind it, and [AGENTS.md](AGENTS.md) for contribution guidance.

## Privacy

AgentSession is local-first and read-only with respect to coding-agent session files and inspected repositories. It does not run agents or upload source code.

## License

Apache-2.0. See [LICENSE](LICENSE).
