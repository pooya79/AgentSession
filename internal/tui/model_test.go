package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

type servicesStub struct {
	mu sync.Mutex

	start       app.ImportAllStart
	startErr    error
	status      app.ImportAllStatus
	statusErr   error
	sessionPage app.SessionPage
	sessionErr  error
	timeline    app.TimelinePage
	timelineErr error
	detail      app.EventDetail
	detailErr   error

	startCalls    int
	sessionCalls  []app.ListSessionsRequest
	timelineCalls []app.TimelineRequest
	detailCalls   []app.EventDetailRequest
	statusCtx     context.Context
}

func (s *servicesStub) DiscoverSources(context.Context) (app.SourceDiscovery, error) {
	return app.SourceDiscovery{}, nil
}

func (s *servicesStub) StartImport(context.Context, model.SourceID) (app.ImportStart, error) {
	return app.ImportStart{}, nil
}

func (s *servicesStub) StartImportAll(context.Context) (app.ImportAllStart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startCalls++
	return s.start, s.startErr
}

func (s *servicesStub) ImportAllStatus(ctx context.Context) (app.ImportAllStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusCtx = ctx
	return s.status, s.statusErr
}

func (s *servicesStub) ListSessions(_ context.Context, request app.ListSessionsRequest) (app.SessionPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionCalls = append(s.sessionCalls, request)
	return s.sessionPage, s.sessionErr
}

func (s *servicesStub) Timeline(_ context.Context, request app.TimelineRequest) (app.TimelinePage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timelineCalls = append(s.timelineCalls, request)
	return s.timeline, s.timelineErr
}

func (s *servicesStub) EventDetail(_ context.Context, request app.EventDetailRequest) (app.EventDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detailCalls = append(s.detailCalls, request)
	return s.detail, s.detailErr
}

func testSession(id string) app.SessionSummary {
	return app.SessionSummary{
		ID: model.SessionID(id), Title: id, EventCount: 2, State: app.EvidenceComplete,
	}
}

func testEvent(id string, sequence int64) model.EventSummary {
	return model.EventSummary{
		ID: model.EventID(id), SessionID: "session-1", Sequence: sequence,
		Kind: model.EventKindMessage, Summary: "summary",
	}
}

func updateModel(t *testing.T, current Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := current.Update(msg)
	result, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	return result, cmd
}

func TestInitStartsImportAllAndLoadsSessions(t *testing.T) {
	services := &servicesStub{
		start: app.ImportAllStart{Status: app.ImportAllStatus{Active: true, Phase: app.ImportAllIndexing}},
		sessionPage: app.SessionPage{
			State: app.EvidenceComplete, Sessions: []app.SessionSummary{testSession("session-1")},
		},
	}
	m := New(context.Background(), services)
	msg, ok := m.Init()().(tea.BatchMsg)
	if !ok || len(msg) != 2 {
		t.Fatalf("Init() message = %T %#v, want two-command batch", msg, msg)
	}
	for _, cmd := range msg {
		switch result := cmd().(type) {
		case importStartedMsg:
			m, _ = updateModel(t, m, result)
		case sessionsLoadedMsg:
			m, _ = updateModel(t, m, result)
		default:
			t.Fatalf("startup result type = %T", result)
		}
	}
	if services.startCalls != 1 || len(services.sessionCalls) != 1 {
		t.Fatalf("startup calls = start %d, sessions %d", services.startCalls, len(services.sessionCalls))
	}
	if got := m.View().Content; !strings.Contains(got, "Imported sessions") || strings.Contains(got, "Select source") {
		t.Fatalf("startup view = %q", got)
	}
}

func TestImportCompletionRefreshesSessions(t *testing.T) {
	services := &servicesStub{}
	m := New(context.Background(), services)
	m.importStatus = app.ImportAllStatus{Active: true, Phase: app.ImportAllIndexing}
	m.observeGeneration = 7

	updated, cmd := updateModel(t, m, importStatusMsg{
		generation: 7,
		status: app.ImportAllStatus{
			Phase: app.ImportAllIssues, SourcesDiscovered: 2, SourcesCompleted: 2,
			SourcesFailed: 1, DiagnosticsTotal: 1,
		},
	})
	if cmd == nil || !updated.sessionsLoading {
		t.Fatal("terminal import status did not trigger a sessions refresh")
	}
	if got := updated.View().Content; !strings.Contains(got, "completed with issues") {
		t.Fatalf("completion view = %q", got)
	}
}

