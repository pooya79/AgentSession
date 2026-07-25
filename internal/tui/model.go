package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/sanitization"
)

const (
	pageSize     = app.DefaultPageSize
	pollInterval = 500 * time.Millisecond
)

type screen uint8

const (
	sessionsScreen screen = iota
	indexingScreen
	timelineScreen
	detailScreen
)

type sessionsLoadedMsg struct {
	generation uint64
	page       app.SessionPage
	err        error
}

type timelineLoadedMsg struct {
	generation uint64
	page       app.TimelinePage
	err        error
}

type detailLoadedMsg struct {
	generation uint64
	detail     app.EventDetail
	err        error
}

type importStartedMsg struct {
	generation uint64
	start      app.ImportAllStart
	err        error
}

type importStatusMsg struct {
	generation uint64
	status     app.ImportAllStatus
	err        error
}

type pollImportMsg struct{ generation uint64 }

// Model is the sessions-first AgentSession terminal interface.
type Model struct {
	ctx      context.Context
	services app.Services

	width  int
	height int
	screen screen

	requestGeneration uint64
	requestCtx        context.Context
	requestCancel     context.CancelFunc
	observeGeneration uint64
	observeCtx        context.Context
	observeCancel     context.CancelFunc

	sessions        app.SessionPage
	sessionsLoading bool
	sessionsErr     error
	sessionCursor   int
	sessionCursors  []string
	sessionPage     int
	selectedSession model.SessionID

	timeline        app.TimelinePage
	timelineLoading bool
	timelineErr     error
	eventCursor     int
	timelineCursors []string
	timelinePage    int

	detail        app.EventDetail
	detailLoading bool
	detailErr     error
	scroll        int

	importStatus app.ImportAllStatus
	importErr    error
}

// New creates a terminal model over the shared application services.
func New(ctx context.Context, services app.Services) Model {
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, requestCancel := context.WithCancel(ctx)
	observeCtx, observeCancel := context.WithCancel(ctx)
	return Model{
		ctx:               ctx,
		services:          services,
		requestGeneration: 1,
		requestCtx:        requestCtx,
		requestCancel:     requestCancel,
		observeGeneration: 1,
		observeCtx:        observeCtx,
		observeCancel:     observeCancel,
		sessionsLoading:   services != nil,
		sessionCursors:    []string{""},
		timelineCursors:   []string{""},
		importStatus:      app.ImportAllStatus{Phase: app.ImportAllUnavailable},
	}
}

// Init starts or joins application-owned indexing while independently loading
// the first page of already committed sessions.
func (m Model) Init() tea.Cmd {
	if m.services == nil {
		return nil
	}
	return tea.Batch(
		loadSessions(m.services, m.requestCtx, m.requestGeneration, ""),
		startImportAll(m.services, m.observeGeneration),
	)
}

func loadSessions(services app.Services, ctx context.Context, generation uint64, cursor string) tea.Cmd {
	return func() tea.Msg {
		page, err := services.ListSessions(ctx, app.ListSessionsRequest{Cursor: cursor, Limit: pageSize})
		return sessionsLoadedMsg{generation: generation, page: page, err: err}
	}
}

func loadTimeline(services app.Services, ctx context.Context, generation uint64, sessionID model.SessionID, cursor string) tea.Cmd {
	return func() tea.Msg {
		page, err := services.Timeline(ctx, app.TimelineRequest{SessionID: sessionID, Cursor: cursor, Limit: pageSize})
		return timelineLoadedMsg{generation: generation, page: page, err: err}
	}
}

func loadDetail(services app.Services, ctx context.Context, generation uint64, sessionID model.SessionID, eventID model.EventID) tea.Cmd {
	return func() tea.Msg {
		detail, err := services.EventDetail(ctx, app.EventDetailRequest{
			SessionID: sessionID, EventID: eventID, IncludePayload: true,
		})
		return detailLoadedMsg{generation: generation, detail: detail, err: err}
	}
}

func startImportAll(services app.Services, generation uint64) tea.Cmd {
	return func() tea.Msg {
		// Starting only hands work to the application-owned coordinator. Its
		// lifetime intentionally does not derive from a presentation context.
		start, err := services.StartImportAll(context.Background())
		return importStartedMsg{generation: generation, start: start, err: err}
	}
}

