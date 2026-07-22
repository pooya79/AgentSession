# AgentSession

AgentSession is a lightweight, local-first explorer for coding-agent sessions. It turns local records from coding agents into repository-aware evidence about messages, commands, file changes, tests, failures, and outcomes.

The project provides a first local web workflow for discovering, importing,
and browsing normalized session evidence. Read-only source discovery,
authoritative import storage, verified bounded import orchestration, and
adapters for Codex CLI, Claude Code, and OpenCode are implemented; search and
analysis are still under development.

## Supported session sources

- Codex CLI rollout JSONL files, including legacy event-based history and
  current ordinal-bearing history. Imports stream complete records, retain raw
  bytes and unknown variants, defer incomplete trailing records, and verify
  checkpoints before append or reconciliation.
- Claude Code JSONL session files. Imports preserve mixed message and tool
  content in source order, retain snapshots, sidechains, malformed records,
  and unknown variants, and use the same verified append and reconciliation
  guarantees.
- OpenCode SQLite databases using the current `session`/`message`/`part`
  schema. Each database is read through one query-only snapshot and expands
  into one stable logical source per OpenCode session. Complete typed rows,
  including unknown columns and exact TEXT/BLOB values, are retained.

The application composes discovery and all adapters behind one shared runtime.
Canonical imports are stored locally in SQLite and can be started explicitly
from the command line.

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

Start the local web interface:

```bash
go run ./cmd/agentsession web
```

The web server listens on `127.0.0.1:8080` by default. Use `--addr` to select another address:

```bash
go run ./cmd/agentsession web --addr 127.0.0.1:9000
```

The source and session panels load from the shared application services. Select
one or more discovered sources to import them, then open an imported session to
browse its paginated event summaries. Normalized payloads are fetched only when
an event detail is opened; retained raw records are not exposed by the web UI.

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
```

## Planned architecture

AgentSession is designed as a modular Go monolith. Source-specific adapters stream records into a canonical event model, followed by deterministic analysis and SQLite-backed search. The TUI, web interface, and import command share the same application runtime and services.

See [the architecture guide](docs/ARCHITECTURE.md) for the target system design, [ADR-001](docs/decisions/001-modular-go-application.md) for the decision behind it, and [AGENTS.md](AGENTS.md) for contribution guidance.

## Privacy

AgentSession is local-first and read-only with respect to coding-agent session files and inspected repositories. It does not run agents or upload source code.

## License

Apache-2.0. See [LICENSE](LICENSE).
