# Codex rollout fixture inventory

All values are sanitized structural placeholders. “Observed” rows follow the
wire definitions shipped by Codex CLI `0.145.0`; they are not copied from a
user session. Synthetic malformed and future rows are labeled explicitly.
The retained JSONL bytes are the authoritative evidence in every test.

| Fixture | CLI / format | Top-level type | Nested `payload.type` | Provenance | Expected classification |
| --- | --- | --- | --- | --- | --- |
| `explicit_commands.jsonl` | explicit event messages | `session_meta`, `event_msg` | `exec_command_begin`, `exec_command_end` | sanitized wire-shaped starts/results; synthetic duplicate and missing-field cases | linked Command events, raw retention, source order independent of timestamps |
| `streamed_commands.jsonl` | 0.145.0-shaped | `session_meta`, `response_item` | function calls/outputs | sanitized unified-exec envelopes; synthetic duplicate/retry sequence | one invocation through yield/poll/final, repetition, recovery, pending attempt |
| `mirrored_commands.jsonl` | 0.145.0-shaped | metadata, response items, command events | exec/poll and unified-exec interaction | sanitized matching request/event/result projections | shell-wrapper identity, one invocation across mirrored and polled results |
| `command_forms.jsonl` | historical and 0.145.0-shaped | metadata, response items, command events | shell, shell_command, local-shell and custom tools | sanitized cross-generation examples, not one observed session | recorded context, linked exits, unsupported custom grammar stays generic |
| `uncertain_commands.jsonl` | 0.145.0-shaped and synthetic invalid rows | metadata, response items, event messages | exec/poll plus malformed/future kinds | sanitized delayed context, cancellation, missing exits, contradictory exits and unlinked poll | no invented context or success; diagnostics and Unknown retained |
| `legacy.jsonl` | 0.42.0 / legacy | `session_meta`, `event_msg`, `response_item` | message and function call variants | sanitized baseline | messages, tool call/result, metadata |
| `ordinal.jsonl` | 0.133.0 / ordinal | `session_meta`, `event_msg`, `response_item` | message, lifecycle, token count | sanitized baseline | durable messages, usage, projections retained as metadata |
| `current_0.145.0.jsonl` | 0.145.0 / ordinal | `session_meta`, `response_item`, `event_msg` | reasoning, function/custom/local-shell, web search, MCP, dynamic tool, collaboration, plan/lifecycle | observed 0.145.0 structure | Summary, ToolCall, ToolResult, Patch, metadata |
| `delayed_metadata_0.145.0.jsonl` | 0.145.0 / ordinal | malformed, `response_item`, `session_meta` | message | synthetic ordering around observed shapes | malformed diagnostic, durable message, delayed metadata |
| `malformed_future_0.145.0.jsonl` | 0.145.0 / ordinal | `session_meta`, `response_item`, `event_msg`, future top-level | malformed known and future nested variants | explicitly synthetic | malformed diagnostics or reasoned Unknown |
| `malformed_unknown.jsonl` | unknown / legacy | malformed and future top-level | future nested | synthetic baseline | malformed diagnostic and top-level Unknown |

Opaque `reasoning.encrypted_content`, turn/task lifecycle, streaming deltas,
startup progress, and other transient projections are retained as metadata and
are never indexed. A valid future top-level kind uses
`unsupported_record_kind`; a valid future `response_item` or `event_msg`
variant uses `unsupported_nested_variant`. Known shapes with missing
discriminants or invalid required fields emit categorized diagnostics and do
not become Unknown events.

## Command evidence coverage

`go_verification.jsonl` contains sanitized explicit command starts and results
for Go test, vet, and build, including a duplicate failure result, recovery,
an unknown completed exit settled by later verification,
reopening, overlapping invocations, timestamp/source-order disagreement, and
assistant assurance. Shared service contracts consume this fixture through the
real importer and both interfaces. Incremental prefix tests cover all four
assessment states; rebuild tests remove the source before rebuilding.

The shapes above follow these tagged upstream definitions rather than developer
session data:

