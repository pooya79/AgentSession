# Combined TUI acceptance (#93)

This validates the final combined implementation of [spec #86](https://github.com/pooya79/AgentSession/issues/86).
The production TUI runs against shared application services and a temporary
SQLite index containing the invented #89 fixtures. No developer session
directories or real repositories are inspected. Source fixture hashes are
checked after the PTY journey; mouse capture enable sequences are rejected.

## Product-owner review

**Accepted on 2026-10-12.** The owner accepted the second simplification with
“Accept this version” in response to the final review request covering the actual
screens, sizes, color profiles and live terminal-selection check. The accepted
production code is `b8fe9e7`; its final captures and check record are at `c4c08c2`.

The owner rejected the initial combined UI as
“messy”, with too much text and unclear navigation, and requested simplification
across all screens. The owner then rejected the first simplification as still
needing more simplification. This second revision removes repeated titles,
previews, focus labels and routine background-success status; uses two-row
session entries and plain speaker labels; gives command output the full line
width; lists each supporting event once; and reduces visible controls to primary
actions. Derived views use readable names and indexing starts with progress
instead of a source inventory. The revision puts recorded content first, removes internal
identities from the reader target, shortens controls, makes help specific to the
current screen, and moves metadata, full explanations and operational counters
behind `v`. All evidence remains reachable; failures and partial availability
still appear in the default views. Fresh captures below replace the rejected
frames. The accepted [#88 previews](../tui-review-88.md) establish design
direction. Earlier #89/#91/#92 implementation inspections do not establish
final product-owner acceptance. Fresh frames and input/output/resize recordings
below document the combined production journey; automated captures and an
agent's visual inspection cannot supply the owner's verdict.

The final review provided the [actual-screen gallery](gallery.html) and
[accepted preview](../tui-review-88.html), covering wide 120×32, baseline 80×24,
compact 54×18, minimum 40×12, and insufficient 32×10 behavior in dark, light,
and color-free profiles. The review request also included the live isolated TUI,
normal terminal text selection and the browse → read → inspect → return journey.
The owner accepted this version without requesting further corrections.

## Reproduce the actual terminal journey

From the repository root:

```bash
go build -o /tmp/agentsession-93-capture ./docs/design/tui-89/_capture
/tmp/agentsession-93-capture
python3 docs/design/tui-89/capture.py /tmp/agentsession-93-capture --acceptance
```

The live binary and capture use production rendering, keyboard handling,
discovery, imports, search, assessments and projections. The runner isolates
home, working directory, settings and indexes in a temporary directory, removed
on exit. The capture requires review-only `pexpect`, `pyte==0.8.2`, and Pillow;
none is an application dependency. `workflow-dark.cast`, `workflow-light.cast`,
and `workflow-mono.cast` contain asciicast v2 input/output and resize events.
Replay with a local asciicast-compatible player; do not upload recordings.

The journey checks session previews, continuous long-message reading, help and
return, technical disclosure, wrapped/unwrapped inspection and horizontal
panning, cursor editing and bracketed paste, search result/evidence/return,
assessment reference/evidence/return, explicit Unknown inspection, projection
rebuild, refresh, resize and return to sessions, then indexing observation.
Every main/supporting screen has fixed-cell text and PNG variants across all
four usable dimensions and three profiles, plus the insufficient-size frame.

## Deterministic integration coverage

| Requirement | Evidence at the agreed Model/services seam |
| --- | --- |
| Combined real-index journey, safe source reads, mouse mode | `TestCompleteTUIJourneyWithTemporaryIndex` |
| Concise defaults, explicit metadata/explanation disclosure, current-screen help | `disclosure_test.go` |
| Session preview/selection, compact focus, Markdown, long lines, terminal sanitization | `browsing_test.go`, `presentation_test.go`, `timeline_cards_test.go` |
| Long-message scrolling, independent event target, disclosure, resize, refresh, changed/missing anchors, temporary-screen return | `reading_test.go` |
| Forward/backward reload, eviction/revisit, overlap reconciliation, large-event access, removed prefetch anchors | `retention_test.go` |
| Delayed/out-of-order replies, cancellation, errors and retry | `reading_test.go`, `retention_test.go`, `timeline_test.go`, `search_test.go`, `supporting_test.go` |
| Cursor editing, shortcut-safe input, paste, search selection/refresh/return | `search_test.go` |
| Details, assessments/references, Unknown inspection, partial/unavailable/error states | `supporting_test.go`, `findings_test.go`, `evidence_view_test.go` |
| Application-owned indexing and projection work, observation and retry | `lifecycle_test.go`, `navigation_test.go`, `projections_test.go` |
| Shared TUI/web evidence, search, assessments, projection, redaction and raw size limits | Existing `services_contract_test.go` in both presentations and `internal/app/apptest` contracts |
| 10,000 events, 1 MiB message, full evicted-history revisit, 3 MiB exception | Opt-in real-index latency/memory harnesses; revisit asserts every available source-order target |

The focused tests exercise response permutations deliberately rather than
depending on scheduler timing. Live captures show completed indexing and actual
projection rebuilds. Delayed/error/partial states remain deterministic-test
evidence, not claimed as live captures. The synthetic benchmark background
writer owns its own temporary source; the product never writes source sessions.

## Measurements, architecture and limits

See the [final latency/memory report](../../benchmarks/tui-timeline-final.md)
for before/after measurements, reference machine, workload, sample method,
allocation/RSS results and limitations. The budgets remain 150 resident events,
2 MiB accounted payload content and 4 MiB accounted rendered content. These are
heavy-retention budgets, not process-memory ceilings. Lightweight metadata grows
with visited history; oversized events and transient work can exceed budgets.
Evicted evidence remains reloadable without silently truncating it.

The implementation retains [ADR-010](../../decisions/010-bounded-evidence-exploration-services.md),
[ADR-019](../../decisions/019-opt-in-inline-timeline-payloads.md)'s payload opt-in,
[ADR-020](../../decisions/020-canonical-summary-categories-and-continuous-web-timeline.md),
and [ADR-022](../../decisions/022-bounded-tui-timeline-retention.md)'s retention
policy. No model, schema, outcome semantics, source adapter, web presentation,
or compatibility layer changes are introduced by this acceptance work.

Known limits remain: reimport cursors are not snapshots; metadata outside reread
intervals may reflect an earlier import; raw inspection is explicitly requested
again after navigation/eviction; arbitrary single-event memory and terminal
transport/paint latency are outside the cache and model-to-frame guarantees.

## Check record

On 2026-10-12, after the second simplification and regression fixes at `b8fe9e7`:

- `GOCACHE=/tmp/agentsession-go-cache make fmt` passed: templ formatting,
  generation and gofmt; no generated output changes.
- `GOCACHE=/tmp/agentsession-go-cache make check` passed: generated-code
  consistency, static analysis and the complete test suite, including shared
  TUI/web application-service contracts.
- `GOCACHE=/tmp/agentsession-go-cache go test -race ./...` passed
  (TUI: 77.282 s).
- `CGO_ENABLED=0 GOCACHE=/tmp/agentsession-go-cache go build` for
  `./cmd/agentsession` passed; production capture runner build passed.
- The full PTY journey passed in all three profiles; source fixture hashes were
  unchanged, no mouse-capture enable sequences appeared, and color-free output
  contained no color controls. All 168 PNG/text pairs, three recordings and
  local gallery links exist. The gallery was opened in the in-app browser and
  the screen, size and profile selectors, previous/next navigation and image
  loading were verified. The gallery presents one screen at a time.
- Both final opt-in real-index measurement runs passed, sequentially after
  capture/full/race checks. All four p95s remain below 100 ms; the memory run
  revisited every source-order target and retained the documented exceptions.
- Two independent code-review agents compared `67d8885...b8fe9e7`:
  Standards: no findings. Spec: no implementation defects or scope creep;
  one completion finding at review time, final owner visual acceptance,
  subsequently resolved by the owner’s explicit acceptance above.
  The review found command-whitespace and clipped-preview regressions; both
  were corrected with failing-then-passing tests before the final review.

**Final owner verdict: accepted.** This is the owner’s explicit verdict,
separate from the automated checks. The documented memory and measurement
limitations remain applicable.
