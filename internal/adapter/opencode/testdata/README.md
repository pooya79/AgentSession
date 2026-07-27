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

Every generation fixture contains multiple or empty sessions where applicable,
unknown TEXT/BLOB columns, and empty TEXT/BLOB values. Together the inventory
covers malformed JSON, missing or conflicting discriminants, malformed known
shapes, future kinds, valid rows after malformed rows, coexisting generations,
and exact typed-column retention. Large-record retention is generated
deterministically by `adapter_test.go` so the repository does not carry an
oversized fixture.
