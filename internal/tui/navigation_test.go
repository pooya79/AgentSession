package tui

import (
	"context"
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
	commandResult := cmd()
	var message timelineLoadedMsg
	var loaded bool
	switch result := commandResult.(type) {
	case timelineLoadedMsg:
		message, loaded = result, true
	case tea.BatchMsg:
		for _, batchedCmd := range result {
			if candidate, ok := batchedCmd().(timelineLoadedMsg); ok {
				message = candidate
				loaded = true
				break
			}
		}
	default:
		t.Fatalf("timeline navigation command result = %T", commandResult)
	}
	if !loaded {
		t.Fatal("timeline navigation batch did not contain a timeline load")
	}
	if message.err != nil || len(services.timelineCalls) != 1 ||
		services.timelineCalls[0].SessionID != "matched-session" ||
		!services.timelineCalls[0].IncludePayloads || len(services.detailCalls) != 0 {
		t.Fatalf("timeline navigation = msg %#v, timeline calls %#v, detail calls %#v",
			message, services.timelineCalls, services.detailCalls)
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