func readImportStatus(services app.Services, ctx context.Context, generation uint64) tea.Cmd {
	return func() tea.Msg {
		status, err := services.ImportAllStatus(ctx)
		return importStatusMsg{generation: generation, status: status, err: err}
	}
}

func pollImport(generation uint64) tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg {
		return pollImportMsg{generation: generation}
	})
}

func (m *Model) replaceRequest() context.Context {
	if m.requestCancel != nil {
		m.requestCancel()
	}
	m.requestGeneration++
	ctx, cancel := context.WithCancel(m.ctx)
	m.requestCtx = ctx
	m.requestCancel = cancel
	return ctx
}

func (m *Model) cancelRequest() {
	if m.requestCancel != nil {
		m.requestCancel()
	}
	m.requestGeneration++
	m.requestCtx = nil
	m.requestCancel = nil
}

func (m *Model) stopObservation() {
	if m.observeCancel != nil {
		m.observeCancel()
	}
	m.observeGeneration++
	m.observeCancel = nil
}

func (m *Model) startObservation() context.Context {
	if m.observeCancel != nil {
		m.observeCancel()
	}
	m.observeGeneration++
	ctx, cancel := context.WithCancel(m.ctx)
	m.observeCtx = ctx
	m.observeCancel = cancel
	return ctx
}

func (m *Model) reloadSessions() tea.Cmd {
	ctx := m.replaceRequest()
	m.sessionsLoading = true
	m.sessionsErr = nil
	cursor := m.sessionCursors[m.sessionPage]
	return loadSessions(m.services, ctx, m.requestGeneration, cursor)
}

func (m *Model) reloadTimeline() tea.Cmd {
	ctx := m.replaceRequest()
	m.timelineLoading = true
	m.timelineErr = nil
	cursor := m.timelineCursors[m.timelinePage]
	return loadTimeline(m.services, ctx, m.requestGeneration, m.selectedSession, cursor)
}

func (m *Model) observeNow() tea.Cmd {
	ctx := m.startObservation()
	return readImportStatus(m.services, ctx, m.observeGeneration)
}

// Update handles navigation, bounded page requests, refreshes, and import
// observation. Generation checks prevent obsolete asynchronous replies from
// replacing newer state.
func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case sessionsLoadedMsg:
		if msg.generation != m.requestGeneration {
			return m, nil
		}
		m.sessionsLoading = false
		m.sessionsErr = visibleError(msg.err)
		if msg.err == nil {
			m.sessions = msg.page
			m.restoreSessionSelection()
		}
		return m, nil
	case timelineLoadedMsg:
		if msg.generation != m.requestGeneration {
			return m, nil
		}
		m.timelineLoading = false
		m.timelineErr = visibleError(msg.err)
		if msg.err == nil {
			m.timeline = msg.page
			if m.eventCursor >= len(m.timeline.Events) {
				m.eventCursor = max(0, len(m.timeline.Events)-1)
			}
		}
		return m, nil
	case detailLoadedMsg:
		if msg.generation != m.requestGeneration {
			return m, nil
		}
		m.detailLoading = false
		m.detailErr = visibleError(msg.err)
		if msg.err == nil {
			m.detail = msg.detail
		}
		return m, nil
	case importStartedMsg:
		if msg.generation != m.observeGeneration {
			return m, nil
		}
		m.importErr = visibleError(msg.err)
		if msg.err != nil {
			m.importStatus = app.ImportAllStatus{Phase: app.ImportAllUnavailable, Failure: msg.err.Error()}
			return m, nil
		}
		m.importStatus = msg.start.Status
		if m.importStatus.Active {
			return m, pollImport(m.observeGeneration)
		}
		return m, nil
	case pollImportMsg:
		if msg.generation != m.observeGeneration || m.observeCancel == nil {
			return m, nil
		}
		ctx, cancel := context.WithCancel(m.ctx)
		m.observeCancel()
		m.observeCtx = ctx
		m.observeCancel = cancel
		return m, readImportStatus(m.services, ctx, m.observeGeneration)
	case importStatusMsg:
		if msg.generation != m.observeGeneration {
			return m, nil
		}
		wasActive := m.importStatus.Active
		m.importErr = visibleError(msg.err)
		if msg.err != nil {
			m.importStatus = app.ImportAllStatus{Phase: app.ImportAllUnavailable, Failure: msg.err.Error()}
			return m, nil
		}
		m.importStatus = msg.status
		if msg.status.Active {
			return m, pollImport(m.observeGeneration)
		}
		if wasActive && m.screen == sessionsScreen {
			return m, m.reloadSessions()
		}
		return m, nil
	case tea.KeyPressMsg:
		updated, cmd := m.handleKey(msg.String())
		if pointer, ok := updated.(*Model); ok {
			return *pointer, cmd
		}
		return updated, cmd
	}
	return m, nil
}

