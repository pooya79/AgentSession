# ADR-005: Retain metadata records without timeline evidence

## Status

Accepted.

## Context

Some source formats persist session metadata as an ordinary source record. The
record is authoritative raw evidence and must participate in incremental
checkpointing, but manufacturing a timeline event for it would misrepresent the
source. Requiring a diagnostic would similarly describe valid metadata as a
problem.

## Decision

`RecordEnvelope` permits a retained record with no canonical event and no
diagnostic. Adapters use this zero-event lifecycle only for valid metadata or
other explicitly consumed non-timeline records. Malformed records emit a
diagnostic, and unsupported records emit an `Unknown` event.

## Consequences

- Metadata bytes and source positions remain available and checkpoints advance.
- Timeline views do not show invented metadata activity.
- Storage continues to retain the envelope's raw record in the same atomic
  batch as its checkpoint.
