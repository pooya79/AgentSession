# Search terminal captures (#91)

These are actual PTY captures of the production TUI and application services,
compared with the [accepted #88 search direction](../tui-review-88.md).
The existing review runner imports the invented #89 fixtures into a temporary
index, isolates discovery from the real home, and verifies fixture bytes remain
unchanged. No renderer or service is substituted. Python dependencies are only
for review; the application remains a single offline executable.

| Interaction | Dark | Light | Color-free |
| --- | --- | --- | --- |
| Cursor insertion, 120×32 | [frame](editing-120x32-dark.png) | [frame](editing-120x32-light.png) | [frame](editing-120x32-mono.png) |
| Paste with `q/?[]`, 120×32 | [frame](paste-120x32-dark.png) | [frame](paste-120x32-light.png) | [frame](paste-120x32-mono.png) |
| Readable results, 120×32 | [frame](results-120x32-dark.png) | [frame](results-120x32-light.png) | [frame](results-120x32-mono.png) |
| Baseline results, 80×24 | [frame](results-80x24-dark.png) | [frame](results-80x24-light.png) | [frame](results-80x24-mono.png) |
| Compact results, 54×18 | [frame](results-54x18-dark.png) | [frame](results-54x18-light.png) | [frame](results-54x18-mono.png) |
| Minimum results, 40×12 | [frame](results-40x12-dark.png) | [frame](results-40x12-light.png) | [frame](results-40x12-mono.png) |
| Open matching session, 80×24 | [frame](opened-80x24-dark.png) | [frame](opened-80x24-light.png) | [frame](opened-80x24-mono.png) |
| Restore query/selection, 80×24 | [frame](returned-80x24-dark.png) | [frame](returned-80x24-light.png) | [frame](returned-80x24-mono.png) |
| Empty results, 80×24 | [frame](empty-80x24-dark.png) | [frame](empty-80x24-light.png) | [frame](empty-80x24-mono.png) |
| Shared-service validation error, 80×24 | [frame](error-80x24-dark.png) | [frame](error-80x24-light.png) | [frame](error-80x24-mono.png) |
| Insufficient size while editing, 32×10 | [frame](resize-32x10-dark.png) | [frame](resize-32x10-light.png) | [frame](resize-32x10-mono.png) |

Each PNG has a same-named fixed-cell `.txt` frame; `workflow-*.cast` records
terminal output, keyboard input and resizes. The captures use the same terminal
emulator and font as #89. The mono run asserts no foreground/background color
sequences. Focus, selection, coverage and retry controls also use text.
Mouse capture remains disabled, leaving host-terminal selection available.

The reviewed real-input behavior is implemented with insertion/deletion at a
Unicode rune cursor and a horizontally scrolling query line. Home/End and
Ctrl-A/Ctrl-E reach query boundaries. Bracketed paste stays text, including
feature bindings; pasted newlines/tabs become spaces. Esc stops editing before
returning. Below 40×12 the resize screen advertises Ctrl-C while query input owns
`q`. Result cards reuse shared display summaries and existing search/filter
semantics. Enter opens the result's session timeline, as specified by ADR-018;
the shared result contract does not expose a ranked matching-event ID.

Model/service tests additionally exercise delayed and superseded replies,
cancellation, failed refresh with retained previous results, retry, pagination,
stable selection after reordered refresh, removed selections/sessions, hostile
content, Unicode cursor visibility, partial/unavailable coverage and resizes.
Unavailable coverage does not claim there were no matches. Existing reading
tests cover timeline position/disclosure restoration through search and result
navigation; existing TUI/web shared-service parity tests remain in place.
These deterministic tests complement the captures; loading, partial and
unavailable responses are not claimed as captured live projection workloads.
Visual inspection checks the accepted direction, not a new owner approval or a
performance measurement.

From the repository root, with `pexpect`, `pyte==0.8.2` and Pillow available:

```bash
go build -o /tmp/agentsession-91-capture ./docs/design/tui-89/_capture
python3 docs/design/tui-89/capture.py /tmp/agentsession-91-capture --search
```
