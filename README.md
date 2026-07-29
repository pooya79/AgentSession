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
