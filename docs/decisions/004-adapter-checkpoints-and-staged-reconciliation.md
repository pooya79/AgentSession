# ADR-004: Use adapter-owned checkpoints and staged reconciliation

## Status

Accepted; supersedes ADR-003 where it required verification failure to stop
without automatic reconciliation.

## Context

Byte offsets and record sequences describe append-only streams but do not prove
that committed evidence is still authoritative. They also do not model a
logical database cursor. Replacing live evidence with the first recovery batch
would expose a partial generation if recovery were interrupted.

## Decision

Checkpoints retain source identity and canonical record sequence, plus an
opaque versioned adapter cursor and streaming fingerprint. The selected adapter
prepares one consistent read-only source view and uses it to classify unchanged
input, append, truncation, replacement, or mutation before the checkpoint.

Truncation, replacement, and mutation trigger adapter-specific reconciliation
automatically. Reconciliation streams bounded batches into persistent SQLite
staging. A final transaction verifies the expected live checkpoint, deletes
only the affected AgentSession-owned generation and its cascading projections,
promotes every staged batch, and removes staging metadata. Until that commit,
readers continue to see the previous complete generation.

## Consequences

- File adapters may use offsets internally, but offset or sequence equality is
  never sufficient verification.
- Database adapters use stable logical cursors and deterministic row digests
  from a read transaction rather than database-file byte positions.
- Normal cancellation aborts staging; a process interruption leaves isolated
  staging that the next attempt replaces.
- Reconciliation retries are idempotent, and concurrent live checkpoint
  changes prevent stale staged data from being promoted.
- Original source files and databases remain read-only.
