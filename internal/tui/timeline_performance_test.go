package tui

import (
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// Opt-in measurement, deliberately separate from deterministic acceptance tests.
// Every sample ends after View produces visible output, not command dispatch.
func TestTimelinePerformanceWorkload(t *testing.T) {
	if os.Getenv("AGENTSESSION_MEASURE_TIMELINE") == "" {
		t.Skip("set AGENTSESSION_MEASURE_TIMELINE=1 for the 10,000-event measurement")
	}
	m := New(t.Context(), &servicesStub{})
	m.screen = timelineScreen
	m.sessionsState.selected = "session-1"
	m.width, m.height = 80, 24
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	started := time.Now()
	for start := 0; start < 10000; start += 50 {
		page := app.TimelinePage{State: app.EvidenceComplete, Payloads: make(map[model.EventID]model.NormalizedData)}
		for i := start; i < start+50; i++ {
			e := testEvent(fmt.Sprintf("event-%05d", i), int64(i))
			text := strings.Repeat("recorded evidence ", 16)
			if i%100 == 0 {
				text = strings.Repeat("long-line-", 1024)
			}
			if i == 9999 {
				text = strings.Repeat("x", 1<<20)
			}
			if i%10 < 7 || i == 9999 {
				page.Payloads[e.ID] = model.MessageData{Role: model.MessageRoleAssistant, Text: text}
			} else {
				e.Kind = model.EventKindCommand
				page.Payloads[e.ID] = model.CommandData{Command: "go test ./...", Output: text}
				m.timelineState.expanded[e.ID] = i%10 == 7
			}
			page.Events = append(page.Events, e)
		}
		cursor := ""
		if start > 0 {
			cursor = fmt.Sprint(start)
		}
		m, _ = updateModel(t, m, timelineLoadedMsg{generation: m.requestGeneration, cursor: cursor, page: page})
		_ = m.View().Content
	}
	runtime.ReadMemStats(&after)
	t.Logf("exploration=%s heap=%d heap_sys=%d alloc_bytes=%d allocations=%d", time.Since(started), after.HeapAlloc, after.HeapSys, after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs)
	for _, indexing := range []bool{false, true} {
		m.indexingState.status.Active = indexing
		for _, action := range []string{"scroll", "event"} {
			var samples []time.Duration
			for i := range 33 {
				key := tea.KeyPressMsg{Code: tea.KeyPgDown}
				if action == "event" {
					key = tea.KeyPressMsg{Code: ']', Text: "]"}
					if os.Getenv("AGENTSESSION_BASELINE_KEYS") != "" {
						key = tea.KeyPressMsg{Code: tea.KeyDown}
					}
				}
				start := time.Now()
				m, _ = updateModel(t, m, key)
				_ = m.View().Content
				if i >= 3 {
					samples = append(samples, time.Since(start))
				}
			}
			slices.Sort(samples)
			t.Logf("indexing_status=%t action=%s samples=%d p95=%s", indexing, action, len(samples), samples[28])
		}
	}
}
