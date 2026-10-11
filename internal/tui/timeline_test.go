package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestTimelineNearEndAppendsChunksSuppressesDuplicatesAndRetries(t *testing.T) {
	services := &servicesStub{}
	m := New(context.Background(), services)
	m.screen = timelineScreen
	m.sessionsState.selected = "session-1"
	m.timelineState.page = app.TimelinePage{
		State:      app.EvidenceComplete,
		NextCursor: "next-50",
		Payloads:   make(map[model.EventID]model.NormalizedData),
	}
	for index := 0; index < 50; index++ {
		event := testEvent("event-"+string(rune('A'+index)), int64(index))
		m.timelineState.page.Events = append(m.timelineState.page.Events, event)
		m.timelineState.page.Payloads[event.ID] = model.MessageData{Role: model.MessageRoleUser, Text: "message"}
	}
	m.timelineState.cursor = 44
	m.timelineState.selected = m.timelineState.page.Events[44].ID
	m.timelineState.inspectionLoading["superseded-inspection"] = true
	services.timeline = app.TimelinePage{
		State: app.EvidenceComplete,
		Events: []model.EventSummary{
			m.timelineState.page.Events[49],
			testEvent("event-50", 50),
			testEvent("event-51", 51),
		},
		Payloads: map[model.EventID]model.NormalizedData{
			"event-50": model.SummaryData{Category: model.SummaryCategorySummary, Text: "next"},
			"event-51": model.SummaryData{Category: model.SummaryCategorySummary, Text: "last"},
		},
	}

	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: ']', Text: "]"})
	if cmd == nil || !m.timelineState.loading {
		t.Fatal("near-end navigation did not request continuation")
	}
	if m.timelineState.inspectionLoading["superseded-inspection"] {
		t.Fatal("prefetch left a canceled inspection pending")
	}
	msg := cmd().(timelineLoadedMsg)
	if len(services.timelineCalls) != 1 || services.timelineCalls[0].Cursor != "next-50" ||
		!services.timelineCalls[0].IncludePayloads {
		t.Fatalf("continuation request = %#v", services.timelineCalls)
	}
	m, _ = updateModel(t, m, msg)
	if len(m.timelineState.page.Events) != 52 || m.timelineState.page.Events[50].ID != "event-50" {
		t.Fatalf("appended events = %#v", m.timelineState.page.Events[49:])
	}

	m.timelineState.page.NextCursor = "retry"
	m.timelineState.cursor = len(m.timelineState.page.Events) - 1
	m.timelineState.selected = m.timelineState.page.Events[m.timelineState.cursor].ID
	services.timelineErr = errors.New("temporary failure")
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if cmd == nil {
		t.Fatal("continuation failure setup did not request")
	}
	m, _ = updateModel(t, m, cmd().(timelineLoadedMsg))
	if len(m.timelineState.page.Events) != 52 || m.timelineState.requestedCursors["retry"] {
		t.Fatal("failed continuation discarded cards or remained duplicate-suppressed")
	}
	services.timelineErr = nil
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if cmd == nil {
		t.Fatal("failed continuation was not retryable")
	}
}

func TestTimelineUnknownInspectionStaysInline(t *testing.T) {
	event := testEvent("event-unknown", 1)
	event.Kind = model.EventKindUnknown
	services := &servicesStub{inspection: app.UnknownEvidenceInspection{
		State: app.EvidenceComplete, EventID: event.ID, Text: "redacted evidence", ReturnedSize: 17, OriginalSize: 17,
	}}
	m := New(context.Background(), services)
	m.screen = timelineScreen
	m.sessionsState.selected = "session-1"
	m.timelineState.page = app.TimelinePage{
		State:  app.EvidenceComplete,
		Events: []model.EventSummary{event},
		Payloads: map[model.EventID]model.NormalizedData{
			event.ID: model.UnknownData{Reason: model.UnknownUnsupportedRecordKind, OriginalKind: "future"},
		},
	}
	m.timelineState.selected = event.ID
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'u', Text: "u"})
	if cmd == nil || m.screen != timelineScreen {
		t.Fatal("Unknown inspection did not remain on timeline")
	}
	m, _ = updateModel(t, m, cmd().(unknownEvidenceLoadedMsg))
	lines, _ := m.timelineContent()
	if got := strings.Join(lines, "\n"); !strings.Contains(got, "redacted evidence") {
		t.Fatalf("inline inspection = %q", got)
	}
}

func TestTimelineInspectionResponseRoutesByPendingEventAcrossScreens(t *testing.T) {
	event := testEvent("event-unknown", 1)
	event.Kind = model.EventKindUnknown
	m := New(context.Background(), &servicesStub{})
	m.screen = projectionsScreen
	m.timelineState.inspectionLoading[event.ID] = true

	inspection := app.UnknownEvidenceInspection{
		State: app.EvidenceComplete, EventID: event.ID, Text: "bounded evidence",
	}
	m, _ = updateModel(t, m, unknownEvidenceLoadedMsg{
		generation: m.requestGeneration,
		eventID:    event.ID,
		inspection: inspection,
	})
	if m.timelineState.inspectionLoading[event.ID] ||
		m.timelineState.inspections[event.ID].Text != "bounded evidence" ||
		!m.timelineState.expanded[event.ID] {
		t.Fatalf("routed timeline inspection = loading %v, inspection %#v, expanded %v",
			m.timelineState.inspectionLoading[event.ID],
			m.timelineState.inspections[event.ID],
			m.timelineState.expanded[event.ID],
		)
	}
	if m.detailState.inspection.EventID != "" {
		t.Fatalf("timeline response leaked into detail state: %#v", m.detailState.inspection)
	}
}

func TestTimelineViewportHeightTracksOptionalHeaderLines(t *testing.T) {
	m := New(t.Context(), nil)
	m.screen = timelineScreen
	m.width, m.height = 80, 24
	m.sessionsState.selected = "session"
	m.timelineState.err = errors.New("continuation unavailable")
	m.timelineState.page = app.TimelinePage{
		State: app.EvidencePartial,
		Events: []model.EventSummary{{
			ID: "event", SessionID: "session", Kind: model.EventKindMessage,
		}},
		Payloads: map[model.EventID]model.NormalizedData{
			"event": model.MessageData{Role: model.MessageRoleAssistant, Text: "answer"},
		},
		Diagnostics: app.DiagnosticSynopsis{Total: 1},
	}
	m.syncViewports()
	want := max(1, m.contentHeight()-len(m.timelineHeaderLines()))
	if got := m.timelineState.viewport.Height(); got != want {
		t.Fatalf("timeline viewport height = %d, want %d for %d header lines", got, want, len(m.timelineHeaderLines()))
	}
}
