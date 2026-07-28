# Codex rollout fixture inventory

All values are sanitized structural placeholders. “Observed” rows follow the
wire definitions shipped by Codex CLI `0.145.0`; they are not copied from a
user session. Synthetic malformed and future rows are labeled explicitly.
The retained JSONL bytes are the authoritative evidence in every test.

| Fixture | CLI / format | Top-level type | Nested `payload.type` | Provenance | Expected classification |
| --- | --- | --- | --- | --- | --- |
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
