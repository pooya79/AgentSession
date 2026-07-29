# ADR-020: Categorize recorded summaries and render bounded continuous web timelines

## Status

Accepted

## Context

Canonical `Summary` events previously carried only text. Presentations therefore
could not distinguish reasoning, context compaction, plan updates, and generic
session summaries without inspecting source-specific labels. The web interface
also retained a summary-only event log and fetched one payload at a time,
despite ADR-019 establishing bounded opt-in payload reads for continuous
timelines.

## Decision

`SummaryData` carries one required source-neutral category: `reasoning`,
`context`, `plan`, or `summary`. Adapters select the category while normalizing
upstream records. Shared and presentation packages do not inspect source names
or source-specific fields to recover this meaning.

The category is part of the normalized v1 JSON payload. It does not require a
SQL migration because normalized payloads already live in `data_json` and the
product has no released compatibility obligation.

Both presentation layers use the category for titles while keeping their own
disclosure mechanics. The web timeline requests normalized payloads for each
bounded 50-event page, renders them as escaped plaintext components, and uses a
viewport-triggered htmx continuation sentinel. The sentinel remains a normal
`rel="next"` link, so bounded navigation works without JavaScript. Focused event
URLs still request a bounded window and highlight the target.

Retained raw records are not included in timeline payloads. Unknown-event raw
evidence remains available only through the explicit bounded, redacted,
CSRF-protected inspection action.

## Consequences

- Summary meaning is canonical and testable without leaking adapter knowledge.
- TUI and web timelines can give reasoning, context, plans, and summaries
  consistent titles.
- Web payload work and memory use remain bounded even though JavaScript users
  experience a continuously appended timeline.
- Missing normalized payloads are presented as unavailable evidence rather
  than being replaced by concise event summaries.
- Native `<details>` disclosure and continuation links preserve core reading
  and expansion behavior when JavaScript is unavailable.

## Relations

Extends ADR-010 and ADR-019. Supersedes ADR-019's web-specific decision to keep
the summary-only, single-event payload flow; ADR-019's application payload
boundary and default opt-in behavior remain accepted.
