package tui

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	tea "charm.land/bubbletea/v2"
	"github.com/pooya79/AgentSession/internal/model"
)

// Accounting reports content bytes separately from measured heap/RSS. Map
// buckets, allocator rounding, SQLite, and temporary formatting buffers are not
// represented by the logical content budgets.
func timelineRetentionSample(t *testing.T, m Model, stage string, baseline runtime.MemStats) {
	t.Helper()
	payload, rendered, metadata := 0, 0, 0
	for id := range m.timelineState.page.Payloads {
		payload += m.timelineState.payloadMetadata[id].bytes + len(m.timelineState.inspections[id].Text)
	}
	for _, card := range m.timelineState.cards {
		rendered += card.bytes
	}
	for _, event := range m.timelineState.page.Events {
		metadata += int(unsafe.Sizeof(event)) + len(event.ID) + len(event.SessionID) + len(event.Summary) + len(event.Kind)
	}
	metadata += len(m.timelineState.payloadMetadata) * int(unsafe.Sizeof(timelinePayloadMetadata{})+unsafe.Sizeof(model.EventID("")))
	metadata += len(m.timelineState.expanded) * int(unsafe.Sizeof(model.EventID(""))+1)
	metadata += len(m.timelineState.disclosureAnchors) * int(unsafe.Sizeof(model.EventID(""))+unsafe.Sizeof(timelineReadingAnchor{}))
	for cursor := range m.timelineState.requestedCursors {
		metadata += len(cursor) + int(unsafe.Sizeof(cursor)) + 1
	}
	var mem runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&mem)
	t.Logf("stage=%s visited=%d resident=%d payload_bytes=%d rendered_bytes=%d metadata_bytes_estimate=%d heap=%d heap_sys=%d alloc_bytes=%d allocations=%d", stage, len(m.timelineState.page.Events), len(m.timelineState.page.Payloads), payload, rendered, metadata, mem.HeapAlloc, mem.HeapSys, mem.TotalAlloc-baseline.TotalAlloc, mem.Mallocs-baseline.Mallocs)
	runtime.KeepAlive(m)
}

// Sampled phase peaks complement the process RSS high-water mark. Brief peaks
// between samples can be missed; this is deliberately not a hard memory bound.
type timelineMemoryPeaks struct {
	phase atomic.Int32
	heap  [3]atomic.Uint64
	stop  chan struct{}
	done  chan struct{}
}

func sampleTimelineMemory() *timelineMemoryPeaks {
	peaks := &timelineMemoryPeaks{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(peaks.done)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-peaks.stop:
				return
			case <-ticker.C:
				var sample runtime.MemStats
				runtime.ReadMemStats(&sample)
				phase := peaks.phase.Load()
				old := peaks.heap[phase].Load()
				if sample.HeapAlloc > old {
					peaks.heap[phase].Store(sample.HeapAlloc)
				}
			}
		}
	}()
	return peaks
}

func TestTimelineRetentionMeasurement(t *testing.T) {
	if os.Getenv("AGENTSESSION_MEASURE_TIMELINE") == "" {
		t.Skip("opt-in memory measurement")
	}
	service, _, _ := timelineIndex(t, 10000)
	m := indexedTimelineModel(t, service)
	peaks := sampleTimelineMemory()
	defer func() {
		close(peaks.stop)
		<-peaks.done
		t.Logf("sampled_peak_heap_5ms exploration=%d eviction_revisit=%d oversized=%d", peaks.heap[0].Load(), peaks.heap[1].Load(), peaks.heap[2].Load())
	}()
	var baseline runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&baseline)
	began := time.Now()
	m = historyKey(t, m, "r")
	var cmd tea.Cmd
	for i := 0; i < 200; i++ {
		for _, event := range m.timelineState.page.Events {
			if event.Sequence%10 == 7 {
				m.timelineState.expanded[event.ID] = true
			}
		}
		m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
		_ = m.View().Content
		if i%50 == 49 {
			timelineRetentionSample(t, m, fmt.Sprintf("explore-%d", (i+1)*50), baseline)
		}
		if cmd == nil {
			break
		}
		m, _ = updateModel(t, m, cmd())
	}
	// Reload the beginning, then revisit all available events in source order.
	peaks.phase.Store(1)
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
	if cmd != nil {
		m, _ = updateModel(t, m, cmd())
	}
	_ = m.View().Content
	timelineRetentionSample(t, m, "evict-large-revisit-start", baseline)
	for i := 1; i < 10000; i++ {
		m = historyKey(t, m, "]")
		_ = m.View().Content
		if i%2500 == 2499 {
			timelineRetentionSample(t, m, fmt.Sprintf("revisit-%d", i+1), baseline)
		}
	}
	timelineRetentionSample(t, m, "large-message-reloaded", baseline)
	t.Logf("exploration-and-revisit=%s", time.Since(began))
	peaks.phase.Store(2)
	oversized, source := historyModel(t, 3)
	source.payloads["history-001"] = model.MessageData{Text: strings.Repeat("x", 3<<20)}
	oversized = historyKey(t, oversized, "r")
	_ = oversized.View().Content
	timelineRetentionSample(t, oversized, "oversized-3MiB-with-boundary-neighbor", baseline)
}
