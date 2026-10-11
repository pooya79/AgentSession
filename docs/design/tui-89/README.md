# Production terminal comparison (#89)

These captures record the production Bubble Tea TUI and shared application
services in a real pseudo-terminal. The review-only entry point injects a
fresh temporary home/discovery root and index, imports the invented fixtures,
and runs `tui.Run`. It does not read the user's session directories or modify
fixture files. There are no service stubs or substituted renderers in the app.

`workflow-{dark,light,mono}.cast` contains the PTY output, timed keyboard input,
and resize events. The PNGs and fixed-cell text frames are replays of those
bytes using pyte, with a small CSI S/T scrolling extension in `capture.py`.
They are terminal captures, not browser mockups or photographs of a terminal
window. The PNG font is DejaVu Sans Mono at 14 px; cells are 9×19 px.

## Compare with the accepted #88 previews

| Accepted example | Production capture (dark / light / color-free) | Result |
| --- | --- | --- |
| Wide list + useful preview | [dark](sessions-120x32-dark.png) / [light](sessions-120x32-light.png) / [mono](sessions-120x32-mono.png) | Selection, preview, evidence coverage and counts remain visible. |
| Spacious timeline, inline Markdown/code | [dark](timeline-80x24-dark.png) / [light](timeline-80x24-light.png) / [mono](timeline-80x24-mono.png) | One full-width reader; 20 reading rows at 80×24 absent extra diagnostic/error rows. |
| Long-message text scrolling | [dark](reading-80x24-dark.png) / [light](reading-80x24-light.png) / [mono](reading-80x24-mono.png) | Page Down advances text while the action target remains on the original event. |
| Expanded recorded failure | [dark](evidence-80x24-dark.png) / [light](evidence-80x24-light.png) / [mono](evidence-80x24-mono.png) | Explicit failure/exit label, indented output, `-` disclosure and independent target. |
| Wrapped normalized inspection | [dark](inspection-wrapped-80x24-dark.png) / [light](inspection-wrapped-80x24-light.png) / [mono](inspection-wrapped-80x24-mono.png) | `d` reads normalized evidence; long output wraps and envelope metadata remains reachable. |
| Unwrapped inspection with panning | [dark](inspection-80x24-dark.png) / [light](inspection-80x24-light.png) / [mono](inspection-80x24-mono.png) | `w`, Left/Right and the column indicator expose the original long-line suffix. The event/result header remains visible while panning. |
| Unknown evidence | [dark](unknown-80x24-dark.png) / [light](unknown-80x24-light.png) / [mono](unknown-80x24-mono.png) | Interpretation gap is explicit; raw content stays behind `u`. |
| Compact single main view | [dark](compact-54x18-dark.png) / [light](compact-54x18-light.png) / [mono](compact-54x18-mono.png) | One reader, short controls, off-screen action target retained. |
| Smallest supported navigation | [dark](minimum-40x12-dark.png) / [light](minimum-40x12-light.png) / [mono](minimum-40x12-mono.png) | Scroll/event jump/help remain usable; no second pane. |
| Insufficient size | [dark](resize-32x10-dark.png) / [light](resize-32x10-light.png) / [mono](resize-32x10-mono.png) | Explicit current size, minimum 40×12 and quit control. |

The 110-column preview breakpoint was checked at 110×24 in the model/output
tests; captures use 120×32. At 80×24 and above, full contextual controls fit. Wide but short terminals
(e.g. 120×18 and 120×12) also use the single compact main view.
Below 80 columns or 24 rows, shorter contextual help keeps `?` visible. At
40×12, the primary workflow is still reachable through scrolling and help.
Below either dimension the UI displays the resize state. These are validated
layout choices for this rollout, replacing the proposed thresholds in #88.

The screenshots use the actual terminal background response for dark and light.
The mono run sets `NO_COLOR=1` and has no foreground/background color sequences in its recorded output
(bold and reset sequences remain).
Focus, disclosure, failures, unavailable results and Unknown inspection remain
textual. Mouse capture is disabled in the production view; text selection is
left to the host terminal.

## Differences from the illustrative preview

- Shared session summaries expose a recorded preview/summary, counts, coverage
  and projection readiness. They do not expose repository, branch or latest
  event text. The preview uses the available summary rather than inventing
  those fields or adding TUI-specific service logic. Outcome remains explicitly
  `Unknown (not assessed)`; canonical completeness is not an outcome.
- Headings use `§` and code uses a `Code`/language label, preserving structure
  without requiring color. Links and unsupported Markdown remain literal text.
- Technical headings use canonical source sequence numbers and `+`/`-`.
  At 80 columns the footer also shows an abbreviated stable ID; compact
  terminals use the sequence number. Inspection exposes the full event ID.
- The failure fixture records separate invocation-start and completion events.
  The start has `RESULT UNAVAILABLE`, the completed failure has its exit status,
  and the later successful build stays separate. No test recovery is inferred.
- There are no repository/branch placeholders or four large metric boxes.
  Library counts use one row at baseline and two rows in narrow terminals.

Visual inspection compared all three profiles, including the long-line suffix
and minimum-size frames. Hanging list indentation and retained inspection
context were corrected during that comparison. Review also added regressions
for wrapping/panning within one physical line, wide-but-short single-pane
behavior, and visible inspection refresh failures while retaining evidence. This records implementation
comparison, not a new product-owner approval or performance benchmark. The
responsiveness/retention work and the other-screen rollout remain separate
parent-spec tickets.

## Reproduce

On Linux, from the repository root, with Go 1.26 and review-only Python
packages `pexpect`, `pyte==0.8.2` and Pillow available:

```bash
go build -o /tmp/agentsession-89-capture ./docs/design/tui-89/_capture
python3 docs/design/tui-89/capture.py /tmp/agentsession-89-capture
```

The script asserts representative captured content and verifies that fixture
bytes are unchanged. It writes only these review files and a temporary index.
No Python package or capture code is required by the shipped executable.
