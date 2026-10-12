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

In a session timeline, arrows or `j`/`k` scroll text, Page Up/Page Down scroll
one viewport, and `[`/`]` jump between events. The `Target` footer identifies
the event acted on by Enter (show/hide technical details) and `u` (inspect
bounded, redacted Unknown evidence). Messages always remain open. Headings,
lists and code use restrained terminal Markdown; indentation is preserved and
long code/output lines wrap by default. Links remain text and are never fetched.
Press `d` for normalized inspection, `w` to toggle wrapping there, and Left/Right
to pan unwrapped lines. The column indicator and fixed event header preserve
inspection context; Esc restores the earlier timeline reading position. Press `?`
for help, `/` for search, and Esc to return to your previous reading context.
Returning from search results or assessment evidence restores the earlier
timeline, selection, disclosure, and reading position. Resizing and refreshing
preserve the position within available content; refresh explains changed or
removed anchor evidence. Failed reads retain usable evidence and can be retried
with `r`; interrupted continuation can resume when you scroll near the end.
Long histories retain a working set of up to 150 payloads/cards, with 2 MiB of
accounted payload content and 4 MiB of rendered content. Earlier evidence reloads
from the local index as you scroll or jump back; disclosure and reading anchors
remain keyed by event identity. Lightweight navigation metadata still grows with
visited history. An oversized event and one boundary neighbor can exceed the byte
budgets to keep all evidence readable. Refresh rereads the current neighborhood.
See the [measured limits and reproduction](docs/benchmarks/tui-timeline-bounded.md).

Search uses Left/Right to move the query cursor, Home/End (or Ctrl-A/Ctrl-E) to
move to its beginning/end, and Backspace/Delete to remove text before/at the
cursor. Typing and bracketed paste insert at the cursor; shortcut characters
such as `q`, `/`, `?`, `[` and `]` remain query text. Pasted line breaks and tabs
become spaces. Enter submits through the shared search service; Esc leaves
editing, then returns to the previous screen. `/` edits again, `n`/`p` page
results, and `r` reruns the query from the first page. Edited queries require a
new search before old results can be opened or paged. Enter on a result opens
its session timeline; Esc restores the query, selected result, and result page.
Search cancellation and failures have explicit retry guidance; superseded
responses cannot replace the current query's results. Refresh retains earlier
results with a previous-results label while freshness is unconfirmed, preserves
the selected session when still present, and explains a removed selection.
See the [search terminal captures](docs/design/tui-91/README.md).

At 110 columns or wider, sessions show a list beside the selected session's
recorded preview, evidence coverage and projection readiness. Opening a session
gives its timeline the full main area. At 80×24, one main view provides the full
primary workflow. Below 80 columns or 24 rows, help is compact; `?` exposes all
controls. Navigation remains usable at 40×12; smaller terminals show a resize
message. Focus, `+`/`-` disclosure, recorded failures and unavailable results
have text labels in dark, light and color-free output (`NO_COLOR=1`). Mouse
capture stays disabled. See the [production terminal captures](docs/design/tui-89/README.md)
for the comparison with the accepted design.

Supporting screens use the same focus footer and Esc return behavior. In rule
assessments, `[`/`]` reveal the selected supporting reference and Enter opens its
timeline context; Esc restores the assessment and then the original reading
position. In projections, arrows or `j`/`k` select the action scope, while
Page Up/Page Down read long status and diagnostic text without changing that
scope. `t` retries implemented pending/failed projections, `b` rebuilds the
selected implemented projection, and `a` offers a `y`/`n`/Esc confirmation only
when every registered projection can be built. Leaving a panel stops observation
without canceling application-owned work. Search returns to the saved supporting
screen and resumes observation where needed.

Help owns its controls while open: arrows, Page Up/Page Down and `g`/`G` scroll;
Esc or `?` returns to the reader. Detail and assessment reads show loading,
cancellation and retry separately from missing or partial evidence. Command/tool
details say when no output was recorded. Unknown raw inspection requires `u`,
remains bounded and redacted by the shared service, and clears on navigation;
it is still available when the normalized payload is unavailable. See the
[supporting-screen terminal captures](docs/design/tui-92/README.md).

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
evidence, including when invocations overlap. No severity grade or session outcome is assigned.
Missing verification after confirmed source changes is assessed separately below.

