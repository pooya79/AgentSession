# ADR-008: Track versioned projection lifecycles durably

## Status

Accepted.

## Context

Search, Git correlation, findings, outcomes, and aggregates are derived from
canonical session evidence. Their algorithms will evolve independently, their
work may fail after an import commits, and multiple import or service requests
may ask for the same rebuild concurrently. Treating a post-import call as
implicitly successful would make stale or absent derived data indistinguishable
from current data. Persisting arbitrary builder errors would also copy untrusted
session content, paths, or command output into diagnostics.

## Decision

Every session has a monotonic canonical revision advanced only when an
authoritative transaction changes canonical evidence. Idempotent batch and
reconciliation retries preserve both the revision and ready projection state.
A changing transaction marks every registered projection pending for the new
revision. Projection definitions use opaque, non-empty algorithm versions;
inequality requires a rebuild and does not imply ordering.

Per-session state records the target version and revision, the last ready
version and revision, status, attempts, timestamps, and a random run token.
Claims and completions use compare-and-swap updates. A completion is accepted
only for its active token and only while the session still has the claimed
canonical revision. Version or revision changes retain the last ready identity
for diagnosis but reset attempts and failure details for the new target.

Builders run after the canonical commit in the fixed order search, Git
correlation, findings, outcomes, and aggregates. They receive the session ID,
claimed revision, and authoritative database readers. They never receive a
source path, adapter, or source-opening capability. Missing builders leave work
pending. One failure does not prevent attempts for later registered builders.

In-process requests for a session share one flight, while database run tokens
and renewable leases protect against other callers. Cancellation releases a
claim to pending; an expired lease allows a later caller to recover genuinely
abandoned work without resetting claims owned by another live manager. A
flight repeats its pass when a completion was superseded by newer canonical
evidence. Forced rebuild invalidation uses the same shared application service
as retry and status queries, and a rebuild that joins a flight runs another
pass when the requested kind was already visited.

Durable failure diagnostics contain only projection identity, target identity,
a bounded stable code, a generic or explicitly safe bounded summary, attempt,
and time. Ordinary `error.Error()` text is neither stored nor exposed by the
projection result. Import progress reports a generic warning and directs users
to projection status.

## Consequences

- Canonical records and checkpoints remain committed when derived work fails.
- Consumers may use output only when state is ready for the current target
  version and canonical revision.
- Projection outputs can be rebuilt after the original source disappears,
  because session evidence comes from authoritative SQLite content.
- Session deletion and reconciliation remove lifecycle state through foreign
  key ownership.
- There is no background scheduler or backoff in this slice; later imports and
  explicit service requests retry pending or failed work.
- Projection engines and their output schemas remain separate future changes.
