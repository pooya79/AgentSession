# Architecture decision register

This directory contains architecture decision records (ADRs) for consequential
AgentSession design choices. ADR numbers are chronological and filenames remain
stable after acceptance.

## Statuses

- **Proposed:** under discussion and not yet binding.
- **Accepted:** the current decision unless a relation notes that part has been
  superseded.
- **Superseded:** replaced in full by a later ADR.
- **Deprecated:** retained for history but no longer recommended.

An accepted ADR may be partially superseded. The register records the affected
part and the replacement; the original ADR remains intact apart from clerical
corrections and link maintenance.

Use [TEMPLATE.md](TEMPLATE.md) when proposing a decision.

## Register

| ADR | Date | Status | Area | Decision | Relations |
| --- | --- | --- | --- | --- | --- |
| [001](001-modular-go-application.md) | 2026-07-15 | Accepted | Foundation | Modular Go application with two presentation layers | — |
| [002](002-incremental-record-diagnostics.md) | 2026-07-18 | Accepted | Import integrity | Persist record diagnostics incrementally | — |
| [003](003-verified-batched-imports.md) | 2026-07-19 | Accepted; recovery clause superseded | Import integrity | Verify appends and separate canonical commits from projections | Recovery behavior replaced by [ADR-004](004-adapter-checkpoints-and-staged-reconciliation.md) |
| [004](004-adapter-checkpoints-and-staged-reconciliation.md) | 2026-07-21 | Accepted | Import integrity | Use adapter-owned checkpoints and staged reconciliation | Partially supersedes [ADR-003](003-verified-batched-imports.md) |
| [005](005-retained-metadata-records.md) | 2026-07-21 | Accepted | Canonical evidence | Retain metadata records without timeline evidence | — |
| [006](006-container-logical-sources.md) | 2026-07-22 | Accepted | Adapters | Expand database containers into logical session sources | — |
| [007](007-application-owned-import-lifecycle.md) | 2026-07-22 | Accepted | Application lifecycle | Own import lifecycles in the application layer | — |
| [008](008-versioned-projection-lifecycle.md) | 2026-07-22 | Accepted | Derived data | Track versioned projection lifecycles durably | — |
| [009](009-application-runtime-composition.md) | 2026-07-22 | Accepted | Foundation | Compose runnable infrastructure in an application runtime | — |
| [010](010-bounded-evidence-exploration-services.md) | 2026-07-22 | Accepted | Exploration | Expose bounded canonical evidence exploration services | — |
| [011](011-application-owned-import-all-lifecycle.md) | 2026-07-25 | Accepted | Application lifecycle | Coordinate all-source indexing in the application layer | — |
| [012](012-application-owned-projection-actions.md) | 2026-07-25 | Accepted | Derived data | Own projection actions in the application runtime | — |
| [013](013-maintained-session-exploration-fields.md) | 2026-07-25 | Accepted | Exploration | Maintain bounded session exploration fields | — |
| [014](014-server-rendered-web-operations-console.md) | 2026-07-26 | Accepted | Web | Server-rendered web operations console | — |
| [015](015-canonical-unknown-event-contract.md) | 2026-07-27 | Accepted | Canonical evidence | Canonical Unknown-event contract | — |
| [016](016-opencode-storage-generations.md) | 2026-07-27 | Accepted | Adapters | Select one OpenCode storage generation per session | Applies [ADR-015](015-canonical-unknown-event-contract.md) |
| [017](017-claude-agent-session-identities.md) | 2026-07-28 | Accepted | Adapters | Give Claude agent logs distinct canonical session identities | — |
| [018](018-safe-search-projection.md) | 2026-07-28 | Accepted | Search | Build full-text search as a safe canonical projection | Implements the lifecycle from [ADR-008](008-versioned-projection-lifecycle.md) |
| [019](019-opt-in-inline-timeline-payloads.md) | 2026-07-29 | Accepted; web limitation superseded | Exploration | Allow bounded opt-in payloads for inline timelines | Web-specific limitation replaced by [ADR-020](020-canonical-summary-categories-and-continuous-web-timeline.md) |
| [020](020-canonical-summary-categories-and-continuous-web-timeline.md) | 2026-07-29 | Accepted | Canonical evidence, web | Categorize recorded summaries and render bounded continuous web timelines | Extends [ADR-010](010-bounded-evidence-exploration-services.md) and [ADR-019](019-opt-in-inline-timeline-payloads.md) |

Dates are the dates each ADR first entered repository history.

## Subject guide

### Product shape and composition

- [ADR-001](001-modular-go-application.md): executable and presentation-layer
  shape.
- [ADR-009](009-application-runtime-composition.md): runtime composition and
  lifecycle.

### Import integrity and canonical evidence

- [ADR-002](002-incremental-record-diagnostics.md): incremental diagnostics.
- [ADR-003](003-verified-batched-imports.md): verified batching and canonical
  commit boundaries.
- [ADR-004](004-adapter-checkpoints-and-staged-reconciliation.md): checkpoint
  ownership and reconciliation.
- [ADR-005](005-retained-metadata-records.md): retained non-timeline metadata.
- [ADR-006](006-container-logical-sources.md): physical containers and logical
  sources.
- [ADR-007](007-application-owned-import-lifecycle.md): per-source import
  lifecycle.
- [ADR-011](011-application-owned-import-all-lifecycle.md): all-source indexing
  lifecycle.
- [ADR-015](015-canonical-unknown-event-contract.md): unsupported and malformed
  evidence.

### Source-specific decisions

- [ADR-016](016-opencode-storage-generations.md): OpenCode generation
  selection.
- [ADR-017](017-claude-agent-session-identities.md): Claude agent-session
  identity.

Source-specific decisions remain adapter decisions and do not authorize
source-specific behavior in shared layers.

### Exploration and presentation

- [ADR-010](010-bounded-evidence-exploration-services.md): shared bounded
  exploration services.
- [ADR-013](013-maintained-session-exploration-fields.md): session listing and
  preview fields.
- [ADR-014](014-server-rendered-web-operations-console.md): web interaction
  model.
- [ADR-019](019-opt-in-inline-timeline-payloads.md): bounded normalized
  payload batches for continuous inline timelines.
- [ADR-020](020-canonical-summary-categories-and-continuous-web-timeline.md):
  canonical summary categories and the continuous web timeline.

### Derived data and search

- [ADR-008](008-versioned-projection-lifecycle.md): durable projection
  lifecycle.
- [ADR-012](012-application-owned-projection-actions.md): retry and rebuild
  actions.
- [ADR-018](018-safe-search-projection.md): safe full-text search.

## Adding or changing a decision

1. Copy [TEMPLATE.md](TEMPLATE.md) to the next unused zero-padded number and a
   short descriptive filename.
2. Mark it `Proposed` while alternatives and consequences are still being
   discussed.
3. Link related ADRs explicitly.
4. Change it to `Accepted` only when the project has committed to the decision.
5. Add or update its row and subject entry in this register.
6. If it replaces an earlier decision, leave the earlier file in place and
   record the supersession in both ADRs and this register.

Small implementation choices that are easy to reverse do not require an ADR.
ADRs are also not substitutes for a roadmap, issue, or implementation plan.