- [Codex 0.145.0 unified exec arguments](https://github.com/openai/codex/blob/rust-v0.145.0/codex-rs/core/src/tools/handlers/unified_exec.rs),
  [poll arguments](https://github.com/openai/codex/blob/rust-v0.145.0/codex-rs/core/src/tools/handlers/unified_exec/write_stdin.rs),
  and [result formatting](https://github.com/openai/codex/blob/rust-v0.145.0/codex-rs/core/src/tools/context.rs).
- [Codex 0.145.0 shell result formatting](https://github.com/openai/codex/blob/rust-v0.145.0/codex-rs/core/src/tools/mod.rs)
  and [explicit command events](https://github.com/openai/codex/blob/rust-v0.145.0/codex-rs/protocol/src/protocol.rs).
- [Codex 0.42.0 shell/local-shell arguments](https://github.com/openai/codex/blob/rust-v0.42.0/codex-rs/protocol/src/models.rs).
  Historical local-shell actions remain supported even though current Codex
  protocol definitions no longer include that response variant.

Supported starts are function `exec_command` with JSON `cmd`, function
`shell_command` with JSON string `command`, historical function `shell` or
`container.exec` with JSON argument-array `command`, and `local_shell_call`
with `action.type=exec` and argument-array `command`. Local-shell `status` and
tool success/error flags do not establish process exits. Custom tools have no
established command argument grammar in these sources; they retain generic
tool/patch evidence. A custom output cannot independently establish a command.

Explicit command events preserve their call ID and all result records. An
understood `sh`, `bash`, or `zsh` wrapper with exactly `-c`/`-lc` and one script
normalizes to that script only when the same call ID already links it to an
identical script-text request. Standalone wrappers and other arrays use
deterministic shell quoting.
This lets request and execution projections agree without treating arbitrary
different commands as equivalent. Contradictory linked identities/results are
left for the source-neutral analysis contract to mark unknown. Declined or
unrecognized completion statuses cannot turn placeholder exit values into
process outcomes.

For unified exec, parse only the complete formatter header before `Output:`:
optional `Chunk ID`, `Wall time`, exactly one process exit or running-session ID,
and optional original token count. For shell results, require `Exit code`,
`Wall time`, optional total line count, and `Output:`. String and text-block
response outputs use the same envelope. Matching phrases inside the output
body, arbitrary JSON results, missing/malformed headers, code-mode cells or
JavaScript wrappers, and future tool names do not establish exits. These forms
remain unsupported or uncertain rather than heuristically parsed.

An explicit process ID or yielded running-session ID associates subsequent
`write_stdin` calls and `unified_exec_interaction` events with the original
invocation. Reused or conflicting process IDs cannot establish a new poll
relationship. Poll requests, yields, and exit-less responses appear as context
summaries rather than extra starts or completed commands. Their raw records
and outputs remain retained. A final result is a distinct event each time,
so duplicate results do not become retries and contradictory finals survive.

Directories use recorded `workdir`, local-shell `working_directory`, or the
preceding session/turn `cwd`. Relative overrides require an established absolute
POSIX context; context arriving later is not retroactive. Remote environment
selection requires an explicitly recorded absolute directory. Windows paths
without an understood directory context remain uncertain. No filesystem,
live explorer directory, repository root, or module root is consulted.

Correlation retains at most 4,096 call links and 4,096 process links with call
IDs no longer than 512 bytes. Beyond those bounds, generic evidence remains an
explicit analysis gap. Oversized recorded context directories are left unknown;
original records remain authoritative. Incremental verification streams the
verified prefix to reconstruct this bounded adapter state, without growing the
v1 cursor or reading the complete log into memory. Reconciliation resets it.
Tests resume after each streamed fixture record, defer partial tails, preserve
large output/raw bytes, check duplicate IDs/order, and cover truncation,
replacement, projection reassessment, and rebuilding after source removal.

## Confirmed change evidence

`changes.jsonl` models the structured Codex protocol
[`PatchApplyEndEvent` and `FileChange`](https://github.com/openai/codex/blob/main/codex-rs/protocol/src/protocol.rs):
`call_id`, `success`, `status`, and the affected-path map with `add`, `delete`,
or `update` variants (`content`, `unified_diff`, optional `move_path`). Only
successful completed results with an understood map confirm effects. Empty maps
or explicitly empty update diffs without moves are no-ops. Nonempty diffs without
understood edit lines remain unknown; hunk content resembling file headers still
counts as edit evidence. Missing maps/status, malformed entries,
future variants and contradictory statuses stay uncertain. Failed applications
never confirm mutation; recorded intended paths can leave partial effects unknown.
Tests preserve both move paths, raw bytes, source order, large paths, stable IDs,
and request context across incremental resume. Patch requests, begin/update
events, generic responses and turn diffs are retained without claiming mutation.
Generic responses linked to a patch request remain patch context; their prose
cannot supply affected paths or confirmation. Invocation directories are bounded
and reconstructed from the verified prefix. No filesystem inspection occurs.
