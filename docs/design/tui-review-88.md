# Terminal design review (#88)

Status: **Accepted by the product owner on 2026-10-11.**

This is the review artifact for [#88](https://github.com/pooya79/AgentSession/issues/88),
within [spec #86](https://github.com/pooya79/AgentSession/issues/86).
Open [the offline preview](tui-review-88.html) directly in a browser. It has no
external assets, network calls, persistence, real session data, or dependencies.
It does not change the shipped application. The fixture is invented, including
all commands, paths, IDs, results, and counts.

## Review route

1. Start at **Sessions, 120×32, Dark**. Review list density and the useful
   selected-session preview. Open the selected session with Enter.
2. Choose **Timeline, 80×24**. Focus the terminal using Tab or a click. Scroll
   with arrows/j/k and Page Down through the long assistant message. The action
   target remains #104 until you use `[` or `]`. Enter expands its command.
3. Choose **Expanded evidence**. Compare the recorded failed command with the
   later build success: the latter does not establish test recovery. Use `d`
   to inspect the selected event, then `w` to toggle wrapping. Pan with Left/Right
   in unwrapped inspection. Esc returns to the same timeline reading position.
4. Choose **Search**. Edit in the actual text field with cursor movement, deletion,
   and paste. Try `q / ? [ ]`: these stay text. Enter shows illustrative results;
   it does not execute a search. Esc exits editing, then returns to the timeline.
5. Compare **Loading history**, **History error**, **Import error**,
   **Projection error**, **Unavailable evidence**, **Unknown evidence**, and
   **Empty sessions**. Loading/error screens retain already-loaded evidence.
   Retry is an illustration returning to the prepared timeline, not actual I/O.
6. Repeat in **Light** and **Color-free**, especially expanded evidence and
   search. The `>` target marker, `[FOCUS]`, explicit state labels, and `+`/`-`
   disclosures must remain readable without color.
7. Compare **54×18** (one main view, no competing preview pane) and **32×10**
   (explicit resize message). At 80×24 the primary workflow remains available.

The browser frame uses a fixed monospace grid with exact column/row counts;
horizontal browser overflow preserves its dimensions instead of scaling text.
Native browser text selection remains available. Browser controls outside the
terminal are review tools, not proposed TUI widgets. Browser text input is an
editing demonstration, not a terminal input implementation. This is a design
preview, not an ANSI emulator, performance benchmark, or completed TUI rollout.
Use **Download frame (.txt)** for the exact visible, color-free terminal frame.
The files contain plain printable ASCII and newlines; no terminal controls.

## Accepted direction and breakpoints

- At 110 columns or wider: session list with preview. Opening a session gives
  its timeline the full main area. Metadata occupies two rows; contextual help
  and status occupy two bottom rows. The rest is reading space.
- At 80×24: one main view with full navigation. Selected session metadata is
  available by opening it rather than squeezing a second pane into 80 columns.
- At 40–79 columns, or below 24 rows: one compact main view, shorter contextual
  help, full controls in `?`. At least 40×12 is proposed for useful navigation.
- Below 40 columns or 12 rows: resize message with current/minimum dimensions,
  rather than clipped navigation. These thresholds are review proposals.
- Use headings, blank lines, list bullets, and preserved code indentation.
  Wrap code by default; indent continuation lines. Inspection can disable
  wrapping and exposes column offset plus pan controls. No heavy card borders.
- Keep technical rows collapsed. `>` identifies the action target separately
  from the top visible text; the bottom status names an off-screen target.
  Scrolling does not silently retarget Enter.
- Outcome and evidence coverage stay separate. Missing output is unavailable;
  an assistant claim or successful build does not resolve a failed test.
  Unknown raw inspection stays explicit, bounded, and redacted.

## Interaction contract

| Context | Controls and intended behavior |
| --- | --- |
| Session list | Up/Down or j/k select; Enter opens; `/` search; `i` indexing; `r` rescan |
| Timeline | Arrows/j/k scroll text; PgUp/PgDn scroll a page; `[`/`]` jump events; Enter toggles technical disclosure; Esc returns |
| Inspection | `d` opens normalized details (proposed binding); `w` toggles wrapping (proposed binding); Left/Right pan only when unwrapped; Esc restores timeline context |
| Temporary screens | `/` search; `?` help; Esc restores prior context, including disclosure and reading position |
| Search editing | Left/Right, Home/End, Backspace/Delete edit at cursor; paste inserts text; shortcut characters never trigger navigation; Enter submits; Esc exits editing before returning |
| Other existing features | `f` assessments; `x` projections; `u` explicit bounded/redacted Unknown inspection; `r` refresh/retry; `n`/`p` paged lists; `g`/`G` boundaries; `q` or Ctrl-C quit outside editing |
| Assessments | `[`/`]` select supporting reference; Enter opens timeline context; Esc restores reading context |
| Projection controls | `t` retry; `b` rebuild selected; `a` request rebuild all with confirmation when available; Esc leaves observation without canceling application work |
| Indexing | Esc leaves observation; import work remains application-owned |

Mouse capture stays disabled in the intended terminal implementation. This
preview does not install pointer handlers that intercept text selection.
Browser-reserved shortcuts and Ctrl-C retain browser behavior; preview `q`
shows an explanatory status instead of closing the browser. `f`, `x`, `u`,
and `i` remain documented capabilities; only the named gallery scenes and
reading/search/disclosure/help/inspection controls are demonstrated here.

## Acceptance record

| Review item | Product-owner verdict | Revisions |
| --- | --- | --- |
| Wide session list and preview | Accepted | None requested |
| Long timeline, disclosure, wrapped/unwrapped inspection | Accepted | None requested |
| Search editing and contextual controls | Accepted | None requested |
| Baseline, compact, and insufficient sizes | Accepted | None requested |
| Dark, light, and color-free readability | Accepted | None requested |
| Loading/errors, partial and unavailable evidence | Accepted | None requested |

After the concrete previews and review notes were presented, the product owner
instructed: "push and close issue #88". This records acceptance of the presented
direction; no revisions were requested. Acceptance covers these review examples,
not a completed production rollout or measured performance. Implementation and
responsiveness validation remain work under the parent spec #86.
