# Claude Code adapter fixtures

These JSONL fixtures are sanitized structural examples. They contain no paths,
prompts, command output, identifiers, or other content copied from a private
session.

## Inventory

| File | Provenance | Claude version(s) | Shapes covered |
| --- | --- | --- | --- |
| `main.jsonl` | Existing sanitized project fixture | 2.1.3 | messages, client tool call/result, usage, summary, metadata |
| `malformed_unknown_sidechain.jsonl` | Existing sanitized project fixture | 3.0.0 synthetic | malformed JSON, unsupported record, sidechain |
| `observed.jsonl` | Sanitized from locally observed structure | 2.1.160–2.1.179 | sidechain messages; system summaries/errors/messages/metadata; queue operations; progress envelopes |
| `official_blocks.jsonl` | API-documentation-derived; no persisted local example | 2.1.179 fixture label | server tool calls/results, unsupported thinking and multimodal blocks |

The `progress` envelope classifications also reflect public Claude Code
examples: `mcp_progress` was published with 2.1.25-era records and
`hook_progress` with 2.1.71–2.1.81-era records. Progress is classified as
transient metadata because the durable message/tool records carry the timeline
evidence.

The server-tool and `redacted_thinking` rows are deliberately labeled
API-derived. Anthropic documents `redacted_thinking` as opaque encrypted data
and server calls/results as paired `server_tool_use` and `*_tool_result`
blocks:

- https://platform.claude.com/docs/en/build-with-claude/extended-thinking
- https://platform.claude.com/docs/en/agents-and-tools/tool-use/server-tools

## Expected classification matrix

| Top-level type | Nested discriminator | Expected canonical result |
| --- | --- | --- |
| `user`, `assistant` | text | `Message` (sidechain provenance appears only in the summary) |
| `assistant` | `tool_use`, `server_tool_use` | `ToolCall` |
| `user` | `tool_result`, documented `*_tool_result` | `ToolResult` |
| `assistant` | valid usage counters | `Usage` |
| `summary` | — | `Summary` |
| `system` | `away_summary`, `compact_boundary` | `Summary` |
| `system` | `api_error` | `Error` |
| `system` | `local_command`, `informational` | system `Message` |
| `system` | `turn_duration`, `status`, `stop_hook_summary`, `task_notification` | metadata (zero events) |
| `progress` | `hook_progress`, `mcp_progress`, `agent_progress`, `bash_progress`, `powershell_progress` | metadata (zero events) |
| `queue-operation` | `enqueue`, `dequeue`, `remove` | metadata (zero events) |
| status, attachment, and context records | known variant | metadata (zero events) |
| message block | `thinking`, `redacted_thinking`, image/document/search/tool-reference or future valid type | `Unknown(unsupported_nested_variant)` |
| known envelope | future valid nested discriminator | `Unknown(unsupported_nested_variant)` |
| future top-level type | — | `Unknown(unsupported_record_kind)` |

Malformed known shapes produce record diagnostics instead of Unknown events.
Complete raw JSONL bytes remain the authoritative evidence.

## Ordinary command and change evidence

`ordinary_tools.jsonl` is a sanitized structural fixture for ordinary client
`tool_use` / `tool_result` pairs, with invented IDs, Go files, command text and
output. It combines reported persisted structures; its version is a fixture
label, not a claim that every variant was captured in that release. Provenance:

- Ordinary Bash request, record `cwd`, and `stdout`/`stderr`/`interrupted`/`isImage`
  result envelope: [upstream reproduction](https://github.com/anthropics/claude-code/issues/49625)
  (reported 2.1.112).
- Edit `filePath`/`structuredPatch`/`originalFile` and string-valued error
  alternatives: [upstream reproduction](https://github.com/anthropics/claude-code/issues/40858)
  (reported 2.1.86–2.1.87). The sanitized before/after strings and hunks form a
  self-contained worked example, rather than copying a user's source file.
- Write `type: create`, `filePath`, `content`, `structuredPatch`, and null
  `originalFile`: [upstream reproduction](https://github.com/anthropics/claude-code/issues/55162)
  (reported 2.1.63). Update, no-op and malformed cases are controlled mutations
  of the structural fixture in adapter tests, not independent capture claims.

These forms do not borrow hosted `bash_code_execution_tool_result` or
`text_editor_code_execution_tool_result` semantics. Hosted/custom tools remain
uninterpreted evidence for findings.

Ordinary Bash requests establish command starts by tool-use ID. Only understood
foreground result envelopes establish completion; interruption, background
results, missing metadata and ambiguous multi-result envelopes stay generic.
The representative Bash envelopes contain **no numeric process exit**. Error
flags, `FAIL`, `PASS`, and `Exit code 1` text cannot establish failure or recovery.
Accordingly, repeated-failure and unresolved-verification assessments of these
fixtures remain insufficient evidence, including after apparent successful
reruns. No unobserved numeric exit field is invented or parsed.

Edit/Write requests establish intent. A matching ordinary result can establish
confirmation only through structured effect evidence. Paths and mutation fields
must match the linked request. Edit/update confirmation validates the complete
structured hunks against before/after contents, including line counts and order;
empty consistent patches are no-ops. Terminal-newline discrepancies without
represented newline markers remain unknown. Unrecognized structured effect fields,
mixed hosted/client identities, mismatched named results and null error flags
cannot establish confirmation. Write creation requires explicit `create`,
null original content and a valid patch array (empty is permitted for creation).
Failed, prose-only, user-modified, malformed, incomplete, mismatched and
contradictory results remain uncertain. Linked duplicate effects count once in
shared analysis. An unknown result is never proof that no partial edit occurred.

Directories come from the invocation/result record's explicit absolute POSIX
`cwd`, without carrying a directory across records or inspecting repositories.
Missing or ambiguous bases remain unknown; absolute affected paths need no base.
Relative paths additionally require established identical request/result bases.
Request/result paths must match literally; relative-to-absolute aliases and
unfamiliar variants are intentionally uncertain. Every record stays raw, ordered
and inspectable, including unknown forms and large structured payloads.

The adapter checkpoints the most recent 256 tool-use identities (IDs up to
512 bytes), with hashes of request/effect fields rather than file contents.
This bounds linkage memory and preserves pending calls across incremental imports.
Results for evicted/oversized identities stay generic evidence and therefore
limit analysis instead of silently establishing a conclusion. Reconciliation
clears linkage and derives it from the replacement stream. Findings bounds,
rule versions, privacy, stale/unavailable behavior and offline rebuilding remain
those of the shared services; no source-specific rules or outcome grades apply.
