package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

type servicesStub struct {
	mu sync.Mutex

	start               app.ImportAllStart
	startErr            error
	status              app.ImportAllStatus
	statusErr           error
	sessionPage         app.SessionPage
	sessionErr          error
	overview            app.LibraryOverview
	overviewErr         error
	timeline            app.TimelinePage
	timelineErr         error
	detail              app.EventDetail
	detailErr           error
	inspection          app.UnknownEvidenceInspection
	inspectionErr       error
	projections         app.ProjectionStatus
	projectionErr       error
	projectionAction    app.ProjectionAction
	projectionActionErr error

	startCalls            int
	sessionCalls          []app.ListSessionsRequest
	timelineCalls         []app.TimelineRequest
	detailCalls           []app.EventDetailRequest
	statusCtx             context.Context
	projectionStatusCalls int
	retryCalls            int
	rebuildCalls          []string
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

func (s *servicesStub) LibraryOverview(context.Context) (app.LibraryOverview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.overview, s.overviewErr
}

func (s *servicesStub) EventLocations(context.Context, []model.EventID) (map[model.EventID]app.EventLocation, error) {
	return nil, nil
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

func (s *servicesStub) InspectUnknownEvidence(context.Context, model.SessionID, model.EventID) (app.UnknownEvidenceInspection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inspection, s.inspectionErr
}

func (s *servicesStub) ProjectionStatus(context.Context, model.SessionID) (app.ProjectionStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projectionStatusCalls++
	return s.projections, s.projectionErr
}

func (s *servicesStub) RetryProjections(context.Context, model.SessionID) (app.ProjectionAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retryCalls++
	return s.projectionAction, s.projectionActionErr
}

func (s *servicesStub) RebuildProjections(_ context.Context, _ model.SessionID, kind string) (app.ProjectionAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rebuildCalls = append(s.rebuildCalls, kind)
	return s.projectionAction, s.projectionActionErr
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
	if !ok || len(msg) != 5 {
		t.Fatalf("Init() message = %T %#v, want sessions, overview, import, theme, and spinner commands", msg, msg)
	}
	for _, cmd := range msg {
		switch result := cmd().(type) {
		case importStartedMsg:
			m, _ = updateModel(t, m, result)
		case sessionsLoadedMsg:
			m, _ = updateModel(t, m, result)
		}
	}
	if services.startCalls != 1 || len(services.sessionCalls) != 1 {
		t.Fatalf("startup calls = start %d, sessions %d", services.startCalls, len(services.sessionCalls))
	}
	if got := m.View().Content; !strings.Contains(got, "Sessions dashboard") || strings.Contains(got, "Select source") {
		t.Fatalf("startup view = %q", got)
	}
}

func TestReloadSessionsRestartsStoppedSpinnerWithoutDuplicatingTickChain(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	m.sessionsState.loading = false
	m.sessionsState.overviewLoading = false
	m.spinnerActive = true
	m, cmd := updateModel(t, m, spinner.TickMsg{})
	if cmd != nil || m.spinnerActive {
		t.Fatalf("idle tick = (cmd %v, active %v), want stopped chain", cmd != nil, m.spinnerActive)
	}

	cmd = m.reloadSessions()
	if !m.spinnerActive {
		t.Fatal("reloadSessions left spinner inactive")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("reloadSessions command = %T with %d children, want work plus one tick", batch, len(batch))
	}
	if _, ok := batch[0]().(tea.BatchMsg); !ok {
		t.Fatalf("restarted spinner first command = %T, want batched reload work", batch[0]())
	}

	cmd = m.reloadSessions()
	batch, ok = cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("active reload command = %T with %d children, want only session and overview work", batch, len(batch))
	}
	if _, ok := batch[0]().(sessionsLoadedMsg); !ok {
		t.Fatalf("active spinner first command = %T, want session load without another tick wrapper", batch[0]())
	}
}

func TestImportCompletionRefreshesSessions(t *testing.T) {
	services := &servicesStub{}
	m := New(context.Background(), services)
	m.indexingState.status = app.ImportAllStatus{Active: true, Phase: app.ImportAllIndexing}
	m.observeGeneration = 7

	updated, cmd := updateModel(t, m, importStatusMsg{
		generation: 7,
		status: app.ImportAllStatus{
			Phase: app.ImportAllIssues, SourcesDiscovered: 2, SourcesCompleted: 2,
			SourcesFailed: 1, DiagnosticsTotal: 1,
		},
	})
	if cmd == nil || !updated.sessionsState.loading {
		t.Fatal("terminal import status did not trigger a sessions refresh")
	}
	if got := updated.View().Content; !strings.Contains(got, "COMPLETED WITH ISSUES") {
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
	m.sessionsState.loading = false
	m.sessionsState.page = app.SessionPage{State: app.EvidenceComplete, Sessions: []app.SessionSummary{testSession("session-1")}}
	m.sessionsState.selected = "session-1"

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
	if m.screen != timelineScreen || m.timelineState.cursor != 0 {
		t.Fatalf("back from detail = screen %d selection %d", m.screen, m.timelineState.cursor)
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

func TestIndexingStatesDiagnosticsAndHostileContentAreSafe(t *testing.T) {
	hostile := "bad\x1b]8;;https://attacker.invalid\x07link\x1b]8;;\x07\u202e"
	m := New(context.Background(), &servicesStub{})
	m.width, m.height = 60, 20
	m.screen = indexingScreen
	m.indexingState.status = app.ImportAllStatus{
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
	if strings.Contains(content, "attacker.invalid") || strings.Contains(content, "\u202e") || !strings.Contains(content, "<U+202E>") {
		t.Fatalf("unsafe indexing view = %q", content)
	}
	if strings.Count(content, "\n") >= m.height {
		t.Fatalf("view has too many rows: %d for height %d", strings.Count(content, "\n")+1, m.height)
	}
}

func TestRequestFailuresAndUnavailableEvidence(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	m.sessionsState.loading = false
	m.sessionsState.err = errors.New("database unavailable")
	if got := m.View().Content; !strings.Contains(got, "Could not load sessions") {
		t.Fatalf("failure view = %q", got)
	}

	m.screen = timelineScreen
	m.timelineState.err = nil
	m.timelineState.page = app.TimelinePage{
		State:       app.EvidenceUnavailable,
		Diagnostics: app.DiagnosticSynopsis{Total: 3, Omitted: 2},
	}
	if got := m.View().Content; !strings.Contains(got, "Timeline evidence is unavailable") {
		t.Fatalf("unavailable timeline view = %q", got)
	}
}

func TestUnknownEvidenceInspectionStatesAreRenderedExplicitly(t *testing.T) {
	event := testEvent("event-1", 1)
	event.Kind = model.EventKindUnknown
	m := New(context.Background(), &servicesStub{})
	m.sessionsState.selected = "session-1"
	m.detailState.detail = app.EventDetail{
		State:   app.EvidenceComplete,
		Event:   event,
		Payload: model.UnknownData{Reason: model.UnknownUnsupportedRecordKind, OriginalKind: "future"},
	}

	tests := map[app.EvidenceState]struct {
		eventID model.EventID
		want    string
	}{
		app.EvidenceUnavailable: {eventID: event.ID, want: "Retained evidence for this event is no longer available."},
		app.EvidenceNotFound:    {want: "This event's retained evidence could not be located."},
	}
	for state, test := range tests {
		m.detailState.inspection = app.UnknownEvidenceInspection{
			State: state, EventID: test.eventID,
		}
		if got := strings.Join(m.detailContentLines(), "\n"); !strings.Contains(got, test.want) {
			t.Errorf("state %q detail = %q, want %q", state, got, test.want)
		}
	}
}

func TestProjectionPanelControlsPollingConfirmationAndSafeDiagnostics(t *testing.T) {
	hostile := "unsafe\x1b]8;;https://attacker.invalid\x07link\x1b]8;;\x07"
	status := app.ProjectionStatus{
		State: app.EvidenceComplete, SessionID: "session-1", Active: true,
		Summary: app.ProjectionSummary{Pending: 1},
		Projections: []app.ProjectionState{{
			Kind: "search", Status: app.ProjectionStatusPending, TargetVersion: "1", TargetRevision: 2,
			BuildAvailable: true,
			Diagnostic:     &app.ProjectionDiagnostic{Code: hostile, Summary: hostile},
		}},
	}
	services := &servicesStub{projections: status, projectionAction: app.ProjectionAction{
		State: app.EvidenceComplete, Active: true, Status: status,
	}}
	m := New(context.Background(), services)
	m.screen, m.sessionsState.selected = timelineScreen, "session-1"

	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.screen != projectionsScreen || cmd == nil {
		t.Fatalf("open projections = screen %d cmd %v", m.screen, cmd != nil)
	}
	m, poll := updateModel(t, m, cmd().(projectionStatusMsg))
	if poll == nil || !strings.Contains(m.View().Content, "Canonical evidence remains available") {
		t.Fatalf("projection status did not render or poll: %q", m.View().Content)
	}
	if strings.Contains(m.View().Content, "attacker.invalid") {
		t.Fatalf("projection diagnostic was not terminal-safe: %q", m.View().Content)
	}

	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'b', Text: "b"})
	if cmd == nil {
		t.Fatal("rebuild selected returned no command")
	}
	_ = cmd()
	if len(services.rebuildCalls) != 1 || services.rebuildCalls[0] != "search" {
		t.Fatalf("selected rebuild calls = %v", services.rebuildCalls)
	}

	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 't', Text: "t"})
	if cmd == nil {
		t.Fatal("retry returned no command")
	}
	_ = cmd()
	if services.retryCalls != 1 {
		t.Fatalf("retry calls = %d", services.retryCalls)
	}

	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if !m.projectionsState.confirmAll || !strings.Contains(m.View().Content, "Confirm [y]") {
		t.Fatalf("rebuild-all confirmation = %v, view %q", m.projectionsState.confirmAll, m.View().Content)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.projectionsState.confirmAll || len(services.rebuildCalls) != 1 {
		t.Fatal("declining rebuild-all started work")
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	_ = cmd()
	if got := services.rebuildCalls[len(services.rebuildCalls)-1]; got != app.ProjectionKindAll {
		t.Fatalf("confirmed rebuild kind = %q", got)
	}

	generation := m.projectionsState.generation
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.screen != timelineScreen || m.projectionsState.cancel != nil {
		t.Fatal("leaving projection panel did not stop observation")
	}
	updated, staleCmd := updateModel(t, m, pollProjectionsMsg{generation: generation})
	if staleCmd != nil || updated.screen != timelineScreen {
		t.Fatal("stale projection poll revived observation")
	}
}

func TestProjectionActionNotFoundUpdatesPanelWithoutPolling(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	m.screen = projectionsScreen
	m.projectionsState.generation = 3
	m.projectionsState.status = app.ProjectionStatus{
		State:  app.EvidenceComplete,
		Active: true,
		Projections: []app.ProjectionState{{
			Kind: "search", Status: app.ProjectionStatusRunning,
		}},
	}

	updated, cmd := updateModel(t, m, projectionActionMsg{
		generation: 3,
		action:     app.ProjectionAction{State: app.EvidenceNotFound},
	})
	if cmd != nil {
		t.Fatal("not-found projection action scheduled polling")
	}
	if updated.projectionsState.status.State != app.EvidenceNotFound || updated.projectionsState.status.Active {
		t.Fatalf("projection status = %#v", updated.projectionsState.status)
	}
	if got := updated.View().Content; !strings.Contains(got, "no longer available") {
		t.Fatalf("not-found projection view = %q", got)
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
	for _, line := range strings.Split(view.Content, "\n") {
		if width := ansi.StringWidth(line); width > 24 {
			t.Fatalf("narrow line width = %d: %q", width, line)
		}
	}
	_, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("q did not request exit")
	}
}

func TestDynamicTextSanitizesBeforeStyling(t *testing.T) {
	got := sanitizeLines([]string{"safe\x1b]8;;https://attacker.invalid\x07label\x1b]8;;\x07\u202e"})
	if want := "safelabel<U+202E>"; len(got) != 1 || got[0] != want {
		t.Fatalf("sanitizeLines() = %q, want %q", got, want)
	}
}

func TestViewportStoresClampedOffsetAcrossOverscrollAndResize(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	m.screen = detailScreen
	m.sessionsState.selected = "session-1"
	m.width, m.height = 24, 8
	m.detailState.detail = app.EventDetail{
		State: app.EvidenceComplete,
		Event: model.EventSummary{
			ID: "event-1", SessionID: "session-1", Sequence: 1, Kind: model.EventKindMessage, Summary: "long event",
		},
		Payload: model.MessageData{Role: model.MessageRoleAssistant, Text: strings.Repeat("evidence ", 200)},
	}
	m.syncViewports()
	for range 500 {
		m.moveScroll(1)
	}
	if m.detailState.viewport.PastBottom() || !m.detailState.viewport.AtBottom() {
		t.Fatalf("overscroll offset = %d, viewport was not clamped", m.detailState.viewport.YOffset())
	}
	bottom := m.detailState.viewport.YOffset()
	if bottom == 0 {
		t.Fatal("long detail did not produce a scrollable viewport")
	}
	m.moveScroll(-1)
	if got := m.detailState.viewport.YOffset(); got != bottom-1 {
		t.Fatalf("one upward input moved offset to %d, want %d", got, bottom-1)
	}

	updated, _ := updateModel(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	if updated.detailState.viewport.PastBottom() {
		t.Fatalf("resize retained out-of-range offset %d", updated.detailState.viewport.YOffset())
	}
}

func TestHelpOverlayAndBoundaryNavigation(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	m.sessionsState.loading = false
	m.sessionsState.page = app.SessionPage{Sessions: []app.SessionSummary{
		testSession("one"), testSession("two"), testSession("three"),
	}}
	m.restoreSessionSelection()

	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'G', Text: "G"})
	if m.sessionsState.cursor != 2 || m.sessionsState.selected != "three" {
		t.Fatalf("G selection = %d/%q", m.sessionsState.cursor, m.sessionsState.selected)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.sessionsState.cursor != 0 || m.sessionsState.selected != "one" {
		t.Fatalf("g selection = %d/%q", m.sessionsState.cursor, m.sessionsState.selected)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '?', Text: "?"})
	if !m.helpOpen || !strings.Contains(m.View().Content, "Keyboard help") {
		t.Fatal("help overlay did not open")
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.helpOpen || m.screen != sessionsScreen {
		t.Fatal("Esc did not dismiss help before navigating")
	}
}

func TestUnimplementedProjectionActionsAreHonestAndDisabled(t *testing.T) {
	services := &servicesStub{}
	m := New(context.Background(), services)
	m.screen = projectionsScreen
	m.sessionsState.selected = "session-1"
	m.projectionsState.status = app.ProjectionStatus{
		State: app.EvidenceComplete,
		Summary: app.ProjectionSummary{
			Pending: 1, Unimplemented: 1,
		},
		Projections: []app.ProjectionState{{
			Kind: "search", Status: app.ProjectionStatusPending, BuildAvailable: false,
		}},
	}
	if got := m.View().Content; !strings.Contains(got, "not implemented in this build") {
		t.Fatalf("unimplemented projection view = %q", got)
	}
	for _, key := range []tea.KeyPressMsg{{Code: 'b', Text: "b"}, {Code: 't', Text: "t"}, {Code: 'a', Text: "a"}} {
		var cmd tea.Cmd
		m, cmd = updateModel(t, m, key)
		if cmd != nil {
			t.Fatalf("key %q admitted unavailable projection work", key.String())
		}
	}
	if len(services.rebuildCalls) != 0 || services.retryCalls != 0 || m.projectionsState.confirmAll {
		t.Fatalf("unimplemented actions changed work: rebuilds %v retries %d confirm %v",
			services.rebuildCalls, services.retryCalls, m.projectionsState.confirmAll)
	}
	if !strings.Contains(m.View().Content, "disabled") {
		t.Fatalf("disabled action feedback = %q", m.View().Content)
	}
}

func TestSessionsDashboardResponsiveLayoutsAndUnavailableMetrics(t *testing.T) {
	activity := time.Date(2026, 7, 25, 12, 30, 0, 0, time.FixedZone("test", 3*60*60))
	session := testSession("session-with-a-long-identifier")
	session.Title = ""
	session.Preview = "first user request with enough detail to identify the work"
	session.AgentName = "codex"
	session.LastActivityAt = &activity
	m := New(context.Background(), &servicesStub{})
	m.sessionsState.loading = false
	m.sessionsState.overviewLoading = false
	m.sessionsState.overview = app.LibraryOverview{Sessions: 12, Events: 345, Agents: 3, IssueSessions: 2}
	m.sessionsState.page = app.SessionPage{State: app.EvidenceComplete, Sessions: []app.SessionSummary{session}}
	m.restoreSessionSelection()

	tests := []struct {
		name   string
		width  int
		height int
		want   []string
	}{
		{name: "wide", width: 120, height: 30, want: []string{"LAST ACTIVITY ↓", "SESSION / PREVIEW", "CODEX", "2026-07-25 09:30 UTC", "┌"}},
		{name: "medium", width: 90, height: 30, want: []string{"LAST ACTIVITY ↓", "CODEX", "Sessions", "Evidence issues"}},
		{name: "narrow", width: 60, height: 30, want: []string{"Sessions 12 · Events 345 · Agents 3", "CODEX", "first user request"}},
		{name: "short", width: 120, height: 14, want: []string{"Sessions 12 · Events 345 · Agents 3", "CODEX", "first user request"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copy := m
			copy.width, copy.height = test.width, test.height
			content := ansi.Strip(copy.View().Content)
			for _, want := range test.want {
				if !strings.Contains(content, want) {
					t.Fatalf("view missing %q:\n%s", want, content)
				}
			}
			for _, line := range strings.Split(content, "\n") {
				if ansi.StringWidth(line) > test.width {
					t.Fatalf("line width %d exceeds %d: %q", ansi.StringWidth(line), test.width, line)
				}
			}
		})
	}

	m.sessionsState.overviewErr = errors.New("aggregate read failed")
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "— unavailable") || !strings.Contains(content, "first user request") {
		t.Fatalf("overview failure disturbed table or lacked explicit unavailable label:\n%s", content)
	}
}

func TestSessionsDashboardSanitizesHostileFieldsAndRestoresSelection(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	m.width, m.height = 120, 30
	m.sessionsState.loading = false
	m.sessionsState.overviewLoading = false
	m.sessionsState.selected = "safe-two"
	m.sessionsState.page = app.SessionPage{Sessions: []app.SessionSummary{
		{ID: "safe-one", Preview: "\x1b]8;;https://attacker.invalid\aopen\x1b]8;;\a", AgentName: "\x1b[31mcodex", State: app.EvidenceComplete},
		{ID: "safe-two", Title: "selected", AgentName: "claude", State: app.EvidenceComplete},
	}}
	m.restoreSessionSelection()
	if m.sessionsState.cursor != 1 {
		t.Fatalf("restored cursor = %d, want 1", m.sessionsState.cursor)
	}
	content := m.View().Content
	if strings.Contains(content, "attacker.invalid") || strings.Contains(content, "\x1b]8;") {
		t.Fatalf("hostile terminal content survived sanitization: %q", content)
	}
	if !strings.Contains(ansi.Strip(content), "> 202") && !strings.Contains(ansi.Strip(content), "> —") {
		t.Fatalf("focused row marker missing: %q", ansi.Strip(content))
	}
}