func visibleError(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (m *Model) restoreSessionSelection() {
	if len(m.sessions.Sessions) == 0 {
		m.sessionCursor = 0
		return
	}
	if m.selectedSession != "" {
		for i, session := range m.sessions.Sessions {
			if session.ID == m.selectedSession {
				m.sessionCursor = i
				return
			}
		}
	}
	if m.sessionCursor >= len(m.sessions.Sessions) {
		m.sessionCursor = len(m.sessions.Sessions) - 1
	}
	m.selectedSession = m.sessions.Sessions[m.sessionCursor].ID
}

func (m Model) handleKey(key string) (tea.Model, tea.Cmd) {
	if key == "ctrl+c" || key == "q" {
		if m.requestCancel != nil {
			m.requestCancel()
		}
		m.stopObservation()
		return m, tea.Quit
	}

	switch key {
	case "esc":
		return m.back()
	case "i":
		if m.screen == sessionsScreen {
			m.screen = indexingScreen
			m.scroll = 0
			if m.observeCancel == nil {
				return m, m.observeNow()
			}
		}
	case "r":
		return m.refresh()
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup":
		if m.screen == detailScreen || m.screen == indexingScreen {
			m.moveScroll(-m.pageStep())
		} else {
			return m.previousPage()
		}
	case "pgdown":
		if m.screen == detailScreen || m.screen == indexingScreen {
			m.moveScroll(m.pageStep())
		} else {
			return m.nextPage()
		}
	case "p":
		return m.previousPage()
	case "n":
		return m.nextPage()
	case "enter":
		return m.openSelection()
	}
	return m, nil
}

func (m *Model) back() (tea.Model, tea.Cmd) {
	switch m.screen {
	case indexingScreen:
		m.screen = sessionsScreen
		return m, m.reloadSessions()
	case timelineScreen:
		m.screen = sessionsScreen
		m.timeline = app.TimelinePage{}
		m.timelineErr = nil
		m.timelineLoading = false
		return m, tea.Batch(m.reloadSessions(), m.observeNow())
	case detailScreen:
		m.cancelRequest()
		m.screen = timelineScreen
		m.detail = app.EventDetail{}
		m.detailErr = nil
		m.detailLoading = false
		m.scroll = 0
	}
	return m, nil
}

func (m *Model) refresh() (tea.Model, tea.Cmd) {
	switch m.screen {
	case sessionsScreen, indexingScreen:
		load := m.reloadSessions()
		m.startObservation()
		return m, tea.Batch(load, startImportAll(m.services, m.observeGeneration))
	case timelineScreen:
		return m, m.reloadTimeline()
	case detailScreen:
		if len(m.timeline.Events) == 0 {
			return m, nil
		}
		ctx := m.replaceRequest()
		m.detailLoading, m.detailErr, m.scroll = true, nil, 0
		event := m.timeline.Events[m.eventCursor]
		return m, loadDetail(m.services, ctx, m.requestGeneration, m.selectedSession, event.ID)
	}
	return m, nil
}

func (m *Model) move(delta int) {
	switch m.screen {
	case sessionsScreen:
		if len(m.sessions.Sessions) > 0 {
			m.sessionCursor = clamp(m.sessionCursor+delta, 0, len(m.sessions.Sessions)-1)
			m.selectedSession = m.sessions.Sessions[m.sessionCursor].ID
		}
	case timelineScreen:
		if len(m.timeline.Events) > 0 {
			m.eventCursor = clamp(m.eventCursor+delta, 0, len(m.timeline.Events)-1)
		}
	case detailScreen, indexingScreen:
		m.moveScroll(delta)
	}
}

func (m *Model) moveScroll(delta int) {
	m.scroll = max(0, m.scroll+delta)
}

func (m Model) pageStep() int {
	return max(1, m.contentHeight()-2)
}

func (m *Model) nextPage() (tea.Model, tea.Cmd) {
	switch m.screen {
	case sessionsScreen:
		if m.sessions.NextCursor == "" || m.sessionsLoading {
			return m, nil
		}
		if m.sessionPage+1 == len(m.sessionCursors) {
			m.sessionCursors = append(m.sessionCursors, m.sessions.NextCursor)
		} else {
			m.sessionCursors[m.sessionPage+1] = m.sessions.NextCursor
			m.sessionCursors = m.sessionCursors[:m.sessionPage+2]
		}
		m.sessionPage++
		m.sessionCursor = 0
		m.selectedSession = ""
		return m, m.reloadSessions()
	case timelineScreen:
		if m.timeline.NextCursor == "" || m.timelineLoading {
			return m, nil
		}
		if m.timelinePage+1 == len(m.timelineCursors) {
			m.timelineCursors = append(m.timelineCursors, m.timeline.NextCursor)
		} else {
			m.timelineCursors[m.timelinePage+1] = m.timeline.NextCursor
			m.timelineCursors = m.timelineCursors[:m.timelinePage+2]
		}
		m.timelinePage++
		m.eventCursor = 0
		return m, m.reloadTimeline()
	}
	return m, nil
}

func (m *Model) previousPage() (tea.Model, tea.Cmd) {
	switch m.screen {
	case sessionsScreen:
		if m.sessionPage == 0 || m.sessionsLoading {
			return m, nil
		}
		m.sessionPage--
		m.sessionCursor = 0
		m.selectedSession = ""
		return m, m.reloadSessions()
	case timelineScreen:
		if m.timelinePage == 0 || m.timelineLoading {
			return m, nil
		}
		m.timelinePage--
		m.eventCursor = 0
		return m, m.reloadTimeline()
	}
	return m, nil
}

func (m *Model) openSelection() (tea.Model, tea.Cmd) {
	switch m.screen {
	case sessionsScreen:
		if m.sessionsLoading || len(m.sessions.Sessions) == 0 {
			return m, nil
		}
		m.selectedSession = m.sessions.Sessions[m.sessionCursor].ID
		m.screen = timelineScreen
		m.timeline = app.TimelinePage{}
		m.timelineErr = nil
		m.timelineLoading = true
		m.timelinePage = 0
		m.timelineCursors = []string{""}
		m.eventCursor = 0
		m.stopObservation()
		ctx := m.replaceRequest()
		return m, loadTimeline(m.services, ctx, m.requestGeneration, m.selectedSession, "")
	case timelineScreen:
		if m.timelineLoading || len(m.timeline.Events) == 0 {
			return m, nil
		}
		m.screen = detailScreen
		m.detail = app.EventDetail{}
		m.detailErr = nil
		m.detailLoading = true
		m.scroll = 0
		ctx := m.replaceRequest()
		event := m.timeline.Events[m.eventCursor]
		return m, loadDetail(m.services, ctx, m.requestGeneration, m.selectedSession, event.ID)
	}
	return m, nil
}

// View renders a bounded, terminal-safe representation of the current screen.
func (m Model) View() tea.View {
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}

	lines := []string{"AgentSession  ·  local · offline · read-only", m.indexSummary(), ""}
	switch m.screen {
	case sessionsScreen:
		lines = append(lines, m.sessionsLines()...)
	case indexingScreen:
		lines = append(lines, m.indexingLines()...)
	case timelineScreen:
		lines = append(lines, m.timelineLines()...)
	case detailScreen:
		lines = append(lines, m.detailLines()...)
	}
	lines = append(lines, "", m.helpLine(width))

	view := terminalView(fit(lines, width, height))
	view.AltScreen = true
	return view
}

