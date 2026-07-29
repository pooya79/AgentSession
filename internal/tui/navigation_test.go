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

func TestNilServicesKeyCommandsAreNoOps(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: 'r', Text: "r"},
		{Code: tea.KeyEnter},
		{Code: 'n', Text: "n"},
		{Code: 'p', Text: "p"},
	} {
		m := New(context.Background(), nil)
		m.sessionsState.page = app.SessionPage{
			Sessions:   []app.SessionSummary{testSession("session-1")},
			NextCursor: "next",
		}
		m.sessionsState.pageNumber = 1
		m.sessionsState.cursors = []string{"", "next"}

		updated, cmd := updateModel(t, m, key)
		if cmd != nil {
			t.Errorf("key %q with nil services returned a command", key.String())
		}
		if updated.requestGeneration != m.requestGeneration || updated.observeGeneration != m.observeGeneration {
			t.Errorf("key %q with nil services changed request generations", key.String())
		}
	}
}

func TestSessionNavigationLoadsInlinePayloadsAndEnterTogglesCards(t *testing.T) {
	longSummary := strings.Repeat("long timeline evidence ", 80)
	services := &servicesStub{
		timeline: app.TimelinePage{
			State:  app.EvidenceComplete,
			Events: []model.EventSummary{testEvent("event-1", 1), testEvent("event-2", 2)},
			Payloads: map[model.EventID]model.NormalizedData{
				"event-1": model.SummaryData{Category: model.SummaryCategorySummary, Text: longSummary},
				"event-2": model.MessageData{Role: model.MessageRoleAssistant, Text: "complete message"},
			},
		},
	}
	m := New(context.Background(), services)
	m.sessionsState.loading = false
	m.sessionsState.page = app.SessionPage{State: app.EvidenceComplete, Sessions: []app.SessionSummary{testSession("session-1")}}
	m.sessionsState.selected = "session-1"

	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.screen != timelineScreen || cmd == nil || m.observeCancel != nil {
		t.Fatalf("open session state = screen %d, cmd %v, observing %v", m.screen, cmd != nil, m.observeCancel != nil)
	}
	timelineMsg := cmd().(timelineLoadedMsg)
	m, _ = updateModel(t, m, timelineMsg)
	if len(services.timelineCalls) != 1 || !services.timelineCalls[0].IncludePayloads {
		t.Fatalf("Timeline calls = %d, want 1", len(services.timelineCalls))
	}

	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.screen != timelineScreen || cmd != nil || !m.timelineState.expanded["event-1"] {
		t.Fatalf("expand card state = screen %d, cmd %v, expanded %v", m.screen, cmd != nil, m.timelineState.expanded["event-1"])
	}
	if len(services.detailCalls) != 0 {
		t.Fatalf("ordinary timeline opened detail: %#v", services.detailCalls)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.timelineState.expanded["event-1"] {
		t.Fatal("second Enter did not collapse selected card")
	}
}

func TestSearchSelectionOpensSessionTimeline(t *testing.T) {
	services := &servicesStub{timeline: app.TimelinePage{State: app.EvidenceComplete}}
	m := New(context.Background(), services)
	m.screen = searchScreen
	m.searchState.page = app.SearchPage{Results: []app.SearchResult{{SessionID: "matched-session"}}}

	updated, cmd := m.openSelection()
	got := updated.(*Model)
	if got.screen != timelineScreen || got.sessionsState.selected != "matched-session" ||
		!got.timelineState.loading || cmd == nil {
		t.Fatalf("search selection = screen %d, session %q, loading %v, cmd %v",
			got.screen, got.sessionsState.selected, got.timelineState.loading, cmd != nil)
	}
	message := cmd().(timelineLoadedMsg)
	if message.err != nil || len(services.timelineCalls) != 1 ||
		services.timelineCalls[0].SessionID != "matched-session" ||
		!services.timelineCalls[0].IncludePayloads || len(services.detailCalls) != 0 {
		t.Fatalf("timeline navigation = msg %#v, timeline calls %#v, detail calls %#v",
			message, services.timelineCalls, services.detailCalls)
	}
}

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

	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
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

func TestCursorHistoryAndStaleResponses(t *testing.T) {
	services := &servicesStub{}
	m := New(context.Background(), services)
	m.sessionsState.loading = false
	m.sessionsState.page = app.SessionPage{
		State: app.EvidenceComplete, Sessions: []app.SessionSummary{testSession("first")}, NextCursor: "next",
	}
	m.sessionsState.selected = "first"

	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.sessionsState.pageNumber != 1 || cmd == nil {
		t.Fatalf("next page = %d, cmd %v", m.sessionsState.pageNumber, cmd != nil)
	}
	requestGeneration := m.requestGeneration
	stale := sessionsLoadedMsg{
		generation: requestGeneration - 1,
		page:       app.SessionPage{Sessions: []app.SessionSummary{testSession("stale")}},
	}
	m, _ = updateModel(t, m, stale)
	if len(m.sessionsState.page.Sessions) != 1 || m.sessionsState.page.Sessions[0].ID != "first" {
		t.Fatalf("stale response replaced sessions: %#v", m.sessionsState.page.Sessions)
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("next-page command = %T, want batch", cmd())
	}
	for _, batched := range batch {
		if loaded, ok := batched().(sessionsLoadedMsg); ok {
			m, _ = updateModel(t, m, loaded)
		}
	}
	if got := services.sessionCalls[0].Cursor; got != "next" {
		t.Fatalf("next cursor = %q, want next", got)
	}

	m.sessionsState.loading = false
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'p', Text: "p"})
	if m.sessionsState.pageNumber != 0 || cmd == nil {
		t.Fatalf("previous page = %d, cmd %v", m.sessionsState.pageNumber, cmd != nil)
	}
	previousBatch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("previous-page command = %T, want batch", cmd())
	}
	for _, batched := range previousBatch {
		_ = batched()
	}
	if got := services.sessionCalls[1].Cursor; got != "" {
		t.Fatalf("previous cursor = %q, want first page", got)
	}
}

func TestLeavingIndexObservationDoesNotCancelApplicationWork(t *testing.T) {
	services := &servicesStub{
		timeline: app.TimelinePage{State: app.EvidenceComplete},
	}
	m := New(context.Background(), services)
	m.sessionsState.loading = false
	m.sessionsState.page = app.SessionPage{Sessions: []app.SessionSummary{testSession("session-1")}}
	m.sessionsState.selected = "session-1"
	observeCtx := m.observeCtx

	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	select {
	case <-observeCtx.Done():
	default:
		t.Fatal("leaving progress observation did not cancel the presentation observer")
	}
	if services.startCalls != 0 {
		t.Fatalf("navigation changed application import work: start calls = %d", services.startCalls)
	}
}
