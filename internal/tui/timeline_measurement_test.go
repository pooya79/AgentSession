package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/discovery"
	"github.com/pooya79/AgentSession/internal/model"
)

// A synthetic source and real temporary index exercise the existing services.
// Source writing belongs only to this test fixture, never to the TUI.
func timelineIndex(t *testing.T, count int) (*app.Runtime, string, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "timeline.jsonl")
	writeTimelineSource(t, path, "timeline", 0, count)
	background := filepath.Join(root, "background.jsonl")
	writeTimelineSource(t, background, "background", 0, 1)
	inputs := discovery.Inputs{FileSystem: discovery.OSFileSystem{}, HomeDir: root, WorkingDir: root, GOOS: "linux", ExplicitPaths: []discovery.ConfiguredPath{{Kind: discovery.SourceCodex, Path: path}, {Kind: discovery.SourceCodex, Path: background}}}
	paths := app.PathInputs{GOOS: "linux", HomeDir: root, WorkingDir: root}
	service, err := app.OpenRuntime(t.Context(), app.RuntimeConfig{DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "config"), PathInputs: &paths, DiscoveryInputs: &inputs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if result, err := service.DiscoverAndImport(t.Context()); err != nil {
		t.Fatalf("import: %v; %+v", err, result)
	}
	return service, path, background
}

func writeTimelineSource(t *testing.T, path, session string, start, count int) {
	t.Helper()
	if err := appendTimelineSource(path, session, start, count); err != nil {
		t.Fatal(err)
	}
}

func appendTimelineSource(path, session string, start, count int) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if start == 0 {
		if err := enc.Encode(map[string]any{"ordinal": 0, "type": "session_meta", "payload": map[string]any{"id": session, "cwd": "/sanitized/project"}}); err != nil {
			return err
		}
	}
	for i := start; i < start+count; i++ {
		text := fmt.Sprintf("EVIDENCE-%05d ", i) + strings.Repeat("recorded evidence ", 16)
		if i%100 == 0 {
			text += strings.Repeat("long-line-", 1024)
		}
		if i == 9999 {
			text = strings.Repeat("x", 1<<20)
		}
		payload := map[string]any{"type": "message", "id": fmt.Sprintf("%s-%d", session, i), "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": text}}}
		if i%10 >= 7 && i != 9999 {
			payload = map[string]any{"type": "function_call", "call_id": fmt.Sprintf("%s-%d", session, i), "name": "sanitized_tool", "arguments": text}
		}
		if err := enc.Encode(map[string]any{"ordinal": i + 1, "type": "response_item", "payload": payload}); err != nil {
			return err
		}
	}
	return nil
}

func indexedTimelineModel(t *testing.T, service app.Services) Model {
	t.Helper()
	sessions, err := service.ListSessions(t.Context(), app.ListSessionsRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var session model.SessionID
	for _, s := range sessions.Sessions {
		if s.EventCount > 1 {
			session = s.ID
			break
		}
	}
	if session == "" {
		t.Fatal("timeline fixture session missing")
	}
	m := New(t.Context(), service)
	m.screen = timelineScreen
	m.sessionsState.selected = session
	m.width, m.height = 80, 24
	m.spinnerActive = true // Test commands need no asynchronous spinner loop.
	return m
}

func TestTimelineResponsivenessMeasurement(t *testing.T) {
	if os.Getenv("AGENTSESSION_MEASURE_TIMELINE") == "" {
		t.Skip("opt-in reference-machine measurement")
	}
	service, _, background := timelineIndex(t, 10000)
	m := indexedTimelineModel(t, service)
	sessions, err := service.ListSessions(t.Context(), app.ListSessionsRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions.Sessions {
		if s.ID == m.sessionsState.selected && s.EventCount != 10000 {
			t.Fatalf("workload has %d events, want 10000", s.EventCount)
		}
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	began := time.Now()
	// Drive real forward continuation, rendering every bounded response.
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	m, _ = updateModel(t, m, cmd())
	for i := 0; i < 200; i++ {
		for _, event := range m.timelineState.page.Events {
			if event.Sequence%10 == 7 {
				m.timelineState.expanded[event.ID] = true
			}
		}
		m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
		_ = m.View().Content
		if cmd == nil {
			break
		}
		m, _ = updateModel(t, m, cmd())
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	t.Logf("exploration=%s retained_heap=%d heap_sys=%d alloc_bytes=%d allocations=%d resident_payloads=%d", time.Since(began), after.HeapAlloc, after.HeapSys, after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs, len(m.timelineState.page.Payloads))
	// Warm the large message and measure scrolling within it. Event jumps alternate
	// between it and the previous event; every sample therefore changes output.
	for _, indexing := range []bool{false, true} {
		var batches atomic.Int64
		stop := make(chan struct{})
		done := make(chan error, 1)
		if indexing {
			go func() {
				next := 1
				for {
					select {
					case <-stop:
						done <- nil
						return
					default:
					}
					if err := appendTimelineSource(background, "background", next, 100); err != nil {
						done <- err
						return
					}
					next += 100
					if _, err := service.DiscoverAndImport(context.Background()); err != nil {
						done <- err
						return
					}
					batches.Add(1)
				}
			}()
			// Ensure the worker has really completed an import before sampling.
			deadline := time.After(30 * time.Second)
			for batches.Load() == 0 {
				select {
				case err := <-done:
					t.Fatalf("index worker: %v", err)
				case <-deadline:
					t.Fatal("index worker did not start")
				case <-time.After(time.Millisecond):
				}
			}
		}
		m.indexingState.status.Active = indexing
		for _, action := range []string{"scroll", "event"} {
			m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
			_ = m.View().Content
			var samples []time.Duration
			for i := 0; i < 103; i++ {
				key := tea.KeyPressMsg{Code: tea.KeyPgDown}
				if i%2 == 1 {
					key = tea.KeyPressMsg{Code: tea.KeyPgUp}
				}
				if action == "event" {
					key = tea.KeyPressMsg{Code: '[', Text: "["}
					if i%2 == 1 {
						key = tea.KeyPressMsg{Code: ']', Text: "]"}
					}
				}
				started := time.Now()
				m, _ = updateModel(t, m, key)
				_ = m.View().Content
				if i >= 3 {
					samples = append(samples, time.Since(started))
				}
			}
			slices.Sort(samples)
			t.Logf("indexing=%t action=%s warmup=3 samples=%d p95=%s", indexing, action, len(samples), samples[94])
		}
		if indexing {
			close(stop)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			t.Logf("concurrent_import_batches=%d records=%d", batches.Load(), batches.Load()*100)
		}
	}
	runtime.ReadMemStats(&after)
	t.Logf("total_alloc_bytes=%d allocations=%d heap_sys=%d", after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs, after.HeapSys)
}
