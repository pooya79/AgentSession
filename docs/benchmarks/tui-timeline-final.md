# Final combined TUI measurements (#93)

Measured on 2026-10-12 for [#93](https://github.com/pooya79/AgentSession/issues/93).
This rechecks [#90's before/after method](tui-timeline-bounded.md) against the
combined TUI renderer at `7d268bb`, after the owner-requested simplification.
Acceptance work adds tests and review tooling and simplifies presentation; shared
services and retention policies remain unchanged. Full outputs are retained as
[before](tui-93/before.txt), [final latency](tui-93/final-latency.txt), and
[final memory](tui-93/final-memory.txt).

## Machine and workload

- Intel Core i3-12100, four physical cores/eight logical CPUs; Linux x86_64,
  kernel 6.8.0-117-generic; Go 1.27.1. This is the same reference machine as #90.
- Default `go test -c` build settings, no race instrumentation, profiling or
  custom linker flags. Compile before timing with `/usr/bin/time -v`.
- 80×24; each sample begins immediately before `Update(key)` and ends after
  `View().Content`. External terminal transport/paint is excluded. Loaded
  interactions are timed; cold reads have separate correctness coverage.
- Real temporary SQLite index and synthetic Codex source: 10,000 events,
  7,001 assistant messages, 2,999 tool calls, 1,000 expanded technical events.
  Ordinary text is 303 bytes; every 100th event adds a 10,240-byte unbroken
  line. The final message is exactly 1 MiB of unbroken text. Exploration drives
  50-event service continuations and produces visible frames.
- Three warmups and 100 samples for each action/condition. Page Down/Page Up
  alternate within the large message; `[`/`]` alternate between it and its
  neighbor. p95 uses nearest-rank sample 95, after sorting.
- Active indexing continuously appends 100 invented records to a second
  temporary source and performs actual discovery/import, SQLite writes and
  projections in the same runtime. Sampling waits for one completed batch;
  work continues without sleeps until sampling ends. Before completed 76
  batches/7,600 records, final completed 12 batches/1,200 records. Batch counts
  vary with interaction time.
- Before: archive `118b7889`, copy the unchanged
  `timeline_measurement_test.go` harness into it, then compile/run separately.
  Final: this checkout. Timing binaries run sequentially; final runs start after capture and full/race
  checks complete. The baseline rerun overlapped the lightweight PTY capture process;
  no competing benchmark or race/full test suite ran during timing.

## Responsiveness

All four final p95s satisfy the **below 100 ms** reference-workload target.

| Interaction-to-frame p95 | Before idle | Final idle | Before indexing | Final indexing |
| --- | ---: | ---: | ---: | ---: |
| Page scroll | 208.82 ms | 0.27 ms | 177.60 ms | 0.28 ms |
| Event navigation | 279.41 ms | 18.16 ms | 294.47 ms | 21.27 ms |

The previous #90 report recorded final p95s of 0.19/0.27 ms scrolling and
15.81/18.85 ms navigation. These reruns vary with scheduling, garbage collection
and concurrent importer progress, while remaining below the target. The baseline
also varies from its earlier 141.95/154.49 ms and 246.29/266.64 ms readings.
These are machine-sensitive observations, not deterministic-test assertions or
guarantees for arbitrary hardware, terminal paint, cold reads or larger events.

| Exploration/timing process | Before | Final |
| --- | ---: | ---: |
| Exploration time | 49.46 s | 3.84 s |
| Exploration allocation bytes | 15,191,701,008 | 466,607,760 |
| Exploration allocations | 88,056,897 | 1,240,270 |
| Heap after exploration and GC | 18,685,856 B | 6,855,264 B |
| Resident payloads after exploration | 10,000 | 150 |
| Total allocation bytes, including timing/indexing | 33,552,603,544 | 1,184,226,912 |
| Total allocation count, including timing/indexing | 198,763,813 | 6,152,991 |
| Peak process RSS, including fixture import/setup | 95,480 KiB | 53,468 KiB |

## Retention, eviction and large-event exception

The separate memory run explores the same history, evicts the large final event,
revisits every source-order target, reloads the 1 MiB event, and renders a separate
3 MiB message. The opt-in harness now checks the visible target at every step of
the 10,000-event revisit, so a skipped or repeated target fails the run. Existing
deterministic tests exercise backward/forward overlap and changed/missing anchors
after reimport. This measurement does not claim a snapshot across source changes.

| Stage | Visited metadata | Resident events | Accounted payload B | Accounted rendered B | Metadata estimate B | Heap after GC B |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Explore 2,500 | 2,500 | 150 | 71,365 | 98,555 | 606,743 | 1,730,544 |
| Explore 5,000 | 5,000 | 150 | 71,365 | 98,555 | 1,213,493 | 2,537,824 |
| Explore 7,500 | 7,500 | 150 | 71,365 | 98,555 | 1,820,243 | 3,542,816 |
| Explore 10,000, including 1 MiB event | 10,000 | 150 | 1,119,603 | 1,684,918 | 2,426,883 | 6,845,832 |
| Evict large event, revisit start | 10,000 | 26 | 20,757 | 30,573 | 2,426,883 | 4,015,888 |
| Revisit 2,500 | 10,000 | 150 | 81,605 | 114,235 | 2,426,883 | 4,231,320 |
| Revisit 5,000 | 10,000 | 150 | 81,605 | 114,235 | 2,426,883 | 4,262,200 |
| Revisit 7,500 | 10,000 | 150 | 81,605 | 114,235 | 2,426,883 | 4,282,744 |
| Reload 1 MiB event | 10,000 | 150 | 1,119,603 | 1,684,918 | 2,426,883 | 6,939,824 |
| Separate 3 MiB event + boundary neighbor | 3 | 2 | 3,145,917 | 4,759,271 | 527 | 8,621,464 |

Exploration/revisit took 21.96 s and allocated 3,438,326,032 bytes across
19,610,232 allocations through the 1 MiB reload. Including the separate oversized
event, totals reached 3,481,486,688 bytes/19,612,090 allocations. Peak RSS was
57,016 KiB. A 5 ms sampler observed peak heap allocations of 13,405,512 B during
exploration, 16,641,112 B during eviction/revisit, and 22,629,064 B during the
oversized-event phase. Sampling can miss brief peaks and includes uncollected
garbage; process RSS includes fixture import/setup and infrastructure.

Ordinary heavy retention remains bounded by **150 events, 2 MiB accounted payload
content and 4 MiB accounted rendered content**. Lightweight metadata still grows
with visited history. Metadata estimates exclude map buckets, allocator rounding
and shared-string aliasing. Accounting is not a heap census; cumulative allocation
is not retained memory. The memory run includes its sampler and visible-target
assertions; neither is part of latency timing.

An oversized reading event remains complete and can retain an adjacent boundary
neighbor to preserve access while crossing it. The 3 MiB row exceeds both byte
budgets deliberately. Decode/format buffers, bounded responses, refresh staging,
independent inspection content and SQLite can further raise process memory.
Nothing is silently truncated to meet a cache budget. See
[ADR-022](../decisions/022-bounded-tui-timeline-retention.md) for ownership and
[the #90 report](tui-timeline-bounded.md) for accounting definitions.

## Reproduction

From the repository root, with a writable Go build cache:

```bash
GOCACHE=/tmp/agentsession-go-cache go test -c -o /tmp/agentsession-93.test ./internal/tui
AGENTSESSION_MEASURE_TIMELINE=1 /usr/bin/time -v /tmp/agentsession-93.test \
  -test.run '^TestTimelineResponsivenessMeasurement$' -test.count=1 -test.v
AGENTSESSION_MEASURE_TIMELINE=1 /usr/bin/time -v /tmp/agentsession-93.test \
  -test.run '^TestTimelineRetentionMeasurement$' -test.count=1 -test.v
```

For before, archive `118b7889` into a fresh temporary directory, copy only
`internal/tui/timeline_measurement_test.go`, compile its TUI test binary, then
run `TestTimelineResponsivenessMeasurement`. Keep timing processes sequential
and separate from race/full checks. Tests and capture tools operate offline on
temporary indexes and invented sources; the benchmark writer alone appends its
own synthetic background fixture.
