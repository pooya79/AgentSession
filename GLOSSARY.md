# AgentSession

AgentSession explores recorded coding-agent sessions and the evidence they contain.

## Language

**Session timeline**:
The recorded activity of one coding-agent session, arranged in preserved source
order. It includes messages and technical activity; timestamps do not override
that order.

**Rule**:
A specific question about recorded session evidence, answered using fixed,
deterministic criteria.

**Rule assessment**:
An explained answer to a rule for a session: triggered, not triggered, not
applicable, or insufficient evidence. Evidence gaps limit the conclusions they
affect rather than invalidating every assessment of the session.

**Command identity**:
A command's recorded text, ignoring surrounding whitespace, together with its
established working directory. Different text or directories identify different
commands; an unknown directory leaves identity uncertain.

**Verification**:
A recognizable test, lint or static check, or build with recorded result evidence.
Its kind limits what success establishes: a successful build does not establish
that tests passed.
_Avoid_: Assistant assurance, tool-call success

**Applicable source change**:
A confirmed change to code, tests, or build/runtime configuration for which
verification is relevant. Clearly documentation-only changes are exempt; an
unknown file role leaves applicability uncertain.

**Repeated command failure**:
At least two distinct failed invocations of the same command without an
intervening successful invocation of that command. Later recovery does not
erase the historical repetition.
An intervening attempt with an unknown result leaves the failure sequence
uncertain.

**Unresolved failed verification**:
A failed verification with no later recorded successful verification of the
same command identity whose invocation began after that failure completed.
A pending rerun does not establish recovery.

**Missing verification**:
An applicable source change with no recorded completed verification targeting
its relevant scope and beginning after the change. A failed check is verification
performed; an unknown result leaves uncertainty.

**Verification scope**:
The package or directory targeted by a recognized check, limited by its check
kind and known exclusions. Targeting a scope does not prove that every file in
it participated or that the change is correct.
