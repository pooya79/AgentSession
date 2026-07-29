# ADR-019: Allow bounded opt-in payloads for inline timelines

## Status

Accepted

## Context

ADR-010 made timeline listings summary-only and reserved normalized payload
reads for a single selected event. That contract suited paginated lists and
standalone event details, but it forced a terminal user to open every event to
read a session. A continuous inline timeline needs complete normalized
evidence for each displayed card without making timeline reads unbounded or
exposing retained raw records.

## Decision

`TimelineRequest` has an explicit payload-inclusion option. When selected, the
application asks storage for normalized payloads belonging only to the event
IDs in the already bounded timeline page and returns them keyed by stable event
ID. The default remains summary-only.

SQLite performs one bounded batch read of inline and detached normalized
payloads. It does not select or join raw-record content. Missing event IDs are
omitted, cross-session IDs are excluded, and payload decoding failures remain
errors rather than being collapsed into successful evidence.

The TUI uses this option in 50-event chunks and presents the accumulated source
order as a conversation: user and assistant messages remain readable inline,
while technical activity is compact until explicitly expanded. The web UI and
focused standalone detail flow retain their existing summary-only and
single-event behavior.

## Consequences

- A normal TUI session can be read as one gradually loaded timeline without a
  click-through detail step or presentation page boundaries.
- Each storage request and response remains bounded by the exploration page
  limit, while presentation memory grows with the chunks the user explores.
- Normalized payloads cross the application boundary only through an explicit
  caller opt-in; retained raw evidence still requires its separate bounded,
  redacted Unknown-event action.
- Timeline summary callers remain compatible and do not pay payload decoding
  costs.
