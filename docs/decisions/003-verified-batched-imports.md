# ADR-003: Verify appends and separate canonical commits from projections

## Status

Accepted

## Context

Large appendable sources cannot safely resume from byte offsets alone.
Canonical evidence and its cursor must become durable together, while
rebuildable projections must not extend the authoritative transaction.

An incomplete trailing record is not stable evidence. Checkpointing past it
would prevent a later append from repairing it. Empty and partial-only sources
also need a durable cursor even though no complete record exists.

## Decision

The coordinator asks the selected adapter to verify the stored prefix before
supplying a resume checkpoint. Failed verification stops without changing
authoritative data; reconciliation remains a separate explicit operation.

Adapters synchronously stream complete records into a bounded sink. Each sink
batch commits raw records, events, record diagnostics, the session snapshot,
and its checkpoint in one transaction. Earlier batches remain committed if
cancellation or failure discards the current batch.

Sequence `-1`, offset `0`, and last-record hash `none` represent the position
before the first complete record. Incomplete trailing bytes are deferred and
retried after an append. Complete malformed records are retained with
diagnostics.

Projection work starts after adapter completion and the final canonical commit.
Projection errors are reported separately and never roll back canonical data.

## Consequences

- Verification may read the committed prefix, but normalization opens directly
  at the verified offset and does not reparse committed records.
- Record and batch limits bound coordinator memory. Oversized records fail
  rather than being truncated or retained incompletely.
- Adapter, format, model, and normalization identities must match durable state.
- Projections remain idempotent and rebuildable from canonical evidence.
