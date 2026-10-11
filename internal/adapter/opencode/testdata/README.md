# OpenCode adapter fixtures

These fixtures are synthetic and **schema-derived**. They are not observed
production records. The client version for every fixture is `unspecified
(schema-derived)`.

Pinned upstream JSON schema references:

- legacy `MessageV2` message and part definitions:
  <https://github.com/anomalyco/opencode/blob/0b4edfc64ce1c1235737a6ffb1ed93c4cab418fe/packages/opencode/src/session/message-v2.ts>
- sequenced messages:
  <https://github.com/anomalyco/opencode/blob/0b4edfc64ce1c1235737a6ffb1ed93c4cab418fe/packages/schema/src/session-message.ts>
- durable events:
  <https://github.com/anomalyco/opencode/blob/0b4edfc64ce1c1235737a6ffb1ed93c4cab418fe/packages/schema/src/session-event.ts>

The DDL version labels below are the generation labels supplied to this
feature; upstream client versions were not supplied.

| Fixture | Generation / supplied DDL label | Shapes and expected inventory |
| --- | --- | --- |
| `valid_multi_session.sql` | legacy baseline / message-part-v1 | User text, completed tool call/result, assistant usage: typed. Future role: Unknown `message:future-role`. Malformed part: diagnostic only. |
| `legacy_variants_v1.sql` | legacy / message-part-v1 | Text and file source: typed. Step finish: Usage + residual `part:step-finish:unmapped`. Retry: Error + residual `part:retry:unmapped`. Future part: Unknown `part:future-part`. Empty session: retained metadata only. |
| `session_message_v1.sql` | sequenced / session-message-v1 | User/system/text, shell, assistant text/tool, usage, and compaction: typed. Assistant reasoning: Unknown `session_message:assistant:reasoning`. Future type: Unknown `session_message:future-kind`. Malformed JSON: diagnostic only; a later valid row continues. |
| `durable_events_v1.sql` | durable / durable-event-v1 | Prompt, shell lifecycle, tool lifecycle, settlement usage, and compaction: typed where faithful. Step settlement also emits residual `event:session.next.step.ended:unmapped`. Future durable type: Unknown `event:session.next.future`. Empty session: retained metadata only. |
| `coexisting_generations.sql` | all three supplied DDL labels | Complete durable evidence is selected exclusively. An incomplete durable sequence falls back exclusively to populated sequenced messages. Duplicate older rows are not retained or normalized. |
| `malformed_variants.sql` | sequenced / session-message-v1 | JSON null, empty JSON, array JSON, a conflicting discriminator, and malformed known text are diagnostic only. A future kind is Unknown. The following valid system row normalizes. |
| `findings_evidence_v1.sql` | all three supplied DDL labels | Durable structured Bash exits establish repetition/recovery and unresolved Go verification; structured Write results distinguish completed failed verification from a later missing check. Older alternative histories are excluded. Sequenced final snapshots lack ordered starts; legacy prose and patch parts remain uncertain. A separate durable session covers timeout, malformed and future records. Timestamps intentionally disagree with source sequence. |
| `modern_mutations_v1.sql` | durable / durable-event-v1 | Successful Edit `structured.files` with one modified absolute path and ApplyPatch `structured.applied` with explicit add/delete targets confirm application. Integration variants reject previews, relative paths, null, partial, empty Edit, and unknown or foreign operations. |

Every generation fixture contains multiple or empty sessions where applicable,
unknown TEXT/BLOB columns, and empty TEXT/BLOB values. Together the inventory
covers malformed JSON, missing or conflicting discriminants, malformed known
shapes, future kinds, valid rows after malformed rows, coexisting generations,
and exact typed-column retention. Large-record retention is generated
deterministically by `adapter_test.go` so the repository does not carry an
oversized fixture.

## Findings normalization contract

These additions are derived from the same pinned revision above, including
`packages/core/src/tool/{bash,write,edit,apply-patch,tool}.ts`. They are synthetic
examples, not a claim of validation against production databases.

| Generation/form | Canonical evidence and limitation |
| --- | --- |
| Durable `session.next.tool.called` / `success` | `bash` command/input and explicit absolute `workdir` at the ordered call; linked integer `structured.exit` at success. Nonzero exits are failures even though tool status is success. Timeout or tool failure never becomes an exit. Results need not repeat the tool name: bounded adapter attribution uses `callID`. |
| Sequenced assistant tool snapshots | Same modern structured process and change fields. A final snapshot has no source-ordered start; input and result adjacency never establishes a sequential rerun. Earlier explicit running records can retain starts where actually recorded. |
| Modern Write | Successful `structured.operation=write`, absolute `target`, and boolean `existed` confirm a create/update effect. Relative `resource` is display context only. |
| Modern Edit | Successful `structured.files` containing exactly one absolute `file` with `status=modified` confirms the ordinary update. Preview patch text alone is insufficient. |
| Modern ApplyPatch | Successful `structured.applied` enumerates absolute `target` paths and `type=add/update/delete`; `files` previews alone do not confirm effects. An empty applied list is a no-op. Unknown/malformed entries make the entire result uncertain. |
| Legacy tool snapshots / patch parts | Recognized Bash command intent and Edit/Write/ApplyPatch intent remain source-neutral evidence. Legacy metadata exits and mutation results are not interpreted in this slice; status, prose, and patch file lists alone cannot establish effects. |
| Shell lifecycle / shell messages | Existing shell records preserve invocation/output linkage but supply neither a numeric exit nor an established directory; findings remain uncertain. |
| Incomplete durable selection without fallback | Positioned diagnostics identify skipped ranges and missing tails. Ambiguous ordering/markers produce unlocated diagnostics. Missing intervals may contain recovery or verification; established earlier repetition and positive completed checks remain usable. This does not change generation selection. |

Paths/bases require clean absolute POSIX paths; relative paths and unsupported
Windows forms remain uncertain. Durable attribution retains 256 recent IDs/tool
names of at most 512 bytes each plus input hashes. Resume reconstructs attribution
by streaming the selected prefix; no full history or raw command output is held
in attribution state. Missing/evicted/conflicting attribution stays generic.
Large command/output records are generated in runtime tests and round-trip intact;
the established bounded findings fields prevent oversized text from becoming a
definitive conclusion. Both consumers exercise the same persisted assessments,
evidence links and rebuilding controls through the shared service contract.

Incomplete appends reconcile the selected generation to relocate prior tail diagnostics. Cursor v1 includes marker state, so marker-only changes also reconcile. Diagnostic boundaries persist in the initial v1 schema and are consumed without source-specific shared logic.