func (m Model) indexSummary() string {
	status := m.importStatus
	switch {
	case m.importErr != nil:
		return "Index: unavailable — " + m.importErr.Error()
	case status.Active:
		return fmt.Sprintf("Index: indexing · %d/%d sources · %d records · %d diagnostics",
			status.SourcesCompleted, status.SourcesDiscovered, status.RecordsProcessed, status.DiagnosticsTotal)
	case status.Phase == app.ImportAllUpToDate:
		if status.SourcesDiscovered == 0 {
			return "Index: complete · no supported sources found"
		}
		return fmt.Sprintf("Index: complete · %d sources · %d sessions", status.SourcesCompleted, status.SessionsObserved)
	case status.Phase == app.ImportAllIssues:
		return fmt.Sprintf("Index: completed with issues · %d failed · %d diagnostics",
			status.SourcesFailed, status.DiagnosticsTotal)
	default:
		if status.Failure != "" {
			return "Index: unavailable — " + status.Failure
		}
		return "Index: status unavailable"
	}
}

func (m Model) sessionsLines() []string {
	lines := []string{"Imported sessions"}
	switch {
	case m.sessionsLoading && len(m.sessions.Sessions) == 0:
		return append(lines, "", "Loading imported sessions…")
	case m.sessionsErr != nil:
		return append(lines, "", "Could not load sessions: "+m.sessionsErr.Error(), "Press r to retry.")
	case len(m.sessions.Sessions) == 0:
		lines = append(lines, "", "No imported sessions are available.")
		if m.importStatus.Active {
			lines = append(lines, "Indexing continues in the background; this list refreshes when it completes.")
		} else if m.importStatus.SourcesDiscovered == 0 && m.importStatus.Phase == app.ImportAllUpToDate {
			lines = append(lines, "No supported sources were discovered. Press r to rescan.")
		}
		return lines
	}
	if m.sessions.State == app.EvidencePartial {
		lines = append(lines, "Some sessions contain diagnostics; available evidence is still shown.")
	} else if m.sessions.State == app.EvidenceUnavailable {
		lines = append(lines, "Session evidence is unavailable.")
	}
	lines = append(lines, "")
	visible := max(1, m.contentHeight()-len(lines))
	start := windowStart(m.sessionCursor, len(m.sessions.Sessions), visible)
	end := min(len(m.sessions.Sessions), start+visible)
	for i := start; i < end; i++ {
		session := m.sessions.Sessions[i]
		marker := " "
		if i == m.sessionCursor {
			marker = ">"
		}
		title := session.Title
		if strings.TrimSpace(title) == "" {
			title = string(session.ID)
		}
		lines = append(lines, fmt.Sprintf("%s %s  ·  %d events  ·  %s", marker, title, session.EventCount, evidenceLabel(session.State)))
	}
	lines = append(lines, fmt.Sprintf("Page %d · %d shown%s", m.sessionPage+1, len(m.sessions.Sessions), nextLabel(m.sessions.NextCursor)))
	return lines
}

