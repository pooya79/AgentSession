# AgentSession

AgentSession is a lightweight, local-first explorer for coding-agent sessions.
It turns local session records into searchable timelines of messages, commands,
file changes, tests, errors, and other recorded activity.

> **Status:** AgentSession is an active pre-v0.1 project. The core import,
> browsing, and search workflows are usable, but source coverage and the user
> experience are still evolving.

## Features

- Imports sessions from Codex CLI, Claude Code, and OpenCode
- Provides both a terminal UI and a local web UI
- Browses session timelines, event details, and import diagnostics
- Searches normalized session evidence with text and structured filters
- Updates a local SQLite index incrementally
- Works offline without accounts, API keys, telemetry, or cloud services
- Reads source sessions without modifying them or running recorded commands

## Supported sources

AgentSession discovers sessions from the standard locations used by:

- [Codex CLI](https://github.com/openai/codex)
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code)
- [OpenCode](https://opencode.ai/)

You can also provide session files, directories, or OpenCode databases
explicitly.

## Getting started

AgentSession currently runs from source and requires Go 1.26 or newer.

```bash
git clone https://github.com/pooya79/AgentSession.git
cd AgentSession
go run ./cmd/agentsession
```

The default command starts the terminal interface and discovers supported
sessions automatically.

To start the web interface:

```bash
go run ./cmd/agentsession web
```

It listens on `127.0.0.1:8080` by default. Use `--addr` to choose a different
local address.

To import sessions without opening an interface:

```bash
go run ./cmd/agentsession import
```

Explicit source paths supplement automatic discovery. Each flag is repeatable:

```bash
go run ./cmd/agentsession import \
  --codex ./path/to/codex-sessions \
  --claude ./path/to/claude-sessions \
  --opencode ./path/to/opencode.db
```

Run `go run ./cmd/agentsession --help` for all commands and options.

## Repeated command failures

Session detail shows an explained rule assessment in the web interface. In the
terminal timeline, press `f` to inspect it, `[` / `]` to select supporting
evidence, and Enter to open that event's timeline context. Press `x` from the
terminal timeline for projection readiness and retry/rebuild controls; the web
session page exposes the same controls. Refresh after a rebuild completes.

The initial rule uses supported Codex shell calls and explicit
`exec_command_begin` / `exec_command_end` records linked by call ID. Supported
`exec_command` yields and `write_stdin` polls remain one invocation, including
when completion arrives in a later import. Shell/function calls and historical
local-shell actions establish starts; only explicit process exits or recognized
tool result envelopes establish completion. Argument arrays preserve boundaries
with shell quoting. A three-argument POSIX shell wrapper matches a script-text
request only when its call ID and exact script match that recorded request.
Standalone wrappers retain their shell and flags. No command is executed. The rule requires
two distinct failed invocations with the
same recorded command text (trimmed only at the ends) and an established matching
working directory, without intervening success or relevant uncertainty. Other
commands do not interrupt a sequence. Later success shows recovery while keeping
the historical assessment. Generic tool success and assistant claims are not
process exit evidence. Yielded results and responses without exits remain
inspectable context summaries alongside the pending start. Unsupported custom
tools, arbitrary output prose, ambiguous process reuse, and unlinked polls
remain uncertain. Directories come only from recorded invocation or preceding
session/turn context, without assuming a repository or module root. See the
[Codex evidence inventory](internal/adapter/codex/testdata/README.md) for exact
forms and limits.

Assessments report `triggered`, `not_triggered`, `not_applicable`, or
`insufficient_evidence` for the currently imported record in source order.
Pending, running, failed, and stale projections are unavailable conclusions,
not a negative assessment. Unknown or malformed records and commands with
missing/contradictory evidence limit affected sequences; they do not discard
independently established repetition. Overlapping failed invocations can establish
repetition; a successful invocation counts as recovery only when it starts after
that sequence's repeated failures completed. Sequences retain independent recovery
evidence, including when invocations overlap. No severity grade or session outcome is assigned. The
verification rules described in ADR-021 are not implemented yet.

All invocations are evaluated. At most 12 representative event links are shown,
with totals and an omission notice. Command text over 16 KiB, directories over
4 KiB, and invocation IDs over 512 bytes are retained in canonical evidence but
excluded from identity matching and reported as uncertainty. Findings contain
counts and fixed explanations, without copying commands or output prose into
metadata. Rebuilds use stored evidence and work after source files disappear.

## Development

Common tasks are available through the Makefile:

```bash
make run    # run the terminal interface
make web    # run the web interface
make build  # build bin/agentsession
make check  # verify generated code, vet, and run tests
```

## Documentation

- [Documentation guide](docs/README.md)
- [Product contract](docs/PRODUCT.md)
- [Current implementation](docs/CURRENT_STATE.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Architecture decisions](docs/decisions/README.md)
- [Contribution rules](AGENTS.md)

## Privacy

AgentSession keeps its index and settings separate from source sessions and
repositories. It does not upload source code, run agents, or modify inspected
session data.

## License

AgentSession is licensed under the [Apache License 2.0](LICENSE).
