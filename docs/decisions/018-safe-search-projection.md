# ADR-018: Build full-text search as a safe canonical projection

## Status

Accepted

## Context

Search is the first user-facing derived projection. It must remain rebuildable
without source files, must not expose raw SQLite FTS syntax, and must represent
partial lifecycle availability honestly. Canonical records can contain
untrusted commands, patches, paths, output, invalid text, and retained raw
payloads that are inappropriate to index implicitly.

## Decision

The search builder reads bounded, source-ordered canonical event pages from
SQLite. It derives source-neutral documents from explicit normalized fields,
never from retained raw records, unknown raw payloads, `data_json`, or source
files. Summaries and metadata have small field-specific bounds. Larger textual
payloads are indexed only when the complete field is valid UTF-8, contains no
NUL, and is no larger than 64 KiB.

Builds write token-isolated staging rows in bounded transactions. Publication
atomically replaces one session's active documents and file associations.
Active rows are owned by the canonical session through foreign keys. Failed or
canceled work does not publish staged data, and cleanup is best-effort without
altering canonical evidence.

SQLite FTS5 uses an external-content table maintained by triggers. The parser
accepts terms, phrases, and an allowlist of structured filters. It strictly
validates quotes, escapes, dates, identifiers, clause counts, and byte limits.
Every term is quoted in a generated FTS expression and every SQL predicate is
parameterized; user text is never treated as SQL or raw FTS syntax.

Queries join durable projection lifecycle state and use rows only when the
ready version and revision equal the current target and the document identity.
Aggregate availability is complete when all canonical sessions are usable,
partial when some are usable, and unavailable when sessions exist but none are
usable. Stale rows are never a fallback.

Search groups matching documents by session so each session appears at most
once. Text ranking multiplies each event's FTS5 BM25 rank by a bounded recency
factor. The newest timestamp in the current ready search corpus is the stable
age anchor; an event receives a 35 percent boost at that anchor, fading
linearly to no boost after 90 days. Missing timestamps receive no boost and
future timestamps cannot exceed the maximum. This matching-event policy avoids
refreshing old evidence merely because its session has unrelated later
activity. The best adjusted event ranks its session, with session ID as the
stable tie-breaker.

Filter-only results order sessions by canonical last activity with missing
activity last, then session ID. The best-ranked text event, or newest
filter-matching event, supplies a bounded explanatory snippet and the result
also reports the exact number of matching events. Bidirectional keyset cursors
are bound to the raw query, the ranking-policy identity, and the current usable
projection generation. Snippets are plain text; HTML escaping and terminal
sanitization remain presentation responsibilities.

## Consequences

- Search remains deterministic, offline, and reconstructable from canonical
  evidence after original session files disappear.
- A rebuild can invalidate a cursor; callers receive a safe validation error
  and restart from the first page.
- Sessions with pending, running, failed, stale, or unavailable search
  projections are distinguishable and excluded from results.
- Session-level FTS ranking is stable for an unchanged projection, with stable
  identifiers providing deterministic tie-breakers.
- Recency is relative to indexed canonical evidence rather than the wall clock,
  preserving deterministic offline results and stable pagination.
- Search results open the matching session timeline rather than treating an
  individual event as the primary result.
- Adding searchable canonical fields or changing limits requires a projection
  version change and rebuild.