func (m Model) timelineLines() []string {
	lines := []string{"Timeline · session " + string(m.selectedSession)}
	switch {
	case m.timelineLoading && len(m.timeline.Events) == 0:
		return append(lines, "", "Loading event summaries…")
	case m.timelineErr != nil:
		return append(lines, "", "Could not load timeline: "+m.timelineErr.Error(), "Press r to retry.")
	case m.timeline.State == app.EvidenceNotFound:
		return append(lines, "", "This session is no longer available.")
	case len(m.timeline.Events) == 0:
		if m.timeline.State == app.EvidenceUnavailable {
			return append(lines, "", "Timeline evidence is unavailable.", diagnosticSummary(m.timeline.Diagnostics))
		}
		return append(lines, "", "This session has no normalized events.")
	}
	if m.timeline.State == app.EvidencePartial {
		lines = append(lines, diagnosticSummary(m.timeline.Diagnostics))
	}
	lines = append(lines, "")
	visible := max(1, m.contentHeight()-len(lines))
	start := windowStart(m.eventCursor, len(m.timeline.Events), visible)
	end := min(len(m.timeline.Events), start+visible)
	for i := start; i < end; i++ {
		event := m.timeline.Events[i]
		marker := " "
		if i == m.eventCursor {
			marker = ">"
		}
		lines = append(lines, fmt.Sprintf("%s #%d  %-13s  %s", marker, event.Sequence, event.Kind, event.Summary))
	}
	lines = append(lines, fmt.Sprintf("Page %d · %d shown%s", m.timelinePage+1, len(m.timeline.Events), nextLabel(m.timeline.NextCursor)))
	return lines
}

