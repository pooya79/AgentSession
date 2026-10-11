# ADR-022: Bound TUI timeline retention around the reading position

## Status

Accepted

The consolidated design was confirmed on 2026-10-11. This record establishes
the intended retention policy; it does not claim implementation.

## Context

ADR-019 allows the TUI to accumulate normalized payloads and rendered timeline
content as users explore more history. Code inspection shows that selection
movement currently invalidates the complete rendered timeline, rebuilding all
loaded cards. This establishes increasing rendering work, but actual latency
and memory use have not yet been measured.

The agreed reading experience separates text scrolling from event navigation
and preserves position through disclosure, resize, inspection, and history
loading. Long sessions should not require retaining their complete payloads
and rendered text. Eviction trades memory retention for local reads and more
complex position restoration.

## Decision

The TUI will retain a bounded working set of normalized timeline payloads and
rendered cards around the reading position. Retention will account for both
event count and content size; numerical budgets will be selected and measured
during implementation. Evicted evidence remains in the AgentSession index and
can be reloaded through shared application services.

Reading position is anchored to a stable event ID and a location within its
content, rather than an absolute row in the complete rendered session. Keep
lightweight navigation and expansion state separately from cached content.
Reloading, wrapping changes, and disclosure must preserve the anchor where
the underlying event still exists. If reimport removes or changes that
evidence, present the changed availability honestly rather than promising
exact restoration.

Existing focused timeline reads can retrieve a bounded page ending at a
known event, and forward cursors can continue after it. Use these capabilities
before adding service APIs solely for the TUI. Request-generation and
cancellation handling must allow evicted/loading content to be retried without
leaving the interface stuck or accepting stale responses.

Page limits bound event count, not an individual payload's byte size. Cache
budgets therefore do not establish a strict process memory ceiling for
arbitrarily large individual events. Measure large-event loading and rendering
separately; any necessary shared payload-access change requires an explicit
follow-up decision. Evidence must not be silently truncated to claim compliance.

## Consequences

- Exploring more history will no longer require retaining every visited
  payload and rendered card. Lightweight per-event UI state can still grow
  with visited history and must be measured separately.
- Scrolling into evicted content may require a local index read. Loading and
  failure states must remain visible and retryable while input stays usable.
- Position restoration and two-direction history loading need focused tests
  for overlap, eviction, stale results, cancellation, resize, and reimport.
- This changes TUI retention only. Shared bounded reads, payload opt-in, raw
  evidence restrictions, canonical ordering, and web behavior remain governed
  by their existing decisions.

## Alternatives considered

- **Retain all explored payloads and optimize rendering only:** simpler
  navigation, but memory still grows with explored payload volume.
- **Explicit presentation pages:** naturally bounds content, but interrupts
  the agreed continuous reading experience.
- **Add backward cursors first:** could improve symmetry and earlier-history
  availability reporting, but existing focused reads already support bounded
  reloads. A new API is not required to establish this retention policy.

## Relations

Partially supersedes [ADR-019](019-opt-in-inline-timeline-payloads.md)
only for accumulating TUI presentation content. Applies the bounded service
and non-snapshot cursor contract in
[ADR-010](010-bounded-evidence-exploration-services.md). Does not change the
web presentation decision in
[ADR-020](020-canonical-summary-categories-and-continuous-web-timeline.md).
