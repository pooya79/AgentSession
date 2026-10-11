# ADR-021: Assess rules within explicit recorded evidence

## Status

Accepted

## Context

Issue [#48](https://github.com/pooya79/AgentSession/issues/48) introduces
deterministic assessments for repeated command failures, unresolved failed
verification, and missing verification after applicable source changes.

Existing canonical evidence does not uniformly distinguish command invocation
from command completion, or patch intent from confirmed source changes.
Generic tool success is not necessarily command success. Recorded session end
timestamps do not establish that the imported record is a complete history.
Rules that silently fill these gaps would overstate the available evidence.

## Decision

The design interview settled the following boundaries, confirmed on
2026-10-11. This ADR records the agreed design; implementation is separate work.

- Every initial rule produces an inspectable session assessment in one of four
  states: triggered, not triggered, not applicable, or insufficient evidence.
  Presentation emphasizes triggered assessments while retaining access to the
  other results and their explanations.
- Evidence gaps limit the conclusions they affect. They do not automatically
  invalidate conclusions established independently by positive evidence.
- Include the minimal canonical evidence and adapter changes needed to retain
  explicit command results and confirmed file changes where source records
  supply them. Source-specific interpretation remains adapter-owned. A patch
  request alone does not establish a source change, and tool-call success alone
  does not establish successful verification.
- Assess the currently imported record in source order. Explanations describe
  that evidence boundary rather than claiming to know all real-world activity
  or that a session is finished. New canonical evidence causes reassessment;
  a later relevant successful verification can resolve an earlier failure.
- Preserve uncertainty when the source or interpretation cannot support the
  specific conclusion. Assistant claims are not proof of success.
- Command identity requires matching recorded command text, ignoring
  surrounding whitespace, and an established matching working directory.
  Different commands are not treated as equivalent; unknown directories do
  not establish identity.
- Verification includes explicitly recognized tests, lint/static checks, and
  builds. Preserve the check kind: a successful build cannot resolve a failed
  test. Recognizers use a documented, tested set of source-neutral command
  forms; unfamiliar commands do not silently count as verification.
- Missing-verification applicability requires confirmed changes to code,
  tests, or build/runtime configuration. Clearly documentation-only changes
  are exempt. Unfamiliar file roles remain uncertain unless established by
  recorded evidence, using an explicit conservative path policy.
- Repeated command failure requires two distinct failed invocations of the
  same command without an intervening success of that command. Other commands
  do not break the sequence. Duplicate result records are not extra attempts.
  Success ends the failure sequence but does not erase the historical finding;
  explanations show later recovery when recorded.
- A failed verification is resolved only by a later successful verification
  with the same command identity. A later failure reopens the condition; a
  pending rerun does not establish recovery.
- A completed relevant check counts as verification performed even if it
  fails. Missing verification and unresolved failed verification remain
  separate conditions. A started check with an unknown result leaves
  uncertainty.
- A verification covers a changed file only when its recognized scope includes
  the file and its invocation starts after the change in source order. A check
  started before a change does not verify that change merely because it ends
  later. Unknown paths or scope remain uncertain. Do not consult the live
  repository or infer its dependency graph to establish coverage.
- An intervening attempt with an unknown result cannot be assumed to continue
  a failure sequence. Interpretation gaps that could contain relevant recovery
  prevent a confident unresolved-failure conclusion.
- Aggregate each rule's instances into one session assessment: triggered if
  any instance is conclusively established; otherwise insufficient evidence
  if relevant uncertainty could change the answer; otherwise not triggered
  when applicable evidence was assessed; otherwise not applicable when enough
  evidence establishes that nothing applicable occurred. A triggered result
  still explains remaining uncertainty.
- Initial verification and change-applicability recognition focuses on Go.
  Repeated-command detection remains language-independent. Recognize only
  understood standalone Go test, vet, and build forms, local targets, and
  understood flags. Other languages and unfamiliar shell forms remain
  explicitly unsupported or uncertain. Account for check-kind limits such as
  builds excluding test files and listing tests not executing them.
- Evaluate all evidence while keeping assessment output bounded. Include
  representative supporting event links, total counts, and an explicit notice
  when examples are omitted. The canonical timeline retains the full evidence;
  examples must never be presented as an exhaustive set.
- Conflicting results linked to one invocation make that invocation's result
  unknown. Source order does not silently settle contradictory result evidence.
- Recovery requires a check invocation beginning after the earlier failure
  completed. Parallel completion order alone does not establish a rerun. A
  change made during a running check remains unverified by that check.
- For missing verification, scope relevance means a recorded check targeting
  the relevant package or directory with a suitable check kind. It does not
  establish actual participation of every file, test coverage, or correctness.
  Known exclusions do not count; unclear target relationships remain uncertain.
- Both interfaces expose assessments in the existing session detail workflow,
  with triggered results first. Show state, explanation, counts, limitations,
  and rule identity/version, and navigate supporting links to the relevant
  timeline context. Existing projection status explains unavailable or stale
  results. A cross-session findings dashboard is outside this slice.
- Initial recognizers accept one local target: omitted, `.`, `./...`, a literal
  `./path`, or `./path/...`, with an established working directory. Supported
  optional flags are `-v`, `-json`, `-race`, and `-count=1` for `go test`;
  `-json` for `go vet`; and `-v` and `-trimpath` for `go build`. Other flags,
  multiple targets, shell chains, wrappers, and environment assignments remain
  uncertain for verification analysis. They remain eligible for independent
  repeated-command detection when identity and result evidence are sufficient.
- Moves affect both old and new applicable scopes. Module configuration
  changes such as `go.mod` or `go.sum` require a whole-module target, not an
  isolated subpackage check. Unknown module/workspace boundaries leave coverage
  uncertain; workspace configuration requires recorded evidence establishing
  the affected scope.
- Initial assessments omit severity grading. Report conditions, evidence,
  recovery, and uncertainty without judging session quality. Outcome
  classification remains independent and outside this issue.

Implementation must preserve the existing read-only, offline boundaries.
Adapters retain explicit invocation/result linkage and change confirmation
where available; missing or contradictory evidence is not fabricated. Analysis
consumes source-neutral canonical evidence rather than raw source formats,
assistant assurances, or guesses from command-output prose.

Findings remain an independently rebuildable projection under the existing
version/revision lifecycle. Publish complete results atomically and expose them
only when usable for the current canonical revision. Rebuild from authoritative
AgentSession data without reopening source sessions or inspecting live
repositories. Keep initial AgentSession-owned schema and algorithm identities
at their initial v1; introduce no compatibility-only aliases or version bumps.

Both interfaces call shared services for assessments and evidence navigation.
Bounds must be explicit and tested; sampling, oversized fields, or omitted
examples must never silently change a conclusion. Routine choices such as
numeric limits, storage layout, and test organization remain implementation
details within these boundaries.

## Consequences

### Implemented canonical change contract

`PatchData` retains text and path context and now also carries a source-neutral
change ID, status, recorded working directory, and structured affected mutations
(operation, path, and previous path). Status distinguishes requested, confirmed,
failed, no-op, and unknown evidence; an empty status remains uninterpreted patch
context. Only a confirmed status establishes mutation. This implements the
minimal evidence extension authorized above rather than treating patch intent
as a file change. Raw request/result records remain authoritative and ordered.

Initial Codex confirmation requires a structured successful completed
`patch_apply_end` with understood affected changes. Source parsing and no-op
recognition stay in the adapter. Linked duplicates and contradictions are
reconciled over stored source-neutral results before rule evaluation. Other
adapters' unconfirmed mutation payloads remain uncertain for this rule.

The initial v1 migration stores bounded canonical change-result fields and
affected paths independently of arbitrary patch text. Rebuilds use these fields
and existing explicit command/interpretation evidence. Oversized fields produce
uncertainty without truncating the authoritative normalized payload or raw
record. The findings projection atomically publishes all three v1 assessments.
Ordinary package-local Go changes are assessed now; move/module/workspace scope
recognition remains uncertain as permitted by the staged implementation in #82.

### Positioned interpretation gaps

OpenCode durable sequences can omit rows even when the retained rows have valid,
supported shapes. Such omissions are diagnostics, never synthetic Unknown
events: ADR-015 reserves Unknown for valid unsupported timeline evidence.

The canonical diagnostic contract carries the source-neutral reason
`incomplete_source_sequence` and an optional interpretation boundary. The default
boundary precedes the owning raw record's canonical events; `after_record`
follows them, and `unlocated` means source order cannot locate the gap. These
boundaries also apply to other interpretation reasons when a source establishes
their position. Persist the boundary in the initial v1 record-diagnostics schema
and include it in authoritative reconciliation fingerprints. Shared evidence
readers interpret only that generic boundary, never OpenCode sequence fields.

The adapter identifies skipped sequence ranges and missing tails inside its
query-only snapshot. Tail uncertainty does not erase historical repetition or
recorded recovery before the gap, but can limit absence of later recovery or
verification. Ambiguous markers/order remain unlocated. Incomplete appends use
staged reconciliation because the previous tail diagnostic can move; cursor v1
also records the selected marker state so marker-only changes invalidate derived
evidence. Normal complete-generation appends keep their existing incremental path.

### Product consequences

- Findings can remain useful for sessions that are still receiving events.
- Sources with weaker evidence may yield insufficient-evidence assessments.
- This work includes focused adapter/model improvements rather than deriving
  all rules from the current payloads unchanged.
- New evidence contracts and adapter fixtures need focused tests. Storage,
  services, and interfaces must not interpret source-specific tool formats.
- Initial Go-only recognition is a user-visible limitation to document.
- Unsupported flags and shell forms can make common real sessions uncertain;
  expanding recognition requires deliberate fixtures and documented semantics.
- Current sanitized fixtures do not cover ordinary Claude Bash/Edit/Write or
  yielded Codex command completion. Additional representative fixtures are
  needed before claiming support for those evidence forms.
- Small local targets cannot verify shared module configuration; incomplete
  module/workspace evidence limits the missing-verification assessment.
- Implementation and additional representative adapter fixtures remain
  follow-up work.

## Alternatives considered

- Use only existing payloads: smaller scope, but generic tool records leave
  modern sessions poorly assessable and patch requests cannot prove changes.
- Infer command success or changes from tool invocation: simpler heuristics,
  but conflates attempts with results.
- Require a globally complete session before producing any assessment: hides
  established observations and depends on completeness evidence not reliably
  available in the current model.

## Relations

Applies [ADR-008](008-versioned-projection-lifecycle.md) and
[ADR-012](012-application-owned-projection-actions.md) to findings. Preserves
the distinction between interpretation coverage and analysis established by
[ADR-015](015-canonical-unknown-event-contract.md).