func (m Model) detailLines() []string {
	lines := []string{"Event detail · session " + string(m.selectedSession)}
	switch {
	case m.detailLoading:
		return append(lines, "", "Loading normalized payload…")
	case m.detailErr != nil:
		return append(lines, "", "Could not load event: "+m.detailErr.Error(), "Press r to retry.")
	case m.detail.State == app.EvidenceNotFound:
		return append(lines, "", "This event is no longer available.")
	}
	event := m.detail.Event
	lines = append(lines,
		fmt.Sprintf("#%d · %s · %s", event.Sequence, event.Kind, evidenceLabel(m.detail.State)),
		event.Summary,
	)
	if m.detail.Diagnostics.Total > 0 {
		lines = append(lines, diagnosticSummary(m.detail.Diagnostics))
		for _, diagnostic := range m.detail.Diagnostics.Diagnostics {
			lines = append(lines, fmt.Sprintf("[%s] %s: %s", diagnostic.Severity, diagnostic.Code, diagnostic.Message))
		}
	}
	lines = append(lines, "", "Normalized payload")
	if m.detail.State == app.EvidenceUnavailable || m.detail.Payload == nil {
		lines = append(lines, "Payload evidence is unavailable.")
		return scrollLines(wrapDetailLines(lines, m.renderWidth()), m.scroll, m.contentHeight())
	}
	payload, err := json.MarshalIndent(m.detail.Payload, "", "  ")
	if err != nil {
		lines = append(lines, "Could not render normalized payload: "+err.Error())
	} else {
		lines = append(lines, strings.Split(string(payload), "\n")...)
	}
	return scrollLines(wrapDetailLines(lines, m.renderWidth()), m.scroll, m.contentHeight())
}

func (m Model) indexingLines() []string {
	status := m.importStatus
	lines := []string{"Indexing details", ""}
	switch status.Phase {
	case app.ImportAllIndexing:
		lines = append(lines, "Indexing is active. It continues while you browse imported sessions.")
	case app.ImportAllUpToDate:
		lines = append(lines, "Indexing completed successfully.")
	case app.ImportAllIssues:
		lines = append(lines, "Indexing completed with issues; available evidence was retained.")
	default:
		lines = append(lines, "Indexing is unavailable.")
	}
	if status.Failure != "" {
		lines = append(lines, "Failure: "+status.Failure)
	}
	lines = append(lines,
		fmt.Sprintf("Sources: %d discovered · %d completed · %d failed", status.SourcesDiscovered, status.SourcesCompleted, status.SourcesFailed),
		fmt.Sprintf("Totals: %d records · %d events · %d sessions · %d unchanged", status.RecordsProcessed, status.EventsProcessed, status.SessionsObserved, status.UnchangedSessions),
		fmt.Sprintf("Diagnostics: %d total · %d omitted from bounded status", status.DiagnosticsTotal, status.DiagnosticsOmitted),
	)
	if len(status.Sources) == 0 {
		lines = append(lines, "", "No discovered sources.")
	} else {
		lines = append(lines, "", "Discovered sources")
		for _, source := range status.Sources {
			state := string(source.Phase)
			if source.Failure != "" {
				state += " · failed: " + source.Failure
			}
			lines = append(lines,
				fmt.Sprintf("%s · %s · %s", source.Kind, source.ID, state),
				fmt.Sprintf("  %s · %s · %d records · %d events · %d sessions · %d diagnostics",
					source.Path, source.Origin, source.Records, source.Events, source.Sessions, source.Diagnostics),
			)
		}
	}
	if len(status.RecentDiagnostics) > 0 {
		lines = append(lines, "", "Recent discovery/import diagnostics")
		for _, diagnostic := range status.RecentDiagnostics {
			where := string(diagnostic.SourceID)
			if where == "" {
				where = diagnostic.SourcePath
			}
			lines = append(lines, fmt.Sprintf("[%s] %s · %s · %s", diagnostic.Severity, diagnostic.Code, where, diagnostic.Message))
		}
	}
	return scrollLines(wrapDetailLines(lines, m.renderWidth()), m.scroll, m.contentHeight())
}

