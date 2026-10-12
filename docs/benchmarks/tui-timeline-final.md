# Final combined TUI measurements (#93)

Measured on 2026-10-12 for [#93](https://github.com/pooya79/AgentSession/issues/93).
This rechecks [#90's before/after method](tui-timeline-bounded.md) against the
combined TUI at `67d8885`. Acceptance work adds tests and review tooling; it does
not change production rendering or services. Full outputs are retained as
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
  batches/7,600 records, final completed 13 batches/1,300 records. Batch counts
  vary with interaction time.
- Before: archive `118b7889`, copy the unchanged
  `timeline_measurement_test.go` harness into it, then compile/run separately.
  Final: this checkout. Timing binaries run sequentially, before full/race
  checks. The baseline rerun overlapped the lightweight PTY capture process;
  no competing benchmark or race/full test suite ran during timing.

## Responsiveness

All four final p95s satisfy the **below 100 ms** reference-workload target.

| Interaction-to-frame p95 | Before idle | Final idle | Before indexing | Final indexing |
| --- | ---: | ---: | ---: | ---: |
| Page scroll | 208.82 ms | 0.27 ms | 177.60 ms | 0.38 ms |
| Event navigation | 279.41 ms | 16.38 ms | 294.47 ms | 20.66 ms |

The previous #90 report recorded final p95s of 0.19/0.27 ms scrolling and
15.81/18.85 ms navigation. These reruns vary with scheduling, garbage collection
and concurrent importer progress, while remaining below the target. The baseline
also varies from its earlier 141.95/154.49 ms and 246.29/266.64 ms readings.
These are machine-sensitive observations, not deterministic-test assertions or
guarantees for arbitrary hardware, terminal paint, cold reads or larger events.

| Exploration/timing process | Before | Final |
| --- | ---: | ---: |
| Exploration time | 49.46 s | 3.66 s |
| Exploration allocation bytes | 15,191,701,008 | 467,269,248 |
| Exploration allocations | 88,056,897 | 1,252,102 |
| Heap after exploration and GC | 18,685,856 B | 6,856,760 B |
| Resident payloads after exploration | 10,000 | 150 |
| Total allocation bytes, including timing/indexing | 33,552,603,544 | 1,240,639,592 |
| Total allocation count, including timing/indexing | 198,763,813 | 6,568,318 |
| Peak process RSS, including fixture import/setup | 95,480 KiB | 55,620 KiB |

## Retention, eviction and large-event exception

The separate memory run explores the same history, evicts the large final event,
revisits every source-order target, reloads the 1 MiB event, and renders a separate
3 MiB message. The opt-in harness now checks the visible target at every step of
the 10,000-event revisit, so a skipped or repeated target fails the run. Existing
deterministic tests exercise backward/forward overlap and changed/missing anchors
after reimport. This measurement does not claim a snapshot across source changes.

| Stage | Visited metadata | Resident events | Accounted payload B | Accounted rendered B | Metadata estimate B | Heap after GC B |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Explore 2,500 | 2,500 | 150 | 71,365 | 99,515 | 606,743 | 1,713,184 |
| Explore 5,000 | 5,000 | 150 | 71,365 | 99,515 | 1,213,493 | 2,545,240 |
| Explore 7,500 | 7,500 | 150 | 71,365 | 99,515 | 1,820,243 | 3,627,960 |
| Explore 10,000, including 1 MiB event | 10,000 | 150 | 1,119,603 | 1,685,878 | 2,426,883 | 6,848,640 |
| Evict large event, revisit start | 10,000 | 26 | 20,757 | 30,696 | 2,426,883 | 4,019,440 |
| Revisit 2,500 | 10,000 | 150 | 81,605 | 115,195 | 2,426,883 | 4,231,264 |
| Revisit 5,000 | 10,000 | 150 | 81,605 | 115,195 | 2,426,883 | 4,256,192 |
| Revisit 7,500 | 10,000 | 150 | 81,605 | 115,195 | 2,426,883 | 4,273,568 |
| Reload 1 MiB event | 10,000 | 150 | 1,119,603 | 1,685,878 | 2,426,883 | 6,932,664 |
| Separate 3 MiB event + boundary neighbor | 3 | 2 | 3,145,917 | 4,759,271 | 527 | 8,613,352 |

Exploration/revisit took 22.64 s and allocated 3,449,953,768 bytes across
19,687,029 allocations through the 1 MiB reload. Including the separate oversized
event, totals reached 3,493,114,904 bytes/19,688,892 allocations. Peak RSS was
53,008 KiB. A 5 ms sampler observed peak heap allocations of 15,230,360 B during
exploration, 15,843,168 B during eviction/revisit, and 22,621,432 B during the
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
