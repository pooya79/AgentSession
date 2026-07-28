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