All invocations are evaluated. At most 12 representative event links are shown,
with totals and an omission notice. Command text over 16 KiB, directories over
4 KiB, and invocation IDs over 512 bytes are retained in canonical evidence but
excluded from identity matching and reported as uncertainty. Findings contain
counts and fixed explanations, without copying commands or output prose into
metadata. Rebuilds use stored evidence and work after source files disappear.

## Unresolved Go verification

Both session-detail interfaces also show `unresolved_failed_verification`.
It uses the same explicit command evidence and projection controls described
above. Recognized standalone forms require a recorded working directory and
at most one local package/directory target:

| Command | Optional flags | Kind |
| --- | --- | --- |
| `go test` | `-v`, `-json`, `-race`, `-count=1` | Test |
| `go vet` | `-json` | Static check |
| `go build` | `-v`, `-trimpath` | Build |

Targets may be omitted, `.`, `./...`, a literal `./path`, or `./path/...`.
Literal path components use ASCII letters, digits, `_`, `-`, and `.`, excluding
empty, `.`/`..`, and wildcard components; `.go` file targets are unsupported.
Flags precede targets for vet/build; supported test flags may also follow the
target. Unknown flags, multiple targets, import paths, compile-only/list/filter
forms, wrappers, chains, substitutions, environment assignments, and other
languages remain uncertain. These forms can still establish language-independent
repeated command failures when explicit identity and results suffice. Recognition
never executes a command or inspects a live repository. A recognized target
describes declared scope, not actual file participation, coverage, or correctness.

A failed verification is resolved only by an explicitly successful invocation
of the exact same recorded command (ignoring surrounding whitespace) in the same
established directory, starting after that failure completed in source order.
Timestamps do not override source order. A different check or directory,
parallel success, pending rerun, or assistant assurance does not prove recovery.
A later failure reopens the condition. Tests, static checks, and builds retain
separate counts; build success does not establish test success.

The assessment reports total recognized invocations, failed invocations,
recovered failures, conclusively unresolved failures, and uncertainty, with at
most 12 representative event links and an omission notice. One successful rerun
may resolve multiple earlier failures. A completed invocation with an unknown
exit result cannot leave an unresolved failure after a later same-identity
success starts, provided its identity and completion are established. Its exit
stays unknown; the explanation reports these settled unknown results separately
from known failures and recoveries. Missing starts/completions, conflicting
results, and compatible gaps after a failure can leave recovery uncertain.
Gaps before completion and uncertain commands of other known identities cannot
hide its recovery. Any conclusively unresolved failure triggers the assessment
ahead of remaining uncertainty; otherwise relevant uncertainty yields
`insufficient_evidence`, assessed checks without an unresolved failure yield
`not_triggered`, and an interpreted record with no applicable commands yields
`not_applicable`. New imports reassess the record; rebuilds use stored evidence
even after sources disappear.

## Missing verification after changes

Both session-detail interfaces expose `missing_verification` alongside the
other two assessments. It assesses confirmed Go source and module configuration changes against
the recorded declared targets of the Go checks listed above. A completed relevant
check counts even when it fails: missing verification can be `not_triggered`
while unresolved failed verification is `triggered`. The check must start after
the edit in source order; an earlier or still-running check does not verify it.
Unknown or contradictory results leave uncertainty.

Initial change confirmation uses Codex `patch_apply_end` records with explicit
`success: true`, `status: completed`, and understood structured `changes`.
Patch requests, begin/update events, tool-success prose, failed applications,
and turn diffs do not confirm mutation. Empty change maps and explicitly empty
updates are no-ops. Failed multi-file applications can have uncertain partial
effects. Repeated completion records count once; contradictory linked results
leave confirmation uncertain. Original request/result records remain retained.

Ordinary `.go` files use recorded absolute POSIX paths or a preceding established
patch working directory. A local target covers its directory; a recursive target
also covers descendants. A build excludes `_test.go` changes. This establishes
declared target relevance, without proving actual file participation,
correctness, test coverage, or dependency coverage. No live repository is inspected
and no recorded command is executed.

Clearly documentation-only paths (`.md`, `.rst`, `.adoc`, `LICENSE`, `COPYING`)
are exempt. Confirmed moves assess the origin and destination independently,
including each endpoint's file role and check-kind exclusions. Checking only the
destination does not verify the origin. Unknown endpoints remain uncertain;
independent known missing scopes can still trigger the assessment.

