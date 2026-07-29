# AgentSession documentation

The documentation is organized by the question a reader is trying to answer.

## Start here

| Document | Question it answers | Update when |
| --- | --- | --- |
| [Product contract](PRODUCT.md) | What is AgentSession, who is it for, and what must remain true? | Product direction or non-negotiable guarantees change |
| [Current state](CURRENT_STATE.md) | What is implemented, partial, absent, or not yet validated? | A milestone materially changes product capability |
| [Architecture](ARCHITECTURE.md) | How is the system structured, and which boundaries should implementation preserve? | Component ownership, data flow, or intended structure changes |
| [Decision register](decisions/README.md) | Why were consequential technical choices made? | An ADR is proposed, accepted, superseded, or deprecated |
| [Contribution rules](../AGENTS.md) | How should changes be implemented and verified? | Engineering constraints or project working agreements change |

The root [README](../README.md) is the user-facing introduction and command
reference. It should remain concise and link here for design and project-state
detail.

## Document roles

### Product contract

The product contract describes durable intent and trust boundaries. It should
leave room for the workflows, interface, and implementation to evolve.

It is not a feature backlog, release checklist, or architecture specification.

### Current state

The current-state document is a dated factual snapshot. It distinguishes
working, partial, absent, and unvalidated capabilities without presenting
planned behavior as implemented.

When it conflicts with the executable or tests, the executable and tests are
the evidence to investigate and the document should be corrected.

### Architecture

The architecture guide describes system boundaries, ownership, dependency
direction, and both current and explicitly identified intended capabilities.
It should explain the coherent design without repeating the full history of
every decision.

Consequential changes to canonical data, database ownership, product
guarantees, outcome semantics, or major module boundaries require an ADR.

### Architecture decision records

ADRs preserve the context and consequences of consequential decisions. Accepted
ADRs are historical records: later changes should normally supersede them with a
new ADR instead of rewriting the original decision.

The [decision register](decisions/README.md) is the authoritative navigation and
status index. ADR numbering is chronological; subject areas are represented in
the register rather than by moving files.

## Status language

Documentation should use these terms consistently:

- **Implemented:** present in the executable and covered by repository
  evidence.
- **Partial:** present with meaningful coverage or product limitations.
- **Planned:** intentionally described but not implemented.
- **Needs validation:** implemented or partially implemented, but not
  sufficiently exercised against representative use.
- **Out of scope:** intentionally outside the product boundary.

Avoid using “supported” when the intended meaning is only “designed,”
“registered,” or “planned.”

## Maintenance rules

- Link to one authoritative explanation instead of copying it into several
  documents.
- Keep user commands and observable behavior in the root README.
- Keep current implementation claims in `CURRENT_STATE.md`.
- Keep durable product promises in `PRODUCT.md`.
- Keep component responsibilities and dependency rules in `ARCHITECTURE.md`.
- Record the reason for consequential choices in an ADR.
- Do not create speculative documents, directories, or package descriptions
  solely to make a target layout appear complete.
- Prefer links to ADRs over duplicating their detailed contracts.

There is no committed roadmap document yet. The product contract and
current-state gaps should be reviewed before selecting and documenting the next
outcome-oriented milestones.
