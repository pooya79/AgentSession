# Supporting-screen terminal captures (#92)

These are actual PTY captures of the production TUI and shared application
services, compared with the [accepted #88 direction](../tui-review-88.md).
The existing review runner imports invented #89 fixtures into a temporary index,
isolates discovery from the real home, and verifies fixture bytes remain unchanged.
The application remains offline, read-only toward sources, and a single binary.

| Screen | Wide 120×32 | Baseline 80×24 | Compact 54×18 | Minimum 40×12 |
| --- | --- | --- | --- | --- |
| Event details | [frame](details-120x32-dark.png) | [frame](details-80x24-dark.png) | [frame](details-54x18-dark.png) | [frame](details-40x12-dark.png) |
| Assessments | [frame](assessments-120x32-light.png) | [frame](assessments-80x24-light.png) | [frame](assessments-54x18-light.png) | [frame](assessments-40x12-light.png) |
| Projections | [frame](projections-120x32-mono.png) | [frame](projections-80x24-mono.png) | [frame](projections-54x18-mono.png) | [frame](projections-40x12-mono.png) |
| Indexing | [frame](indexing-120x32-dark.png) | [frame](indexing-80x24-dark.png) | [frame](indexing-54x18-dark.png) | [frame](indexing-40x12-dark.png) |

Every frame has dark, light, and color-free PNG and fixed-cell text variants.
The `workflow-*.cast` files record input, output and resizing. The capture asserts
no color sequences in the mono run. Mouse capture remains disabled, preserving
normal host-terminal selection.

Additional captured interactions:

- [Scrollable help](help-80x24-mono.png) and [last help controls](help-end-80x24-mono.png).
- [Selected supporting reference](supporting-reference-80x24-light.png), its
  [timeline context](supporting-timeline-80x24-light.png), and
  [return to the assessment](assessment-return-80x24-light.png).
- [A real search-projection rebuild](projection-rebuild-80x24-dark.png), verified
  by the incremented attempt count, and [disabled rebuild-all scope](projection-all-disabled-80x24-dark.png).
- [Unwrapped inspection with horizontal panning](unwrapped-80x24-mono.png).
- [Explicit Unknown inspection prompt](unknown-explicit-80x24-light.png) and
  [bounded service result](unknown-redacted-80x24-light.png), including byte,
  redaction and truncation counts. This invented record requires zero redactions;
  secret removal and size bounds remain covered by shared-service parity tests.
- [Resize message at 32×10](resize-32x10-mono.png).

Visual comparison retains the accepted full-width reading area, compact footers,
textual focus, restrained styling, wrapped evidence and optional horizontal
inspection. Recorded command failures remain explicitly labelled; AgentSession
import diagnostics and projection readiness use separate wording. Supporting
screens retain more envelope and operational metadata than the illustrative
preview. That content scrolls instead of disappearing below a clipped page.
At 40×12 the contextual footer prioritizes actions, return and help; full controls
remain reachable in `?`. Dark/light contrast and color-free focus/status labels
were inspected directly in the rendered PNGs. This is implementation validation,
not a new product-owner approval or a performance measurement.

Model/app.Services tests also cover delayed and superseded responses,
cancellation and retry, long diagnostics and reference lists, selected projection
scope across refresh and nested search visits, supporting-timeline return context,
hostile terminal content, explicit raw clearing, missing normalized payloads,
partial/unavailable evidence and all usable sizes. Existing application/TUI/web
contracts continue to verify findings, projections and Unknown inspection parity.
Live captures show completed indexing and ready/unimplemented projections;
injected loading, failure and stale states are verified by deterministic tests,
not claimed as live captured workloads. Existing tests retain rebuild-all
confirmation and application-owned indexing/projection lifecycle coverage.

Reproduce from the repository root with `pexpect`, `pyte==0.8.2` and Pillow:

```bash
go build -o /tmp/agentsession-92-capture ./docs/design/tui-89/_capture
python3 docs/design/tui-89/capture.py /tmp/agentsession-92-capture --supporting
```

These Python packages are review tools only and are not application dependencies.