Confirmed `go.mod` creation/update records establish a module directory for
scope analysis. Changes to `go.mod` and `go.sum` require a recursive declared
target rooted exactly there, such as `go test ./...` from that directory or
`go vet ./module/...` from its parent. An isolated package target or recursion
from an ancestor does not establish whole-module verification. A `go.sum` path
or working directory alone cannot establish a module root: the record must also
contain a confirmed `go.mod` creation/update at or before the configuration
change. Module contents and dependencies are not inferred from these markers.

Recorded nested module relationships, module deletion/relocation, and overlapping
workspace configuration (`go.work`, `go.work.sum`) leave affected configuration
scope uncertain. Later structural evidence can disclose ambiguity but cannot
retroactively establish an earlier module root. Recursive source targets crossing
a recorded module boundary also remain uncertain. The current canonical evidence
does not establish workspace membership, so workspace configuration remains
uncertain even after a successful recursive check. Unrelated known scopes remain
independently assessable.

Other languages, unfamiliar roles, traversal/Windows paths, unavailable bases,
and Go files under `vendor`, `testdata`, or dot/underscore-prefixed components
remain uncertain. Module and workspace boundaries are not inferred from working
directories.
Paths/bases over 4 KiB and change IDs over 512 bytes remain canonical evidence
but cannot establish analysis identity. All affected paths are assessed, with
counts (including separate move origin/destination totals), up to 12 representative
event links, and an omission notice. Omitted-reference counts represent evaluated
reference occurrences beyond the sample, including repeated links once the bound
is reached. Compatible
unknown commands or interpretation gaps can prevent proof of omission; independent
conclusive missing changes still trigger the assessment. Imports reassess the
record, and offline rebuilds use stored canonical evidence.

## OpenCode findings evidence

Both interfaces use the same persisted assessments for supported OpenCode
evidence. The adapter selects one storage generation per session; alternative
histories are never combined.

- Durable `bash` tool calls preserve invocation IDs, command text, and explicit
  absolute `workdir` values. Linked successful tool results with an integer
  `structured.exit` establish process completion, including nonzero exits.
  A timeout, tool error, missing exit, or output assurance does not.
- Modern sequenced and durable `write`, `edit`, and `apply_patch` results can
  confirm ordinary changes through their structured application results.
  Requested patches, patch previews, generic tool success, and `outputPaths`
  do not confirm application. Absolute affected paths are required; relative
  location resources cannot establish scope without a recorded base.
- Completed sequenced tool snapshots retain explicit process results but lack
  an ordered invocation start. They cannot establish sequential recovery or
  verify a preceding change by inventing a start from array order or timestamps.
- Legacy tool records retain command and patch intent. Legacy process results
  and mutation confirmation are not currently recognized. Shell-only records
  lack the exit/directory evidence needed for definitive assessments.

Incomplete durable sequences expose positioned interpretation diagnostics,
preserving uncertainty about missing recovery and verification. Durable tool attribution
keeps the most recent 256 invocation IDs/tool names (at most 512 bytes each),
reconstructing them from the selected snapshot prefix on resume. Evicted,
conflicting, or oversized attribution remains uncertain. Existing findings
bounds, Go grammar, event navigation, and offline rebuilding apply unchanged.
See the [OpenCode evidence inventory](internal/adapter/opencode/testdata/README.md)
for synthetic fixture provenance and exact supported shapes.

## Claude findings evidence

Both interfaces also assess supported ordinary Claude Bash/Edit/Write evidence.
Structured matching Edit/Write results can confirm changes and establish missing
verification. Requests, failed/partial edits and success prose cannot confirm
changes. Ordinary Bash fixtures establish starts and understood foreground
completion, but contain no numeric exit status: error flags and apparent failed
or successful rerun text leave repeated failures, unresolved verification, and
verification after a change uncertain. A later confirmed Go change without any
relevant recorded check can still establish missing verification independently.
See the [Claude evidence inventory](internal/adapter/claude/testdata/README.md)
for exact structures, linkage bounds, fixture provenance, and limitations.

## Development

Common tasks are available through the Makefile:

```bash
make run    # run the terminal interface
make web    # run the web interface
make build  # build bin/agentsession
make check  # verify generated code, vet, and run tests
```

The [combined TUI acceptance record](docs/design/tui-93/README.md) includes an
isolated live review command, terminal captures, and the final visual-review
status. See the [final benchmark report](docs/benchmarks/tui-timeline-final.md)
for the 10,000-event workload, measured responsiveness, cache budgets and limits.

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