func TestPollImportObservationLifecycle(t *testing.T) {
	t.Run("current poll replaces observer context", func(t *testing.T) {
		services := &servicesStub{}
		m := New(context.Background(), services)
		m.observeGeneration = 7
		previousCtx := m.observeCtx

		updated, cmd := updateModel(t, m, pollImportMsg{generation: 7})
		if cmd == nil || updated.observeCtx == previousCtx {
			t.Fatal("current poll did not replace the observer context")
		}
		select {
		case <-previousCtx.Done():
		default:
			t.Fatal("replaced observer context was not canceled")
		}

		_ = cmd()
		if services.statusCtx != updated.observeCtx {
			t.Fatal("status read did not use the replacement observer context")
		}
		select {
		case <-services.statusCtx.Done():
			t.Fatal("replacement observer context was unexpectedly canceled")
		default:
		}
	})

	t.Run("stale and stopped polls do not restart observation", func(t *testing.T) {
		services := &servicesStub{}
		m := New(context.Background(), services)
		observeCtx := m.observeCtx
		generation := m.observeGeneration

		updated, cmd := updateModel(t, m, pollImportMsg{generation: generation - 1})
		if cmd != nil || updated.observeCtx != observeCtx {
			t.Fatal("stale poll replaced or restarted the observer")
		}

		updated.stopObservation()
		stoppedGeneration := updated.observeGeneration
		updated, cmd = updateModel(t, updated, pollImportMsg{generation: stoppedGeneration})
		if cmd != nil || updated.observeCancel != nil || updated.observeGeneration != stoppedGeneration {
			t.Fatal("poll after stopObservation restarted observation")
		}
		if services.statusCtx != nil {
			t.Fatal("ignored polls read import status")
		}
	})
}

func TestNilServicesKeyCommandsAreNoOps(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: 'r', Text: "r"},
		{Code: tea.KeyEnter},
		{Code: 'n', Text: "n"},
		{Code: 'p', Text: "p"},
	} {
		m := New(context.Background(), nil)
		m.sessions = app.SessionPage{
			Sessions:   []app.SessionSummary{testSession("session-1")},
			NextCursor: "next",
		}
		m.sessionPage = 1
		m.sessionCursors = []string{"", "next"}

		updated, cmd := updateModel(t, m, key)
		if cmd != nil {
			t.Errorf("key %q with nil services returned a command", key.String())
		}
		if updated.requestGeneration != m.requestGeneration || updated.observeGeneration != m.observeGeneration {
			t.Errorf("key %q with nil services changed request generations", key.String())
		}
	}
}

func TestNavigationUsesSummaryThenPayloadDetail(t *testing.T) {
	services := &servicesStub{
		timeline: app.TimelinePage{
			State:  app.EvidenceComplete,
			Events: []model.EventSummary{testEvent("event-1", 1), testEvent("event-2", 2)},
		},
		detail: app.EventDetail{
			State:   app.EvidenceComplete,
			Event:   testEvent("event-1", 1),
			Payload: model.MessageData{Role: model.MessageRoleAssistant, Text: "payload text"},
		},
	}
	m := New(context.Background(), services)
	m.sessionsLoading = false
	m.sessions = app.SessionPage{State: app.EvidenceComplete, Sessions: []app.SessionSummary{testSession("session-1")}}
	m.selectedSession = "session-1"

	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.screen != timelineScreen || cmd == nil || m.observeCancel != nil {
		t.Fatalf("open session state = screen %d, cmd %v, observing %v", m.screen, cmd != nil, m.observeCancel != nil)
	}
	timelineMsg := cmd().(timelineLoadedMsg)
	m, _ = updateModel(t, m, timelineMsg)
	if len(services.timelineCalls) != 1 {
		t.Fatalf("Timeline calls = %d, want 1", len(services.timelineCalls))
	}

	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.screen != detailScreen || cmd == nil {
		t.Fatalf("open detail state = screen %d, cmd %v", m.screen, cmd != nil)
	}
	detailCtx := m.requestCtx
	m, _ = updateModel(t, m, cmd().(detailLoadedMsg))
	if len(services.detailCalls) != 1 || !services.detailCalls[0].IncludePayload {
		t.Fatalf("detail calls = %#v, want one payload request", services.detailCalls)
	}
	if got := m.View().Content; !strings.Contains(got, "Normalized payload") || !strings.Contains(got, "payload text") {
		t.Fatalf("detail view = %q", got)
	}

	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.screen != timelineScreen || m.eventCursor != 0 {
		t.Fatalf("back from detail = screen %d selection %d", m.screen, m.eventCursor)
	}
	select {
	case <-detailCtx.Done():
	default:
		t.Fatal("back from detail did not cancel its presentation request")
	}
}

