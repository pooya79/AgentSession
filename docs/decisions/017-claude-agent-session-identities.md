# ADR-017: Give Claude agent logs distinct canonical session identities

## Status

Accepted

## Context

Claude agent and sidechain JSONL files retain the parent `sessionId` while
also carrying an `agentId`. Discovery treats each file as an independent
read-only source, and the import store requires one canonical session to have
one owning source. Using only the parent `sessionId` therefore makes multiple
agent files collide with the parent or with each other.

Some Claude task-result JSONL files carry `agentId` without a parent
`sessionId`. Their `started` and `result` envelopes are valid Claude-owned
records but are otherwise too generic for adapter selection.

## Decision

The Claude adapter derives a stable opaque canonical session ID from the
parent `sessionId` and `agentId` when both are present. Ordinary Claude
sessions keep their existing canonical session ID. Agent-owned files without
a parent session continue to use the existing source-derived fallback ID.

An `agentId` in the bounded probe window is sufficient to identify a source
as Claude. Unsupported agent-log record variants remain retained as raw
records and normalize to canonical `Unknown` events.

## Consequences

- Parent sessions and agent sessions no longer compete for ownership of one
  canonical session ID.
- Repeated imports remain stable and idempotent without depending on paths or
  filename conventions.
- Ordinary Claude session identities are unchanged.
- Source-specific agent identifiers do not escape into the shared event
  model, and original records remain read-only.
