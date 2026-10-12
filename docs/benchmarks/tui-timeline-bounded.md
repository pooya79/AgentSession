# Bounded TUI timeline exploration (#90)

Measured on 2026-10-12 for [#90](https://github.com/pooya79/AgentSession/issues/90),
implementing accepted [ADR-022](../decisions/022-bounded-tui-timeline-retention.md).
The shared application API, source ordering, payload opt-in, raw evidence action,
web behavior, schemas, and local/read-only boundaries are unchanged.

## Working set and exceptions

The active timeline retains a contiguous neighborhood around the reading anchor:

- At most **150 resident events**.
- **2 MiB accounted normalized payload content**, including resident raw
  inspection text. Accounting uses the JSON representation plus 64 bytes per
  payload; it is a reproducible content estimate, not a Go heap census.
- **4 MiB accounted rendered content**, including text bytes and 40 bytes per
  row for card/composite string headers and anchor offsets.

These budgets permit roughly three normal 50-event service chunks, allowing
reading and prefetch on either side without a presentation page boundary. Both
byte limits are independent of event count. Short-line content can exhaust the
render budget even when its normalized payload is small.

An oversized reading event is kept in full. While crossing it, one immediate
boundary neighbor can also remain resident so scrolling can enter and leave it.
Consequently one or two adjacent individual events can exceed the byte budgets.
Nothing is silently truncated. Bounded service pages, response decoding, JSON
accounting, formatting, viewport slices, and refresh staging can also allocate
above the retained budgets temporarily. These are **not process memory limits**.

Lightweight visited summaries (IDs, source sequence, kind and summary), payload
fingerprints/sizes, disclosure flags, logical disclosure positions, and cursor
bookkeeping are separate from resident payloads/cards. Their memory grows with
visited history; the measurements below report that growth separately. Summaries
are canonical metadata, not copies of message bodies or rendered cards. Metadata
estimates exclude map bucket overhead, allocator rounding, and shared-string
aliasing; measured heap/RSS include infrastructure and transient allocations.

Eviction removes payloads, rendered cards/row offsets and raw inspection content
outside the window. Nested search/assessment timelines release saved windows;
returning reloads their bounded neighborhoods while retaining logical anchors
and disclosure state. If an evicted raw inspection was the reading anchor, the
UI explains the eviction and requires the explicit `u` action again.

## Reloading and interaction work

Earlier history uses focused reads ending at a known stable event ID. Forward
exploration keeps the existing opaque cursor; revisits use bounded focused reads.
Overlap is deduplicated by stable ID and sorted by source sequence. A focused
response replaces obsolete summaries inside its returned sequence interval;
there is no snapshot promise outside the reread interval. Refresh reloads only
the resident neighborhood, falling back to another nearby known anchor when the
first one disappeared, rather than staging the complete visited history.

Loading is visible above the reading viewport, including earlier reads. Existing
content stays usable during delayed/failed reads. Generations reject stale
results, cancellation releases guards, and `r` retries an evicted selection.
Changed/missing anchors have explicit explanations. Event jumps can cross a
previously unvisited boundary; scrolling preserves the independent action target.

Card formatting is reused across selection changes. Only card/layout changes
reinstall viewport content, and only the active screen's viewport is formatted.
Logical anchor offsets include inter-card blank rows, so one-line scrolling can
cross card boundaries. These changes address measured repeated formatting and
inactive-view work without a generic cache/rendering framework.

## Reference machine and reproducible method

- Intel Core i3-12100, four physical cores/eight logical CPUs; Linux x86_64,
  kernel 6.8.0-117-generic.
- Go 1.27.1; default `go test -c` compiler configuration, no race instrumentation,
  profiles or custom linker flags. Compilation is outside timed process RSS.
- 80×24 terminal dimensions; model input through `Update`, followed by
  `View().Content`. This measures interaction-to-frame work, excluding external
  terminal transport/paint. Timings include synchronous work, not just command
  dispatch. The timing samples are already-loaded interactions; asynchronous
  loading has separate correctness coverage and visible feedback.
- A generated, sanitized Codex ordinal-history fixture is imported through the
  real runtime into temporary SQLite storage: exactly 10,000 events, 7,001
  assistant messages and 2,999 tool calls. All 1,000 calls at sequence modulo 10
  equal to 7 are expanded. Ordinary text is 303 bytes. Every 100th event has an
  additional 10,240-byte unbroken line. The final message is exactly 1 MiB of
  unbroken text. No real session directories or repositories are inspected.
- Exploration requests 50-event chunks, jumps to the loaded end, and produces a
  frame after each response. Timed actions reset to the final event, warm up
  three times, then collect 100 samples. Scrolling alternates Page Down/Page Up;
  event navigation alternates `[`/`]` between the 1 MiB event and its neighbor.
  p95 is nearest-rank sample 95 of the sorted 100 observations.
- In active-indexing runs, an independent worker continuously appends 100
  sanitized records to a second source and runs actual discovery/import,
  SQLite writes and projections in the same runtime. Sampling starts only after
  one batch completes. The worker continues without sleeps until sampling ends.
  Batch counts differ because the slower baseline runs longer. This also scans
  the unchanged main fixture, exercising importer CPU and database contention.
- Before: task starting commit `118b7889`, with the identical timing harness
  copied into a temporary archive. After: this change. Machine-sensitive
  measurements are opt-in and contain no timing pass/fail assertions.
- Memory uses `runtime.MemStats`, explicit GC at retention checkpoints, and
  `/usr/bin/time -v` around precompiled test binaries. Peak RSS covers fixture
  import/setup as well as exploration. Allocation totals are cumulative, not
  retained memory. The memory run traverses all history again after eviction
  and separately renders a 3 MiB synthetic message through the same model seam.

## Measurements

All four measured p95s satisfy the **<100 ms** target on this machine.

| Interaction-to-frame p95 | Before idle | After idle | Before indexing | After indexing |
| --- | ---: | ---: | ---: | ---: |
| Page scroll | 141.95 ms | 0.19 ms | 154.49 ms | 0.27 ms |
| Event navigation | 246.29 ms | 15.81 ms | 266.64 ms | 18.85 ms |

The active worker completed 88 batches / 8,800 records before and 12 batches /
1,200 records after. Individual runs vary; these are measurements on the stated
reference machine and workload.

| Exploration / timing process | Before | After |
| --- | ---: | ---: |
| Exploration time | 46.92 s | 3.40 s |
| Exploration cumulative allocation bytes | 15,191,023,856 | 464,122,864 |
| Exploration allocation count | 88,056,858 | 1,251,995 |
| Heap after exploration and GC | 18,684,960 B | 6,845,880 B |
| Resident payload count after exploration | 10,000 | 150 |
| Total allocation bytes including all timing/indexing work | 34,924,415,216 | 1,178,312,968 |
| Total allocation count including timing/indexing work | 211,451,071 | 6,168,901 |
| Peak process RSS, including fixture import/setup | 98,340 KiB | 51,456 KiB |

The separate exploration/eviction/revisit run produced:

| Stage | Visited summaries | Resident events | Accounted payload bytes | Accounted rendered bytes | Lightweight metadata estimate | Heap after GC |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Explore 2,500 | 2,500 | 150 | 71,365 | 99,515 | 606,743 B | 1,725,928 B |
| Explore 5,000 | 5,000 | 150 | 71,365 | 99,515 | 1,213,493 B | 2,541,896 B |
| Explore 7,500 | 7,500 | 150 | 71,365 | 99,515 | 1,820,243 B | 3,617,160 B |
| Explore 10,000, including 1 MiB event | 10,000 | 150 | 1,119,603 | 1,685,878 | 2,426,883 B | 6,854,304 B |
| Evict large event, revisit beginning | 10,000 | 26 | 20,757 | 30,696 | 2,426,883 B | 4,024,352 B |
| Revisit 2,500 | 10,000 | 150 | 81,605 | 115,195 | 2,426,883 B | 4,232,000 B |
| Revisit 5,000 | 10,000 | 150 | 81,605 | 115,195 | 2,426,883 B | 4,256,576 B |
| Revisit 7,500 | 10,000 | 150 | 81,605 | 115,195 | 2,426,883 B | 4,301,216 B |
| Reload 1 MiB event at end | 10,000 | 150 | 1,119,603 | 1,685,878 | 2,426,883 B | 6,938,648 B |
| Separate 3 MiB message + boundary neighbor | 3 | 2 | 3,145,917 | 4,759,271 | 527 B | 8,619,336 B |

Exploration plus full revisit took 22.74 s. Up to the 1 MiB reload it allocated
3,363,826,456 bytes across 19,666,452 allocations; including the separate 3 MiB
load/render, totals reached 3,406,955,480 bytes / 19,668,321 allocations. The
memory process peaked at 52,740 KiB RSS, with 37,486,592 bytes of Go heap address
space at the oversized-event checkpoint. These values include temporary decode,
formatting and infrastructure costs. The 3 MiB row deliberately exceeds both
content budgets, demonstrating the exception rather than a strict ceiling.

A 5 ms sampler recorded peak Go heap allocations of 14,101,200 bytes during
exploration, 14,150,192 bytes during eviction/revisit, and 22,623,320 bytes during
the separate oversized-event load/render. These sampled phase peaks include
uncollected garbage and can miss shorter spikes; the OS RSS high-water mark
covers the whole process. Sampling runs only in this memory harness, adding
measurement overhead that is absent from the latency harness.

The old implementation's heavy content grows to all 10,000 payloads. The new
implementation's ordinary heavy content stays stable as history grows; its
lightweight metadata still grows. Revisit adds allocation/IO work without
restoring all explored payloads to memory. No acceptance target is inferred
from deterministic-test success.

## Reproduction

```sh
GOCACHE=/tmp/agentsession-go-cache go test -c -o /tmp/agentsession-90.test ./internal/tui
AGENTSESSION_MEASURE_TIMELINE=1 /usr/bin/time -v /tmp/agentsession-90.test \
  -test.run '^TestTimelineResponsivenessMeasurement$' -test.count=1 -test.v
AGENTSESSION_MEASURE_TIMELINE=1 /usr/bin/time -v /tmp/agentsession-90.test \
  -test.run '^TestTimelineRetentionMeasurement$' -test.count=1 -test.v
```

For the matching baseline, archive `118b7889` into a temporary directory, copy
only `internal/tui/timeline_measurement_test.go` there, compile its TUI test
binary with the same command, and run `TestTimelineResponsivenessMeasurement`.
Do not copy the memory test, which inspects the new retention accounting.
Run timing binaries sequentially, without race checks or other CPU-heavy tests
competing with them. The older `TestTimelinePerformanceWorkload` and
[initial report](tui-timeline-initial.md) remain historical prepared-response
measurements; their status-only indexing runs are not this acceptance method.

## Deterministic coverage and limits

Tests use model input/output and `app.Services`: forward/earlier traversal,
eviction/revisit, count/payload/render pressure, overlap deduplication, initial
focused context, start/end boundaries, independent scrolling/selection, resize,
technical disclosure, oversized-event crossing, local failures/retry, and delayed
cancellation and newer scrolling during pending selection reads. Focused overlap
revalidates changed sequence/summary headers even when payloads are unchanged.
A real temporary runtime/index exercises disclosure and changed or
removed evidence after source replacement/reimport. Existing service parity,
sanitization, raw-evidence and lifecycle tests remain in place.

The measured p95 target applies to this reference machine and workload, not
arbitrary hardware, terminal transport, cold reads, or arbitrarily large events.
An individual large decode/format, bounded response/staging peak, metadata growth,
and independent inspection content can raise process memory above cache budgets.
Previously visited summaries outside reread intervals can reflect an earlier
import until revisited or refreshed, consistent with non-snapshot cursors.