func TestCursorHistoryAndStaleResponses(t *testing.T) {
	services := &servicesStub{}
	m := New(context.Background(), services)
	m.sessionsLoading = false
	m.sessions = app.SessionPage{
		State: app.EvidenceComplete, Sessions: []app.SessionSummary{testSession("first")}, NextCursor: "next",
	}
	m.selectedSession = "first"

	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.sessionPage != 1 || cmd == nil {
		t.Fatalf("next page = %d, cmd %v", m.sessionPage, cmd != nil)
	}
	requestGeneration := m.requestGeneration
	stale := sessionsLoadedMsg{
		generation: requestGeneration - 1,
		page:       app.SessionPage{Sessions: []app.SessionSummary{testSession("stale")}},
	}
	m, _ = updateModel(t, m, stale)
	if len(m.sessions.Sessions) != 1 || m.sessions.Sessions[0].ID != "first" {
		t.Fatalf("stale response replaced sessions: %#v", m.sessions.Sessions)
	}
	m, _ = updateModel(t, m, cmd().(sessionsLoadedMsg))
	if got := services.sessionCalls[0].Cursor; got != "next" {
		t.Fatalf("next cursor = %q, want next", got)
	}

	m.sessionsLoading = false
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'p', Text: "p"})
	if m.sessionPage != 0 || cmd == nil {
		t.Fatalf("previous page = %d, cmd %v", m.sessionPage, cmd != nil)
	}
	_ = cmd()
	if got := services.sessionCalls[1].Cursor; got != "" {
		t.Fatalf("previous cursor = %q, want first page", got)
	}
}

func TestLeavingIndexObservationDoesNotCancelApplicationWork(t *testing.T) {
	services := &servicesStub{
		timeline: app.TimelinePage{State: app.EvidenceComplete},
	}
	m := New(context.Background(), services)
	m.sessionsLoading = false
	m.sessions = app.SessionPage{Sessions: []app.SessionSummary{testSession("session-1")}}
	m.selectedSession = "session-1"
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

func TestIndexingStatesDiagnosticsAndHostileContentAreSafe(t *testing.T) {
	hostile := "bad\x1b]8;;https://attacker.invalid\x07link\x1b]8;;\x07\u202e"
	m := New(context.Background(), &servicesStub{})
	m.width, m.height = 60, 20
	m.screen = indexingScreen
	m.importStatus = app.ImportAllStatus{
		Phase: app.ImportAllIssues, SourcesDiscovered: 1, SourcesCompleted: 1, SourcesFailed: 1,
		DiagnosticsTotal: 2, DiagnosticsOmitted: 1,
		Sources: []app.ImportAllSourceStatus{{
			ID: "source", Kind: "codex", Path: hostile, Origin: "default",
			Phase: app.ImportFailed, Failure: hostile, Complete: true,
		}},
		RecentDiagnostics: []app.ImportAllDiagnostic{{
			Code: hostile, Severity: model.SeverityWarning, Message: hostile, SourcePath: hostile,
		}},
	}
	content := m.View().Content
	if strings.Contains(content, "\x1b") || strings.Contains(content, "\u202e") || !strings.Contains(content, "<U+202E>") {
		t.Fatalf("unsafe indexing view = %q", content)
	}
	if strings.Count(content, "\n") >= m.height {
		t.Fatalf("view has too many rows: %d for height %d", strings.Count(content, "\n")+1, m.height)
	}
}

func TestRequestFailuresAndUnavailableEvidence(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	m.sessionsLoading = false
	m.sessionsErr = errors.New("database unavailable")
	if got := m.View().Content; !strings.Contains(got, "Could not load sessions") {
		t.Fatalf("failure view = %q", got)
	}

	m.screen = timelineScreen
	m.timelineErr = nil
	m.timeline = app.TimelinePage{
		State:       app.EvidenceUnavailable,
		Diagnostics: app.DiagnosticSynopsis{Total: 3, Omitted: 2},
	}
	if got := m.View().Content; !strings.Contains(got, "Timeline evidence is unavailable") {
		t.Fatalf("unavailable timeline view = %q", got)
	}
}

func TestResizeZeroNarrowAndQuit(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	if got := m.View(); !got.AltScreen || got.Content == "" {
		t.Fatalf("zero-size view = %#v", got)
	}
	m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: 24, Height: 6})
	view := m.View()
	if strings.Count(view.Content, "\n") >= 6 {
		t.Fatalf("narrow view rows = %d", strings.Count(view.Content, "\n")+1)
	}
	_, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("q did not request exit")
	}
}

func TestTerminalViewSanitizesContent(t *testing.T) {
	view := terminalView("safe\x1b]8;;https://attacker.invalid\x07label\x1b]8;;\x07\u202e")
	if got, want := view.Content, "safelabel<U+202E>"; got != want {
		t.Fatalf("terminalView() content = %q, want %q", got, want)
	}
}
