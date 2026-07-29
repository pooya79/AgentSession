# ADR-016: Select one OpenCode storage generation per session

## Status

Accepted

## Context

OpenCode SQLite containers can contain legacy message/part rows, sequenced
session messages, durable events, or more than one of these during a producer
transition. Merging them would duplicate evidence and could attach conflicting
meanings to one timeline. Durable sequence markers also have two producer
conventions: the last persisted sequence or the next sequence to assign.

Fixtures for these formats were derived from supplied DDL and pinned OpenCode
JSON schema definitions. They are not observed production records.

## Decision

The adapter selects exactly one generation for each session inside its
query-only SQLite snapshot:

1. a complete, contiguous, duplicate-free durable sequence with an unambiguous
   marker convention;
2. a populated `session_message` generation;
3. populated legacy `message` or `part` evidence.

An incomplete durable generation falls back in that order. If it is the only
populated generation, its available rows are imported in `seq, id` order and
the session receives `opencode.generation.durable.incomplete`. A genuinely
empty session uses the newest structurally available generation. Rows from
different generations are never combined.

The selected format and durable marker convention are part of cursor state.
Normalization version 2, cursor version 2, fingerprint version 2, or a selected
generation change classifies an existing logical source as replaced. Existing
staged reconciliation then atomically replaces the prior AgentSession-owned
canonical generation and projections.

Only selected session and timeline rows are retained. `session_input` and
`session_context_epoch` are detection-only and are neither retained nor
normalized. Deterministic typed-row serialization preserves all selected
columns and exact TEXT/BLOB bytes.

## Consequences

OpenCode imports remain deterministic, offline, snapshot-consistent, and
read-only with respect to the source database. Unsupported valid semantics
remain reasoned Unknown events under ADR 015; malformed known shapes remain
bounded diagnostics. Generation replacement cannot expose mixed normalization
versions or duplicate cross-generation evidence.
