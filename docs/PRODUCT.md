# AgentSession product contract

## Purpose

AgentSession helps developers explore and understand their local coding-agent
sessions.

It transforms records from supported coding agents into a consistent and
searchable experience for reviewing conversations, actions, errors, changes,
and available usage information.

The product should make previous sessions easier to find and understand than
their original source records.

## Primary user

AgentSession is initially intended for developers who use coding agents and
want a better way to inspect their previous sessions.

It is designed first for an individual developer working with sessions
available on their own computer.

## User needs

A developer may use AgentSession when:

- A session is too long or complex to review manually.
- They need to find a previous conversation or action.
- They want to understand what happened during a session.
- They want to investigate errors, commands, tool activity, or file changes.
- They want to understand patterns or usage across multiple sessions.
- They want to know when available records are incomplete or not fully
  understood.

Coding-agent records are often source-specific, fragmented, and difficult to
browse, search, or compare directly.

## Core experience

AgentSession should allow a developer to:

1. Make local coding-agent sessions available for exploration.
2. Find a relevant session.
3. Review the activity within that session.
4. Search across available session evidence.
5. Understand important activity, problems, and gaps in the available
   information.
6. Explore useful patterns across sessions when the underlying data supports
   them.

The exact presentation and workflow may evolve as the product develops.

## Information AgentSession may present

AgentSession may present information such as:

- Conversations and messages.
- Tool and command activity.
- Results, errors, and warnings.
- File-related activity and patches.
- Session and agent metadata.
- Available usage information.
- Information that could not be fully interpreted.
- Summaries and statistics derived from imported sessions.

Not every source provides the same information. AgentSession should communicate
meaningful differences in availability or interpretation without requiring all
sources to share an identical format.

## Relationship to evidence

AgentSession should distinguish between:

- Information recorded by a source.
- Information normalized by AgentSession.
- Information summarized or aggregated by AgentSession.
- Conclusions derived by AgentSession.
- Information that is missing, unavailable, or not understood.

These distinctions do not need to be presented identically in every part of the
product, but users should not be misled about the origin or certainty of
important information.

Recorded actions and agent statements are evidence of session activity. They
are not automatically proof that an intended result was achieved.

## Summaries and statistics

AgentSession may provide summaries and statistics across sessions, agents,
models, tools, time periods, or other useful dimensions.

Statistics should be based on available imported information. Missing,
incompatible, or partially understood values should be handled honestly rather
than silently treated as equivalent or complete.

The specific statistics offered are product decisions that may evolve over
time.

## Derived findings

AgentSession may derive findings or conclusions from imported evidence.

Derived findings should be deterministic and explainable. Important findings
should make it possible for the user to understand the evidence on which they
are based.

AgentSession must not present an assumption, estimate, or derived conclusion as
if it were directly recorded by the source. It must not treat an agent's claim
of success as sufficient proof by itself.

The specific findings and outcome models supported by the product may evolve.

## Product boundaries

AgentSession is an observer and explorer. It is not primarily:

- A coding-agent runner or orchestrator.
- A tool for controlling or resuming agent sessions.
- A command-execution environment.
- A repository restoration tool.
- A replacement for Git.
- A cloud collaboration platform.
- An AI-based evaluator.

New capabilities should remain compatible with the product's purpose as a
trustworthy explorer of coding-agent activity.

## Non-negotiable guarantees

AgentSession must:

- Treat source sessions and inspected repositories as read-only.
- Avoid executing commands or actions found in session records.
- Preserve important uncertainty, gaps, and interpretation failures.
- Avoid presenting missing information as known information.
- Avoid presenting attempted or reported actions as confirmed results.
- Keep its normal operation local and offline-capable.
- Avoid requiring accounts, cloud services, or external model access.
- Treat session and repository information as potentially sensitive.
- Keep shared product behavior consistent across its interfaces.

These guarantees apply even as implementation details and product capabilities
evolve.

## What "v0.1 is useful" means

AgentSession v0.1 is useful when a developer can:

- Explore sessions from the initially supported coding agents.
- Find and open a relevant session.
- Review its important activity in a coherent order.
- Search across available session information.
- Inspect common forms of evidence such as messages, commands, tool activity,
  errors, and file-related activity.
- Recognize when information is missing or not fully interpreted.
- Gain useful cross-session insight where comparable data is available.

The experience should be meaningfully easier and faster than manually
inspecting raw coding-agent records.

More advanced analysis, repository correlation, statistics, and presentation
may evolve after the core exploration experience is dependable.