func (m Model) contentHeight() int {
	height := m.height
	if height <= 0 {
		height = 24
	}
	return max(3, height-5)
}

func (m Model) renderWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}

func (m Model) helpLine(width int) string {
	if width < 55 || m.height > 0 && m.height < 12 {
		return "↑↓ move · Enter open · Esc back · q quit"
	}
	switch m.screen {
	case sessionsScreen:
		return "↑/↓ or j/k move · Enter open · n/p page · i indexing · r rescan · q quit"
	case indexingScreen:
		return "↑/↓ or j/k scroll · PgUp/PgDn scroll · Esc sessions · r rescan · q quit"
	case timelineScreen:
		return "↑/↓ or j/k move · Enter detail · n/p page · Esc sessions · r reload · q quit"
	default:
		return "↑/↓ or j/k scroll · PgUp/PgDn scroll · Esc timeline · r reload · q quit"
	}
}

func evidenceLabel(state app.EvidenceState) string {
	switch state {
	case app.EvidenceComplete:
		return "complete"
	case app.EvidencePartial:
		return "partial evidence"
	case app.EvidenceUnavailable:
		return "unavailable evidence"
	case app.EvidenceNotFound:
		return "not found"
	default:
		return "unknown evidence"
	}
}

func diagnosticSummary(synopsis app.DiagnosticSynopsis) string {
	if synopsis.Total == 0 {
		return ""
	}
	return fmt.Sprintf("Partial evidence: %d diagnostic(s), %d omitted.", synopsis.Total, synopsis.Omitted)
}

func nextLabel(cursor string) string {
	if cursor != "" {
		return " · more available"
	}
	return ""
}

func scrollLines(lines []string, offset, height int) []string {
	if len(lines) <= height {
		return lines
	}
	offset = clamp(offset, 0, max(0, len(lines)-height))
	end := min(len(lines), offset+height)
	result := append([]string(nil), lines[offset:end]...)
	if offset > 0 && len(result) > 0 {
		result[0] = "↑ more"
	}
	if end < len(lines) && len(result) > 0 {
		result[len(result)-1] = "↓ more"
	}
	return result
}

func windowStart(selected, total, visible int) int {
	if total <= visible {
		return 0
	}
	start := selected - visible/2
	return clamp(start, 0, total-visible)
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func fit(lines []string, width, height int) string {
	width = max(1, width)
	height = max(1, height)
	selected := lines
	if len(lines) > height {
		selected = append([]string(nil), lines[:max(0, height-1)]...)
		selected = append(selected, lines[len(lines)-1])
	}
	fitted := make([]string, 0, min(len(selected), height))
	for _, line := range selected {
		// Sanitize before measuring: source-controlled escape sequences must not
		// affect width calculations or composition.
		line = sanitization.Terminal(line)
		fitted = append(fitted, ansi.Truncate(line, width, "…"))
		if len(fitted) == height {
			break
		}
	}
	return strings.Join(fitted, "\n")
}

func wrapDetailLines(lines []string, width int) []string {
	width = max(1, width)
	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		line = sanitization.Terminal(line)
		parts := strings.Split(ansi.Wordwrap(line, width, " \t/"), "\n")
		wrapped = append(wrapped, parts...)
	}
	return wrapped
}

// terminalView is the mandatory final sanitization boundary for TUI content.
func terminalView(content string) tea.View {
	return tea.NewView(sanitization.Terminal(content))
}

// Run opens the interactive terminal interface over application-owned
// services. The command context controls presentation lifetime only.
func Run(ctx context.Context, services app.Services) error {
	if services == nil {
		return fmt.Errorf("tui: application services are required")
	}
	if ctx == nil {
		return fmt.Errorf("tui: context is required")
	}
	_, err := tea.NewProgram(New(ctx, services), tea.WithContext(ctx)).Run()
	return err
}
