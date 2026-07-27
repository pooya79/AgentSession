# ADR 015: Canonical Unknown-event contract

## Status

Accepted.

## Context

Adapters encounter three different conditions: valid evidence whose kind is
not supported, complete records whose structure cannot be interpreted, and
incomplete stream tails. Treating all three as `Unknown` events overstates the
canonical timeline and obscures import integrity.

Retained records are sensitive, untrusted evidence. Ordinary timeline and
detail reads must not load them, but users need an explicit safe way to inspect
the evidence behind an `Unknown` event.

## Decision

An `Unknown` event represents valid timeline evidence that could not be mapped
to a typed canonical event. Its payload has one source-neutral reason:

- `unsupported_record_kind` for an unsupported valid top-level record;
- `unsupported_nested_variant` for an unsupported nested timeline variant.

`OriginalKind` is bounded classification metadata; the retained raw record is
authoritative. Valid non-timeline metadata emits neither an event nor a
diagnostic. A record may emit typed and `Unknown` events when only one nested
variant is unsupported.

Complete malformed records are retained and emit record diagnostics categorized
as `missing_discriminant` or `structurally_invalid_known_record`. Malformed
structure alone never creates an `Unknown` event. Incomplete stream tails are
not committed or checkpointed until complete.

Interpretation coverage is derived on read from the exact number of canonical
`Unknown` events and distinct retained records with a categorized malformed
diagnostic. It is `fully_interpreted` when both counts are zero and
`partially_interpreted` otherwise. Unrelated diagnostics, such as invalid
optional timestamps, do not affect coverage.

Coverage is independent of canonical `EvidenceState`, analysis findings, and
the outcomes `Successful`, `Partially successful`, `Failed`, `Abandoned`, and
`Unknown`.

Raw inspection is an explicit shared-service action allowed only for an
`Unknown` event whose session and event IDs match. Storage lazily decodes at
most 64 KiB. The application returns UTF-8-safe text after deterministic
redaction of private keys, authorization values, common secret assignments,
and recognized provider tokens. It reports original and returned sizes,
truncation, and redaction count. Raw content is excluded from logs, search
indexes, cursors, generic errors, and ordinary exploration reads.

The TUI loads inspection only on command, sanitizes terminal controls after
redaction, and clears it on navigation. The web interface uses a CSRF-protected
POST fragment, escaped bounded `<pre>` output, and `Cache-Control: no-store`.

## Reconciliation and identity

After release, adapter normalization-version changes trigger staged
reconciliation, atomic canonical replacement, and projection invalidation.
This contract is part of the initial normalization version and database schema,
so it does not introduce a compatibility-only version bump or migration.

An `Unknown`-to-typed conversion preserves an event ID when identity inputs and
event cardinality remain unchanged. A mapping that splits or merges events, or
acquires a native identity, may legitimately produce new event IDs.

## Consequences

Import integrity and interpretation coverage are visible separately. Adapters
remain the owners of source-specific parsing, while storage and presentation
consume source-neutral reasons. Inspection remains offline, read-only with
respect to sources and repositories, and compatible with single-binary
distribution.
