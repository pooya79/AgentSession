# Issue tracker: GitHub

Issues and specs live in GitHub Issues for `pooya79/AgentSession`.
Use the `gh` CLI from this clone, or pass `--repo pooya79/AgentSession`.

## Operations

- Create: `gh issue create --title "..." --body-file <file>`
- Read: `gh issue view <number> --json number,title,body,labels,comments`
- List: `gh issue list --state open --json number,title,body,labels,assignees`
- Comment: `gh issue comment <number> --body-file <file>`
- Label: `gh issue edit <number> --add-label "..."` or `--remove-label "..."`
- Assign: `gh issue edit <number> --add-assignee @me`
- Close: `gh issue close <number>`

Write multiline bodies to a temporary file and pass `--body-file`.

When a skill says "publish to the issue tracker", create a GitHub issue.
When it says "fetch the relevant ticket", read the corresponding issue.

## Pull requests as a triage surface

**PRs as a request surface: no.**

## Wayfinding operations

- Map: one issue labelled `wayfinder:map`, containing Notes,
  Decisions-so-far, and Fog.
- Children: link tickets as native sub-issues using `gh api`.
  If unavailable, use a task list in the map and `Part of #<map>`
  in each child.
- Types: `wayfinder:research`, `wayfinder:prototype`,
  `wayfinder:grilling`, or `wayfinder:task`.
- Blocking: use native issue dependencies through `gh api`.
  If unavailable, record `Blocked by: #<number>` in the child.
- Frontier: choose the first open, unassigned child in map order
  whose blockers are all closed.
- Claim: assign the ticket to the driving developer.
- Resolve: comment with the answer, close the ticket, and append
  a concise finding and ticket link to the map's Decisions-so-far.

Check installed CLI help and API support before using native relationship
operations. Create any wayfinding labels needed when that workflow is used.
