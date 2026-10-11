# Initial TUI timeline baseline (#87)

Measured on 2026-10-11 before rendering/retention optimization. This records
the starting behavior for [#87](https://github.com/pooya79/AgentSession/issues/87)
and the responsiveness follow-up in the parent
[spec #86](https://github.com/pooya79/AgentSession/issues/86).

## Correctness reproduction

Hermetic model scenarios reproduced both investigation leads before fixing:

- A 100-line sanitized message at 80×24 never reached its end after twelve
  Page Down inputs. Each rendered frame reanchored to the selected header.
- A pending continuation superseded by either `f` (assessments, then Esc) or
  `u` (Unknown inspection) could not be retried. Delivering its late result
  did not release the loading guard because its generation was obsolete.

`reading_test.go` now exercises both sequences, text scrolling versus event
jumps, delayed/out-of-order replies, retry, disclosure, rewrap, continuation,
refresh, changed/removed anchors (including revalidated raw inspections), blank
lines, session-scoped assessment retention, and return through temporary screens.
Assertions use model input/output and the `app.Services` boundary, without
checking private cache structures.

## Machine and method

- Intel Core i3-12100, 4 cores / 8 logical CPUs, Linux x86_64,
  kernel 6.8.0-117-generic.
- Go 1.27.1, default compiler settings, no race instrumentation or profiling.
- Baseline source: `d46efaa` (TUI code identical to `c3aee08`, the task's initial
  HEAD). The new opt-in measurement file was copied into a temporary archive
  of that revision; the executable was compiled before measuring process RSS.
- Terminal: 80×24. No real session directories, repositories, network, locale,
  or external terminal process are used.
- 10,000 events delivered as 200 bounded, source-ordered 50-event model
  responses. Each response is followed by `View()` producing the fitted frame.
  This measures prepared evidence exploration, not adapter/index latency.
- Mix: 7,001 assistant messages and 2,999 commands; 1,000 command disclosures
  expanded. Ordinary content is 288 bytes. Every 100th event has a 10,240-byte
  unbroken line. The final message contains exactly 1 MiB of unbroken text.
- After exploration, each action has 3 warmups and 30 timed samples. Timing
  starts immediately before `Update(key)` and ends after `View().Content` is
  produced. p95 is the 29th sorted sample (nearest rank). Scrolling uses
  Page Down; baseline event navigation uses its then-current Down binding.
  Current event navigation uses `]`.
- Memory: Go `runtime.MemStats` across exploration, plus `/usr/bin/time -v`
  around the precompiled test binary. Heap at the end includes uncollected
  garbage; cumulative allocations are not retained memory.

## Initial measurements

The precompiled-binary run produced:

| Interaction | Index status inactive | Index status active |
| --- | ---: | ---: |
| Page scroll, p95 | 142.42 ms | 139.63 ms |
| Event navigation, p95 | 243.41 ms | 251.99 ms |

Exploration took 17.59 s and allocated 4,514,775,384 bytes across 24,797,993
allocations. At the end, Go reported 28,429,160 heap bytes and 53,673,984 heap
reservation bytes. Peak resident memory for the test process was 63,132 KiB.

An earlier `go test` invocation recorded 167.08 ms / 228.33 ms inactive p95s
and 141.69 ms / 228.31 ms active-status p95s; exploration allocated
4,514,132,336 bytes. Its process-tree maximum RSS included compilation and is
not used as the application memory baseline. The variation is a reason to
keep these machine-sensitive measurements outside deterministic assertions.

## Reproduction

From a checkout containing the measurement file:

```sh
mkdir -p /tmp/agentsession-87-base
git archive d46efaa | tar -x -C /tmp/agentsession-87-base
cp internal/tui/timeline_performance_test.go /tmp/agentsession-87-base/internal/tui/
cd /tmp/agentsession-87-base
GOCACHE=/tmp/agentsession-go-cache go test -c -o /tmp/agentsession-87-base.test ./internal/tui
AGENTSESSION_MEASURE_TIMELINE=1 AGENTSESSION_BASELINE_KEYS=1 \
  /usr/bin/time -v /tmp/agentsession-87-base.test \
  -test.run '^TestTimelinePerformanceWorkload$' -test.count=1 -test.v
```

To measure the current implementation, compile the current checkout and omit
`AGENTSESSION_BASELINE_KEYS`.

## Limits and follow-up

These are initial measurements, not a claim that the <100 ms p95 target is met.
The active-index samples change the visible application status only: they do
not include contention from a concurrent importer. The old scrolling defect
also means baseline scroll inputs produced a frame without dependable forward
movement. Terminal transport/paint time is outside this model-to-frame method.

Bounded retention, separate lightweight-state accounting, real active-indexing
contention, eviction/reload measurements, and before/after responsiveness
acceptance remain follow-up work under the parent spec. This correctness slice
still retains explored timeline content, and refresh stages the explored range
through bounded service reads before replacing it. It does not establish a
process memory ceiling or silently truncate large evidence.
