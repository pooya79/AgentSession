# AgentSession

AgentSession is a lightweight, local-first explorer for coding-agent sessions. It turns local records from coding agents into repository-aware evidence about messages, commands, file changes, tests, failures, and outcomes.

The project is currently an early runnable scaffold. Read-only session source discovery, authoritative import storage, and verified, bounded import orchestration are implemented; concrete source adapters, search, and analysis are still under development.

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

AgentSession is designed as a modular Go monolith. Source-specific adapters will stream records into a canonical event model, followed by deterministic analysis and SQLite-backed search. The TUI and web interface will share the same application services.

See [the architecture guide](docs/ARCHITECTURE.md) for the target system design, [ADR-001](docs/decisions/001-modular-go-application.md) for the decision behind it, and [AGENTS.md](AGENTS.md) for contribution guidance.

## Privacy

AgentSession is local-first and read-only with respect to coding-agent session files and inspected repositories. It does not run agents or upload source code.

## License

Apache-2.0. See [LICENSE](LICENSE).
