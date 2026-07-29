package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/sanitization"
)

const (
	// pageSize deliberately matches the application default so both
	// presentation layers traverse evidence with the same bounded requests.
	pageSize = app.DefaultPageSize
	// pollInterval keeps progress responsive without continuously reading the
	// coordinator's shared status.
	pollInterval = 500 * time.Millisecond
)

// screen identifies the active presentation without encoding navigation or
// business logic into the application service layer.
type screen uint8

const (
	sessionsScreen screen = iota
	indexingScreen
	timelineScreen
	detailScreen
	projectionsScreen
	searchScreen
)

// sessionsLoadedMsg carries one bounded sessions page and the generation of
// the request that produced it.
type sessionsLoadedMsg struct {
	generation uint64
	page       app.SessionPage
	err        error
}

// overviewLoadedMsg carries library aggregates separately from the session
// page so a failed aggregate read does not hide usable session evidence.
type overviewLoadedMsg struct {
	generation uint64
	overview   app.LibraryOverview
	err        error
}

// timelineLoadedMsg carries one bounded page of lightweight event summaries.
type timelineLoadedMsg struct {
	generation uint64
	cursor     string
	page       app.TimelinePage
	err        error
}

// detailLoadedMsg carries the only exploration response that includes a
// normalized payload.
type detailLoadedMsg struct {
	generation uint64
	detail     app.EventDetail
	err        error
}

type unknownEvidenceLoadedMsg struct {
	generation uint64
	eventID    model.EventID
	inspection app.UnknownEvidenceInspection
	err        error
}

// importStartedMsg reports whether the UI started or joined the shared
// application-owned import-all workflow.
type importStartedMsg struct {
	generation uint64
	start      app.ImportAllStart
	err        error
}

// importStatusMsg is a point-in-time observation of shared indexing work.
type importStatusMsg struct {
	generation uint64
	status     app.ImportAllStatus
	err        error
}

// pollImportMsg schedules one status read. It does not represent a repeating
// timer; another tick is scheduled only while the observed run remains active.
type pollImportMsg struct{ generation uint64 }

// projectionStatusMsg carries an observer-owned snapshot and generation.
type projectionStatusMsg struct {
	generation uint64
	status     app.ProjectionStatus
	err        error
}

// projectionActionMsg acknowledges work transferred to the application.
type projectionActionMsg struct {
	generation uint64
	action     app.ProjectionAction
	err        error
}

type searchLoadedMsg struct {
	generation uint64
	page       app.SearchPage
	err        error
}

// pollProjectionsMsg schedules one observation; it is not a repeating timer.
type pollProjectionsMsg struct{ generation uint64 }

// Model is the sessions-first AgentSession terminal interface.
type Model struct {
	// ctx owns presentation requests only. Starting import-all deliberately
	// uses an independent context because navigation must not cancel indexing.
	ctx      context.Context
	services app.Services

	width  int
	height int
	screen screen

	// Requests and import observation have independent lifetimes. Generations
	// reject responses from commands canceled by later navigation or refreshes.
	requestGeneration uint64
	requestCtx        context.Context
	requestCancel     context.CancelFunc
	observeGeneration uint64
	observeCtx        context.Context
	observeCancel     context.CancelFunc

	// Screen states own their page, selection, errors, and viewport. They are
	// embedded so the root coordinator remains concise while retaining one
	// Bubble Tea model and one explicit navigation graph.
	sessionsState
	timelineState
	detailState
	indexingState
	projectionsState
	searchState

	theme         theme
	helpOpen      bool
	helpViewport  viewport.Model
	spinner       spinner.Model
	spinnerActive bool
}

// New creates a terminal model over the shared application services.
func New(ctx context.Context, services app.Services) Model {
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, requestCancel := context.WithCancel(ctx)
	observeCtx, observeCancel := context.WithCancel(ctx)
	detailViewport := viewport.New()
	detailViewport.SoftWrap = true
	indexViewport := viewport.New()
	indexViewport.SoftWrap = true
	helpViewport := viewport.New()
	helpViewport.SoftWrap = true
	timelineViewport := viewport.New()
	return Model{
		ctx:               ctx,
		services:          services,
		requestGeneration: 1,
		requestCtx:        requestCtx,
		requestCancel:     requestCancel,
		observeGeneration: 1,
		observeCtx:        observeCtx,
		observeCancel:     observeCancel,
		sessionsState: sessionsState{
			loading:         services != nil,
			overviewLoading: services != nil,
			cursors:         []string{""},
		},
		timelineState: timelineState{
			requestedCursors:  make(map[string]bool),
			expanded:          make(map[model.EventID]bool),
			inspections:       make(map[model.EventID]app.UnknownEvidenceInspection),
			inspectionErrors:  make(map[model.EventID]error),
			inspectionLoading: make(map[model.EventID]bool),
			viewport:          timelineViewport,
		},
		detailState: detailState{viewport: detailViewport},
		indexingState: indexingState{
			status:   app.ImportAllStatus{Phase: app.ImportAllUnavailable},
			viewport: indexViewport,
		},
		projectionsState: projectionsState{generation: 1},
		theme:            newTheme(true),
		helpViewport:     helpViewport,
		spinner:          spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		spinnerActive:    services != nil,
	}
}

// Init starts or joins application-owned indexing while independently loading
// the first page of already committed sessions.
func (m Model) Init() tea.Cmd {
	if m.services == nil {
		return tea.RequestBackgroundColor
	}
	return tea.Batch(
		loadSessions(m.requestCtx, m.services, m.requestGeneration, ""),
		loadOverview(m.requestCtx, m.services, m.requestGeneration),
		startImportAll(m.services, m.observeGeneration),
		tea.RequestBackgroundColor,
		m.spinner.Tick,
	)
}

// loadOverview requests exact committed-evidence totals independently of the
// paginated session list.
func loadOverview(ctx context.Context, services app.Services, generation uint64) tea.Cmd {
	return func() tea.Msg {
		overview, err := services.LibraryOverview(ctx)
		return overviewLoadedMsg{generation: generation, overview: overview, err: err}
	}
}

// loadSessions requests one opaque-cursor page of imported session summaries.
func loadSessions(ctx context.Context, services app.Services, generation uint64, cursor string) tea.Cmd {
	return func() tea.Msg {
		page, err := services.ListSessions(ctx, app.ListSessionsRequest{Cursor: cursor, Limit: pageSize})
		return sessionsLoadedMsg{generation: generation, page: page, err: err}
	}
}

func loadSearch(ctx context.Context, services app.Services, generation uint64, query, cursor string) tea.Cmd {
	return func() tea.Msg {
		page, err := services.Search(ctx, app.SearchRequest{Query: query, Cursor: cursor, Limit: pageSize})
		return searchLoadedMsg{generation: generation, page: page, err: err}
	}
}

// loadTimeline opts into normalized payloads for this bounded TUI chunk.
// Retained raw records remain excluded.
func loadTimeline(ctx context.Context, services app.Services, generation uint64, sessionID model.SessionID, cursor string) tea.Cmd {
	return func() tea.Msg {
		page, err := services.Timeline(ctx, app.TimelineRequest{
			SessionID: sessionID, Cursor: cursor, Limit: pageSize, IncludePayloads: true,
		})
		return timelineLoadedMsg{generation: generation, cursor: cursor, page: page, err: err}
	}
}

// loadDetail requests a normalized payload for the selected event. Raw record
// contents are intentionally absent from the exploration response and UI.
func loadDetail(ctx context.Context, services app.Services, generation uint64, sessionID model.SessionID, eventID model.EventID) tea.Cmd {
	return func() tea.Msg {
		detail, err := services.EventDetail(ctx, app.EventDetailRequest{
			SessionID: sessionID, EventID: eventID, IncludePayload: true,
		})
		return detailLoadedMsg{generation: generation, detail: detail, err: err}
	}
}

// loadUnknownEvidence performs the explicit bounded inspection action. Its
// generation prevents a response from leaking into a later detail screen.
func loadUnknownEvidence(ctx context.Context, services app.Services, generation uint64, sessionID model.SessionID, eventID model.EventID) tea.Cmd {
	return func() tea.Msg {
		inspection, err := services.InspectUnknownEvidence(ctx, sessionID, eventID)
		return unknownEvidenceLoadedMsg{generation: generation, eventID: eventID, inspection: inspection, err: err}
	}
}

// startImportAll transfers work to the application-owned coordinator. The
// presentation observes that work but never owns or cancels it.
func startImportAll(services app.Services, generation uint64) tea.Cmd {
	return func() tea.Msg {
		// Starting only hands work to the application-owned coordinator. Its
		// lifetime intentionally does not derive from a presentation context.
		start, err := services.StartImportAll(context.Background())
		return importStartedMsg{generation: generation, start: start, err: err}
	}
}

// readImportStatus obtains one snapshot using an observer-owned context.
func readImportStatus(ctx context.Context, services app.Services, generation uint64) tea.Cmd {
	return func() tea.Msg {
		status, err := services.ImportAllStatus(ctx)
		return importStatusMsg{generation: generation, status: status, err: err}
	}
}

// pollImport creates a single delayed observation message.
func pollImport(generation uint64) tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg {
		return pollImportMsg{generation: generation}
	})
}

// readProjectionStatus obtains one snapshot owned by the projection panel's
// current observation generation.
func readProjectionStatus(ctx context.Context, services app.Services, generation uint64, sessionID model.SessionID) tea.Cmd {
	return func() tea.Msg {
		status, err := services.ProjectionStatus(ctx, sessionID)
		return projectionStatusMsg{generation: generation, status: status, err: err}
	}
}

// projectionAction uses a detached presentation context because the
// application validates and takes ownership of admitted work. Panel navigation
// must not cancel that admission request.
func projectionAction(services app.Services, generation uint64, sessionID model.SessionID, kind string, retry bool) tea.Cmd {
	return func() tea.Msg {
		var action app.ProjectionAction
		var err error
		if retry {
			action, err = services.RetryProjections(context.Background(), sessionID)
		} else {
			action, err = services.RebuildProjections(context.Background(), sessionID, kind)
		}
		return projectionActionMsg{generation: generation, action: action, err: err}
	}
}

// pollProjections creates one delayed observation message. A handler schedules
// another only while application-owned projection work remains active.
func pollProjections(generation uint64) tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg {
		return pollProjectionsMsg{generation: generation}
	})
}

// replaceRequest cancels the previous evidence read and advances its
// generation before returning a context for the replacement request.
func (m *Model) replaceRequest() context.Context {
	if m.requestCancel != nil {
		m.requestCancel()
	}
	m.clearSupersededInspections()
	m.requestGeneration++
	ctx, cancel := context.WithCancel(m.ctx)
	m.requestCtx = ctx
	m.requestCancel = cancel
	return ctx
}

// cancelRequest invalidates an evidence read without starting another one.
func (m *Model) cancelRequest() {
	if m.requestCancel != nil {
		m.requestCancel()
	}
	m.clearSupersededInspections()
	m.requestGeneration++
	m.requestCtx = nil
	m.requestCancel = nil
}

// stopObservation detaches the presentation from indexing progress. It does
// not call into the application coordinator or stop import work.
func (m *Model) stopObservation() {
	if m.observeCancel != nil {
		m.observeCancel()
	}
	m.observeGeneration++
	m.observeCancel = nil
}

// startObservation replaces any prior status observer and advances the status
// generation so delayed ticks cannot revive an obsolete polling loop.
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

// startProjectionObservation replaces the panel's prior read context and
// advances its generation so delayed responses cannot overwrite newer state.
func (m *Model) startProjectionObservation() context.Context {
	if m.projectionsState.cancel != nil {
		m.projectionsState.cancel()
	}
	m.projectionsState.generation++
	ctx, cancel := context.WithCancel(m.ctx)
	m.projectionsState.cancel = cancel
	return ctx
}

// stopProjectionObservation detaches the panel without stopping application
// projection work.
func (m *Model) stopProjectionObservation() {
	if m.projectionsState.cancel != nil {
		m.projectionsState.cancel()
	}
	m.projectionsState.generation++
	m.projectionsState.cancel = nil
}

// startSpinner pairs newly admitted asynchronous work with at most one spinner
// tick loop.
func (m *Model) startSpinner(cmd tea.Cmd) tea.Cmd {
	if cmd == nil || m.spinnerActive {
		return cmd
	}
	m.spinnerActive = true
	return tea.Batch(cmd, m.spinner.Tick)
}

// observeProjectionsNow starts one immediate status read; subsequent reads are
// scheduled only while projectionPolling reports active work.
func (m *Model) observeProjectionsNow() tea.Cmd {
	ctx := m.startProjectionObservation()
	m.projectionsState.loading, m.projectionsState.err = true, nil
	return m.startSpinner(readProjectionStatus(ctx, m.services, m.projectionsState.generation, m.sessionsState.selected))
}

// runProjectionAction marks the panel busy while the application accepts or
// joins projection work.
func (m *Model) runProjectionAction(kind string, retry bool) tea.Cmd {
	m.projectionsState.loading = true
	return m.startSpinner(projectionAction(
		m.services, m.projectionsState.generation, m.sessionsState.selected, kind, retry,
	))
}

// reloadSessions replaces the current page request while retaining its cursor.
func (m *Model) reloadSessions() tea.Cmd {
	ctx := m.replaceRequest()
	m.sessionsState.loading = true
	m.sessionsState.err = nil
	m.sessionsState.overviewLoading = true
	m.sessionsState.overviewErr = nil
	cursor := m.sessionsState.cursors[m.sessionsState.pageNumber]
	return m.startSpinner(tea.Batch(
		loadSessions(ctx, m.services, m.requestGeneration, cursor),
		loadOverview(ctx, m.services, m.requestGeneration),
	))
}

// reloadTimeline refreshes the first chunk. Existing cards remain visible if
// the replacement read fails.
func (m *Model) reloadTimeline() tea.Cmd {
	ctx := m.replaceRequest()
	m.timelineState.loading = true
	m.timelineState.err = nil
	m.timelineState.pendingCursor = ""
	m.invalidateTimelineRender()
	return m.startSpinner(loadTimeline(ctx, m.services, m.requestGeneration, m.sessionsState.selected, ""))
}

// observeNow starts a fresh observer and immediately reads indexing status.
func (m *Model) observeNow() tea.Cmd {
	ctx := m.startObservation()
	return readImportStatus(ctx, m.services, m.observeGeneration)
}

// Update handles navigation, bounded page requests, refreshes, and import
// observation. Generation checks prevent obsolete asynchronous replies from
// replacing newer state.
func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.syncViewports()
		return m, nil
	case tea.BackgroundColorMsg:
		m.theme = newTheme(msg.IsDark())
		m.spinner.Style = m.theme.info
		return m, nil
	case spinner.TickMsg:
		if !m.spinnerActive {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if !m.busy() {
			m.spinnerActive = false
			cmd = nil
		}
		return m, cmd
	case sessionsLoadedMsg:
		if msg.generation != m.requestGeneration {
			return m, nil
		}
		m.sessionsState.loading = false
		m.sessionsState.err = visibleError(msg.err)
		if msg.err == nil {
			m.sessionsState.page = msg.page
			m.restoreSessionSelection()
		}
		return m, nil
	case overviewLoadedMsg:
		if msg.generation != m.requestGeneration {
			return m, nil
		}
		m.sessionsState.overviewLoading = false
		m.sessionsState.overviewErr = visibleError(msg.err)
		if msg.err == nil {
			m.sessionsState.overview = msg.overview
		}
		return m, nil
	case timelineLoadedMsg:
		if msg.generation != m.requestGeneration {
			return m, nil
		}
		m.timelineState.loading = false
		m.timelineState.pendingCursor = ""
		m.timelineState.err = visibleError(msg.err)
		if msg.err != nil && msg.cursor != "" {
			delete(m.timelineState.requestedCursors, msg.cursor)
		}
		if msg.err == nil {
			selected := m.timelineState.selected
			if msg.cursor == "" {
				m.timelineState.page = msg.page
				m.timelineState.requestedCursors = map[string]bool{"": true}
			} else {
				m.appendTimelinePage(msg.page)
			}
			for index, event := range m.timelineState.page.Events {
				if event.ID == selected {
					m.timelineState.cursor = index
					break
				}
			}
			if m.timelineState.cursor >= len(m.timelineState.page.Events) {
				m.timelineState.cursor = max(0, len(m.timelineState.page.Events)-1)
			}
			if len(m.timelineState.page.Events) > 0 {
				m.timelineState.selected = m.timelineState.page.Events[m.timelineState.cursor].ID
			}
		}
		m.invalidateTimelineRender()
		m.syncViewports()
		return m, nil
	case detailLoadedMsg:
		if msg.generation != m.requestGeneration {
			return m, nil
		}
		m.detailState.loading = false
		m.detailState.err = visibleError(msg.err)
		if msg.err == nil {
			m.detailState.detail = msg.detail
		}
		m.syncViewports()
		return m, nil
	case unknownEvidenceLoadedMsg:
		if msg.generation != m.requestGeneration {
			return m, nil
		}
		if m.timelineState.inspectionLoading[msg.eventID] {
			delete(m.timelineState.inspectionLoading, msg.eventID)
			m.timelineState.inspectionErrors[msg.eventID] = visibleError(msg.err)
			if msg.err == nil {
				m.timelineState.inspections[msg.eventID] = msg.inspection
				m.timelineState.expanded[msg.eventID] = true
			}
			m.invalidateTimelineRender()
			m.syncViewports()
			return m, nil
		}
		if m.screen != detailScreen || m.detailState.detail.Event.ID != msg.eventID {
			return m, nil
		}
		m.detailState.inspectionLoading = false
		m.detailState.inspectionErr = visibleError(msg.err)
		if msg.err == nil {
			m.detailState.inspection = msg.inspection
		}
		m.syncViewports()
		return m, nil
	case importStartedMsg:
		if msg.generation != m.observeGeneration {
			return m, nil
		}
		m.indexingState.err = visibleError(msg.err)
		if msg.err != nil {
			m.indexingState.status = app.ImportAllStatus{Phase: app.ImportAllUnavailable, Failure: msg.err.Error()}
			m.syncViewports()
			return m, nil
		}
		m.indexingState.status = msg.start.Status
		m.syncViewports()
		if m.indexingState.status.Active {
			return m, m.startSpinner(pollImport(m.observeGeneration))
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
		return m, readImportStatus(ctx, m.services, m.observeGeneration)
	case importStatusMsg:
		if msg.generation != m.observeGeneration {
			return m, nil
		}
		wasActive := m.indexingState.status.Active
		m.indexingState.err = visibleError(msg.err)
		if msg.err != nil {
			m.indexingState.status = app.ImportAllStatus{Phase: app.ImportAllUnavailable, Failure: msg.err.Error()}
			m.syncViewports()
			return m, nil
		}
		m.indexingState.status = msg.status
		m.syncViewports()
		if msg.status.Active {
			return m, m.startSpinner(pollImport(m.observeGeneration))
		}
		if wasActive && m.screen == sessionsScreen {
			return m, m.reloadSessions()
		}
		return m, nil
	case projectionStatusMsg:
		if msg.generation != m.projectionsState.generation {
			return m, nil
		}
		m.projectionsState.loading = false
		m.projectionsState.err = visibleError(msg.err)
		if msg.err == nil {
			m.projectionsState.status = msg.status
			m.projectionsState.actionNotice = ""
			if m.projectionsState.cursor >= len(msg.status.Projections) {
				m.projectionsState.cursor = max(0, len(msg.status.Projections)-1)
			}
			if projectionPolling(msg.status) {
				return m, m.startSpinner(pollProjections(m.projectionsState.generation))
			}
		}
		return m, nil
	case projectionActionMsg:
		if msg.generation != m.projectionsState.generation {
			return m, nil
		}
		m.projectionsState.loading = false
		m.projectionsState.err = visibleError(msg.err)
		if msg.err == nil {
			m.projectionsState.status = msg.action.Status
			m.projectionsState.status.State = msg.action.State
			m.projectionsState.status.Active = msg.action.Active
			if msg.action.State == app.EvidenceNotFound {
				return m, nil
			}
			if msg.action.Joined {
				m.projectionsState.actionNotice = "Joined projection work already owned by the application."
			} else {
				m.projectionsState.actionNotice = "Projection work accepted; it continues after leaving this screen."
			}
			return m, m.startSpinner(pollProjections(m.projectionsState.generation))
		}
		return m, nil
	case searchLoadedMsg:
		if msg.generation != m.requestGeneration {
			return m, nil
		}
		m.searchState.loading = false
		m.searchState.err = visibleError(msg.err)
		if msg.err == nil {
			m.searchState.page = msg.page
			if m.searchState.cursor >= len(msg.page.Results) {
				m.searchState.cursor = max(0, len(msg.page.Results)-1)
			}
		}
		return m, nil
	case pollProjectionsMsg:
		if msg.generation != m.projectionsState.generation || m.projectionsState.cancel == nil || m.screen != projectionsScreen {
			return m, nil
		}
		ctx := m.startProjectionObservation()
		m.projectionsState.loading = true
		return m, m.startSpinner(readProjectionStatus(ctx, m.services, m.projectionsState.generation, m.sessionsState.selected))
	case tea.KeyPressMsg:
		updated, cmd := m.handleKey(msg.String())
		if pointer, ok := updated.(*Model); ok {
			return *pointer, cmd
		}
		return updated, cmd
	}
	return m, nil
}

// busy reports whether any visible request or application-owned workflow needs
// spinner animation.
func (m Model) busy() bool {
	return m.sessionsState.loading || m.sessionsState.overviewLoading || m.timelineState.loading || m.detailState.loading || m.detailState.inspectionLoading ||
		m.projectionsState.loading || m.projectionsState.status.Active || m.indexingState.status.Active || m.searchState.loading
}

// visibleError suppresses expected cancellation from superseded presentation
// requests while retaining actionable service failures.
func visibleError(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// restoreSessionSelection prefers stable session identity over row position
// when a refreshed page still contains the previously selected session.
func (m *Model) restoreSessionSelection() {
	if len(m.sessionsState.page.Sessions) == 0 {
		m.sessionsState.cursor = 0
		return
	}
	if m.sessionsState.selected != "" {
		for i, session := range m.sessionsState.page.Sessions {
			if session.ID == m.sessionsState.selected {
				m.sessionsState.cursor = i
				return
			}
		}
	}
	if m.sessionsState.cursor >= len(m.sessionsState.page.Sessions) {
		m.sessionsState.cursor = len(m.sessionsState.page.Sessions) - 1
	}
	m.sessionsState.selected = m.sessionsState.page.Sessions[m.sessionsState.cursor].ID
}

// handleKey maps the documented controls to screen-aware navigation.
func (m Model) handleKey(key string) (tea.Model, tea.Cmd) {
	if m.screen == searchScreen && m.searchState.editing {
		switch key {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.searchState.editing = false
		case "enter":
			m.searchState.editing = false
			return m, m.runSearch("")
		case "backspace":
			runes := []rune(m.searchState.query)
			if len(runes) > 0 {
				m.searchState.query = string(runes[:len(runes)-1])
			}
		case "space":
			m.searchState.query += " "
		default:
			if len([]rune(key)) == 1 && len(m.searchState.query)+len(key) <= 4096 {
				m.searchState.query += key
			}
		}
		return m, nil
	}
	if key == "ctrl+c" || key == "q" {
		if m.requestCancel != nil {
			m.requestCancel()
		}
		m.stopObservation()
		m.stopProjectionObservation()
		return m, tea.Quit
	}
	if key == "?" {
		m.helpOpen = !m.helpOpen
		if m.helpOpen {
			m.syncViewports()
			m.helpViewport.GotoTop()
		}
		return m, nil
	}
	if m.helpOpen {
		switch key {
		case "esc":
			m.helpOpen = false
		case "up", "k":
			m.syncViewports()
			m.helpViewport.ScrollUp(1)
		case "down", "j":
			m.syncViewports()
			m.helpViewport.ScrollDown(1)
		case "pgup":
			m.syncViewports()
			m.helpViewport.PageUp()
		case "pgdown":
			m.syncViewports()
			m.helpViewport.PageDown()
		case "home", "g":
			m.helpViewport.GotoTop()
		case "end", "G":
			m.helpViewport.GotoBottom()
		}
		return m, nil
	}
	if m.projectionsState.confirmAll {
		switch key {
		case "esc", "n":
			m.projectionsState.confirmAll = false
			return m, nil
		case "y":
			m.projectionsState.confirmAll = false
			return m, m.runProjectionAction(app.ProjectionKindAll, false)
		}
		return m, nil
	}
	if m.services == nil {
		return m, nil
	}

	switch key {
	case "esc":
		return m.back()
	case "/":
		m.screen = searchScreen
		m.searchState.editing = true
		m.searchState.err = nil
		m.stopObservation()
		return m, nil
	case "i":
		if m.screen == sessionsScreen {
			m.screen = indexingScreen
			m.syncViewports()
			m.indexingState.viewport.GotoTop()
			if m.observeCancel == nil {
				return m, m.observeNow()
			}
		}
	case "x":
		if m.screen == timelineScreen {
			m.screen = projectionsScreen
			m.projectionsState.cursor = 0
			m.projectionsState.confirmAll = false
			return m, m.observeProjectionsNow()
		}
	case "r":
		return m.refresh()
	case "u":
		if m.screen == timelineScreen && len(m.timelineState.page.Events) > 0 {
			event := m.timelineState.page.Events[m.timelineState.cursor]
			if event.Kind == model.EventKindUnknown && !m.timelineState.inspectionLoading[event.ID] {
				ctx := m.replaceRequest()
				m.timelineState.inspectionErrors[event.ID] = nil
				m.timelineState.inspectionLoading[event.ID] = true
				m.invalidateTimelineRender()
				return m, m.startSpinner(loadUnknownEvidence(
					ctx, m.services, m.requestGeneration, m.sessionsState.selected, event.ID,
				))
			}
		}
		if m.screen == detailScreen && m.detailState.detail.Event.Kind == model.EventKindUnknown && !m.detailState.inspectionLoading {
			ctx := m.replaceRequest()
			m.detailState.inspection = app.UnknownEvidenceInspection{}
			m.detailState.inspectionErr = nil
			m.detailState.inspectionLoading = true
			return m, m.startSpinner(loadUnknownEvidence(ctx, m.services, m.requestGeneration, m.sessionsState.selected, m.detailState.detail.Event.ID))
		}
	case "t":
		if m.screen == projectionsScreen {
			if !m.retryAvailable() {
				m.projectionsState.actionNotice = "No implemented pending or failed projection can be retried."
				return m, nil
			}
			return m, m.runProjectionAction("", true)
		}
	case "b":
		if m.screen == projectionsScreen && len(m.projectionsState.status.Projections) > 0 {
			selected := m.projectionsState.status.Projections[m.projectionsState.cursor]
			if !selected.BuildAvailable {
				m.projectionsState.actionNotice = "This projection is not implemented in this build."
				return m, nil
			}
			kind := selected.Kind
			return m, m.runProjectionAction(kind, false)
		}
	case "a":
		if m.screen == projectionsScreen {
			if !m.rebuildAllAvailable() {
				m.projectionsState.actionNotice = "Rebuild all is disabled until every registered projection is implemented."
				return m, nil
			}
			m.projectionsState.confirmAll = true
		}
	case "n":
		if m.screen == timelineScreen {
			return m, nil
		}
		return m.nextPage()
	case "up", "k":
		m.move(-1)
		return m, m.prefetchTimelineIfNeeded()
	case "down", "j":
		m.move(1)
		return m, m.prefetchTimelineIfNeeded()
	case "home", "g":
		m.moveToBoundary(false)
		return m, m.prefetchTimelineIfNeeded()
	case "end", "G":
		m.moveToBoundary(true)
		return m, m.prefetchTimelineIfNeeded()
	case "pgup":
		if m.screen == timelineScreen {
			m.syncViewports()
			m.timelineState.viewport.ScrollUp(m.pageStep())
		} else if m.screen == detailScreen || m.screen == indexingScreen {
			m.moveScroll(-m.pageStep())
		} else {
			return m.previousPage()
		}
	case "pgdown":
		if m.screen == timelineScreen {
			m.syncViewports()
			m.timelineState.viewport.ScrollDown(m.pageStep())
			return m, m.prefetchTimelineIfNeeded()
		} else if m.screen == detailScreen || m.screen == indexingScreen {
			m.moveScroll(m.pageStep())
		} else {
			return m.nextPage()
		}
	case "p":
		if m.screen == timelineScreen {
			return m, nil
		}
		return m.previousPage()
	case "enter":
		return m.openSelection()
	}
	return m, nil
}

func (m *Model) runSearch(cursor string) tea.Cmd {
	ctx := m.replaceRequest()
	m.searchState.loading = true
	m.searchState.err = nil
	m.searchState.cursor = 0
	return m.startSpinner(loadSearch(ctx, m.services, m.requestGeneration, m.searchState.query, cursor))
}

// retryAvailable reports whether at least one pending or failed projection has
// a builder in the current runtime.
func (m Model) retryAvailable() bool {
	for _, state := range m.projectionsState.status.Projections {
		if state.BuildAvailable && (state.Status == app.ProjectionStatusPending || state.Status == app.ProjectionStatusFailed) {
			return true
		}
	}
	return false
}

// rebuildAllAvailable requires every registered projection kind to have a
// builder, preventing a bulk action from promising work this binary cannot do.
func (m Model) rebuildAllAvailable() bool {
	if len(m.projectionsState.status.Projections) == 0 {
		return false
	}
	for _, state := range m.projectionsState.status.Projections {
		if !state.BuildAvailable {
			return false
		}
	}
	return true
}

// back performs screen-specific cleanup before restoring the parent screen.
// Returning to sessions always refreshes committed imports.
func (m *Model) back() (tea.Model, tea.Cmd) {
	switch m.screen {
	case searchScreen:
		m.cancelRequest()
		m.screen = sessionsScreen
		m.searchState.editing = false
		return m, tea.Batch(m.reloadSessions(), m.observeNow())
	case indexingScreen:
		m.screen = sessionsScreen
		return m, m.reloadSessions()
	case timelineScreen:
		m.screen = sessionsScreen
		m.resetTimelineState()
		return m, tea.Batch(m.reloadSessions(), m.observeNow())
	case detailScreen:
		m.cancelRequest()
		m.screen = timelineScreen
		m.detailState.detail = app.EventDetail{}
		m.detailState.err = nil
		m.detailState.loading = false
		m.detailState.inspection = app.UnknownEvidenceInspection{}
		m.detailState.inspectionErr = nil
		m.detailState.inspectionLoading = false
		m.detailState.viewport.GotoTop()
	case projectionsScreen:
		m.stopProjectionObservation()
		m.screen = timelineScreen
		m.projectionsState.status = app.ProjectionStatus{}
		m.projectionsState.err = nil
		m.projectionsState.confirmAll = false
		m.projectionsState.actionNotice = ""
	}
	return m, nil
}

// refresh rescans from overview screens and reloads only the current evidence
// on timeline/detail screens.
func (m *Model) refresh() (tea.Model, tea.Cmd) {
	switch m.screen {
	case searchScreen:
		return m, m.runSearch("")
	case sessionsScreen, indexingScreen:
		load := m.reloadSessions()
		m.startObservation()
		return m, tea.Batch(load, startImportAll(m.services, m.observeGeneration))
	case timelineScreen:
		return m, m.reloadTimeline()
	case projectionsScreen:
		return m, m.observeProjectionsNow()
	case detailScreen:
		if len(m.timelineState.page.Events) == 0 {
			return m, nil
		}
		ctx := m.replaceRequest()
		m.detailState.loading, m.detailState.err = true, nil
		m.detailState.inspection = app.UnknownEvidenceInspection{}
		m.detailState.inspectionErr = nil
		m.detailState.inspectionLoading = false
		m.detailState.viewport.GotoTop()
		event := m.timelineState.page.Events[m.timelineState.cursor]
		return m, m.startSpinner(loadDetail(ctx, m.services, m.requestGeneration, m.sessionsState.selected, event.ID))
	}
	return m, nil
}

// move changes selection within the active list screen and keeps stable
// session identity synchronized with the sessions cursor.
func (m *Model) move(delta int) {
	switch m.screen {
	case searchScreen:
		if len(m.searchState.page.Results) > 0 {
			m.searchState.cursor = clamp(m.searchState.cursor+delta, 0, len(m.searchState.page.Results)-1)
		}
	case sessionsScreen:
		if len(m.sessionsState.page.Sessions) > 0 {
			m.sessionsState.cursor = clamp(m.sessionsState.cursor+delta, 0, len(m.sessionsState.page.Sessions)-1)
			m.sessionsState.selected = m.sessionsState.page.Sessions[m.sessionsState.cursor].ID
		}
	case timelineScreen:
		if len(m.timelineState.page.Events) > 0 {
			m.timelineState.cursor = clamp(m.timelineState.cursor+delta, 0, len(m.timelineState.page.Events)-1)
			m.timelineState.selected = m.timelineState.page.Events[m.timelineState.cursor].ID
			m.invalidateTimelineRender()
			m.syncViewports()
		}
	case projectionsScreen:
		if len(m.projectionsState.status.Projections) > 0 {
			m.projectionsState.cursor = clamp(m.projectionsState.cursor+delta, 0, len(m.projectionsState.status.Projections)-1)
		}
	case detailScreen, indexingScreen:
		m.moveScroll(delta)
	}
}

// moveToBoundary jumps to the first or last row, or to the corresponding
// viewport boundary on long-form screens.
func (m *Model) moveToBoundary(last bool) {
	switch m.screen {
	case searchScreen:
		if len(m.searchState.page.Results) == 0 {
			return
		}
		m.searchState.cursor = 0
		if last {
			m.searchState.cursor = len(m.searchState.page.Results) - 1
		}
	case sessionsScreen:
		if len(m.sessionsState.page.Sessions) == 0 {
			return
		}
		m.sessionsState.cursor = 0
		if last {
			m.sessionsState.cursor = len(m.sessionsState.page.Sessions) - 1
		}
		m.sessionsState.selected = m.sessionsState.page.Sessions[m.sessionsState.cursor].ID
	case timelineScreen:
		if len(m.timelineState.page.Events) == 0 {
			return
		}
		m.timelineState.cursor = 0
		if last {
			m.timelineState.cursor = len(m.timelineState.page.Events) - 1
		}
		m.timelineState.selected = m.timelineState.page.Events[m.timelineState.cursor].ID
		m.invalidateTimelineRender()
		m.syncViewports()
	case projectionsScreen:
		if len(m.projectionsState.status.Projections) == 0 {
			return
		}
		m.projectionsState.cursor = 0
		if last {
			m.projectionsState.cursor = len(m.projectionsState.status.Projections) - 1
		}
	case detailScreen:
		m.syncViewports()
		if last {
			m.detailState.viewport.GotoBottom()
		} else {
			m.detailState.viewport.GotoTop()
		}
	case indexingScreen:
		m.syncViewports()
		if last {
			m.indexingState.viewport.GotoBottom()
		} else {
			m.indexingState.viewport.GotoTop()
		}
	}
}

// moveScroll delegates clamping to the viewport so repeated input cannot leave
// a latent offset beyond the rendered content.
func (m *Model) moveScroll(delta int) {
	m.syncViewports()
	target := &m.detailState.viewport
	if m.screen == indexingScreen {
		target = &m.indexingState.viewport
	}
	if delta < 0 {
		target.ScrollUp(-delta)
	} else {
		target.ScrollDown(delta)
	}
}

// pageStep derives a viewport-sized jump while always making forward progress.
func (m Model) pageStep() int {
	return max(1, m.contentHeight())
}

// nextPage records the opaque next cursor before requesting the following
// bounded page.
func (m *Model) nextPage() (tea.Model, tea.Cmd) {
	switch m.screen {
	case searchScreen:
		if m.searchState.page.NextCursor == "" || m.searchState.loading {
			return m, nil
		}
		return m, m.runSearch(m.searchState.page.NextCursor)
	case sessionsScreen:
		if m.sessionsState.page.NextCursor == "" || m.sessionsState.loading {
			return m, nil
		}
		if m.sessionsState.pageNumber+1 == len(m.sessionsState.cursors) {
			m.sessionsState.cursors = append(m.sessionsState.cursors, m.sessionsState.page.NextCursor)
		} else {
			m.sessionsState.cursors[m.sessionsState.pageNumber+1] = m.sessionsState.page.NextCursor
			m.sessionsState.cursors = m.sessionsState.cursors[:m.sessionsState.pageNumber+2]
		}
		m.sessionsState.pageNumber++
		m.sessionsState.cursor = 0
		m.sessionsState.selected = ""
		return m, m.reloadSessions()
	}
	return m, nil
}

// previousPage reuses a cursor retained when the page was first visited.
func (m *Model) previousPage() (tea.Model, tea.Cmd) {
	switch m.screen {
	case searchScreen:
		if m.searchState.page.PreviousCursor == "" || m.searchState.loading {
			return m, nil
		}
		return m, m.runSearch(m.searchState.page.PreviousCursor)
	case sessionsScreen:
		if m.sessionsState.pageNumber == 0 || m.sessionsState.loading {
			return m, nil
		}
		m.sessionsState.pageNumber--
		m.sessionsState.cursor = 0
		m.sessionsState.selected = ""
		return m, m.reloadSessions()
	}
	return m, nil
}

// openSelection transitions from sessions to summaries or from a summary to
// its payload-bearing detail. Entering evidence stops indexing observation.
func (m *Model) openSelection() (tea.Model, tea.Cmd) {
	switch m.screen {
	case searchScreen:
		if m.searchState.loading || len(m.searchState.page.Results) == 0 {
			return m, nil
		}
		result := m.searchState.page.Results[m.searchState.cursor]
		m.sessionsState.selected = result.SessionID
		m.timelineState.page = app.TimelinePage{State: app.EvidenceComplete, Events: []model.EventSummary{{
			ID: result.EventID, SessionID: result.SessionID, Sequence: result.Sequence,
			Timestamp: result.Timestamp, Kind: result.Kind, Summary: result.Summary,
		}}}
		m.timelineState.cursor = 0
		m.invalidateTimelineRender()
		m.screen = detailScreen
		m.detailState.detail = app.EventDetail{}
		m.detailState.loading = true
		ctx := m.replaceRequest()
		return m, m.startSpinner(loadDetail(ctx, m.services, m.requestGeneration, result.SessionID, result.EventID))
	case sessionsScreen:
		if m.sessionsState.loading || len(m.sessionsState.page.Sessions) == 0 {
			return m, nil
		}
		m.sessionsState.selected = m.sessionsState.page.Sessions[m.sessionsState.cursor].ID
		m.screen = timelineScreen
		m.resetTimelineState()
		m.timelineState.loading = true
		m.stopObservation()
		ctx := m.replaceRequest()
		return m, m.startSpinner(loadTimeline(ctx, m.services, m.requestGeneration, m.sessionsState.selected, ""))
	case timelineScreen:
		if len(m.timelineState.page.Events) == 0 {
			return m, nil
		}
		event := m.timelineState.page.Events[m.timelineState.cursor]
		if timelinePayloadTruncatable(m.timelineState.page.Payloads[event.ID]) ||
			m.timelineState.inspections[event.ID].EventID != "" {
			m.timelineState.expanded[event.ID] = !m.timelineState.expanded[event.ID]
			m.invalidateTimelineRender()
			m.syncViewports()
		}
		return m, m.prefetchTimelineIfNeeded()
	}
	return m, nil
}

// View renders the active screen, applying sanitization before styling and
// fitting the result to the current terminal dimensions.
func (m Model) View() tea.View {
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}

	snapshot := m
	snapshot.syncViewports()
	header := "AgentSession  /  " + snapshot.screenLabel() + "  ·  local · offline · read-only"
	if width < 40 {
		header = "AgentSession / " + snapshot.screenLabel()
	}
	lines := []string{header, snapshot.indexSummary(), ""}
	var body []string
	if snapshot.helpOpen {
		body = strings.Split(snapshot.helpViewport.View(), "\n")
	} else {
		switch snapshot.screen {
		case sessionsScreen:
			body = snapshot.sessionsLines()
		case indexingScreen:
			body = strings.Split(snapshot.indexingState.viewport.View(), "\n")
		case timelineScreen:
			body = snapshot.timelineLines()
		case detailScreen:
			body = strings.Split(snapshot.detailState.viewport.View(), "\n")
		case projectionsScreen:
			body = snapshot.projectionLines()
		case searchScreen:
			body = snapshot.searchLines()
		}
	}
	lines = append(lines, body...)
	lines = append(lines, "", snapshot.helpLine(width))
	lines = snapshot.styleLines(lines)

	view := terminalView(fit(lines, width, height))
	view.AltScreen = true
	view.WindowTitle = "AgentSession"
	return view
}

func (m Model) screenLabel() string {
	switch m.screen {
	case sessionsScreen:
		return "Sessions"
	case indexingScreen:
		return "Indexing"
	case timelineScreen:
		return "Timeline"
	case detailScreen:
		return "Event"
	case projectionsScreen:
		return "Projections"
	case searchScreen:
		return "Search"
	default:
		return "Explorer"
	}
}

// indexSummary renders the last observed lifecycle state persistently across
// every screen.
func (m Model) indexSummary() string {
	status := m.indexingState.status
	switch {
	case m.indexingState.err != nil:
		return "CANONICAL INDEX · UNAVAILABLE — " + m.indexingState.err.Error()
	case status.Active:
		return fmt.Sprintf("%s CANONICAL INDEX · INDEXING · %d/%d sources · %d records · %d diagnostics",
			m.spinner.View(), status.SourcesCompleted, status.SourcesDiscovered, status.RecordsProcessed, status.DiagnosticsTotal)
	case status.Phase == app.ImportAllUpToDate:
		if status.SourcesDiscovered == 0 {
			return "CANONICAL INDEX · COMPLETE · no supported sources found"
		}
		return fmt.Sprintf("CANONICAL INDEX · COMPLETE · %d sources · %d sessions", status.SourcesCompleted, status.SessionsObserved)
	case status.Phase == app.ImportAllIssues:
		return fmt.Sprintf("CANONICAL INDEX · COMPLETED WITH ISSUES · %d failed · %d diagnostics",
			status.SourcesFailed, status.DiagnosticsTotal)
	default:
		if status.Failure != "" {
			return "CANONICAL INDEX · UNAVAILABLE — " + status.Failure
		}
		return "CANONICAL INDEX · STATUS UNAVAILABLE"
	}
}

// sessionsLines renders the current bounded sessions page and its evidence
// quality without exposing source-specific data.
func (m Model) sessionsLines() []string {
	lines := []string{"Sessions dashboard"}
	lines = append(lines, m.metricsLines()...)
	switch {
	case m.sessionsState.loading && len(m.sessionsState.page.Sessions) == 0:
		return append(lines, "", m.spinner.View()+" Loading imported sessions…")
	case m.sessionsState.err != nil && len(m.sessionsState.page.Sessions) == 0:
		return append(lines, "", "Could not load sessions: "+m.sessionsState.err.Error(), "Press r to retry.")
	case len(m.sessionsState.page.Sessions) == 0:
		lines = append(lines, "", "No imported sessions are available.")
		if m.indexingState.status.Active {
			lines = append(lines, "Indexing continues in the background; this list refreshes when it completes.")
		} else if m.indexingState.status.SourcesDiscovered == 0 && m.indexingState.status.Phase == app.ImportAllUpToDate {
			lines = append(lines, "No supported sources were discovered. Press r to rescan.")
		}
		return lines
	}
	if m.sessionsState.err != nil {
		lines = append(lines, "Refresh failed; showing the last loaded page. Press r to retry.")
	} else if m.sessionsState.loading {
		lines = append(lines, m.spinner.View()+" Refreshing sessions; current evidence remains visible.")
	}
	switch m.sessionsState.page.State {
	case app.EvidencePartial:
		lines = append(lines, "Some sessions contain diagnostics; available evidence is still shown.")
	case app.EvidenceUnavailable:
		lines = append(lines, "Session evidence is unavailable.")
	}
	lines = append(lines, "")
	compactLayout := m.renderWidth() < 72 || m.height > 0 && m.height < 18
	rowHeight := 1
	if m.renderWidth() < 110 {
		rowHeight = 2
	}
	if compactLayout {
		rowHeight = 3
	}
	visible := max(1, (m.contentHeight()-len(lines)-1)/rowHeight)
	start := windowStart(m.sessionsState.cursor, len(m.sessionsState.page.Sessions), visible)
	end := min(len(m.sessionsState.page.Sessions), start+visible)
	if !compactLayout && m.renderWidth() >= 110 {
		lines = append(lines, "  LAST ACTIVITY ↓       AGENT       SESSION / PREVIEW                         EVENTS  CANONICAL  DERIVED")
	} else if !compactLayout {
		lines = append(lines, "  LAST ACTIVITY ↓       AGENT       SESSION / PREVIEW")
	}
	for i := start; i < end; i++ {
		session := m.sessionsState.page.Sessions[i]
		marker := " "
		if i == m.sessionsState.cursor {
			marker = ">"
		}
		title := sessionLabel(session)
		agent := strings.ToUpper(session.AgentName)
		activity := formatActivity(session.LastActivityAt)
		derived := compactDerived(session.Projections)
		if compactLayout {
			lines = append(lines, fmt.Sprintf("%s %s", marker, truncateCell(title, max(1, m.renderWidth()-2))))
			lines = append(lines, fmt.Sprintf("  %s · %s · %d events · canonical %s · %s",
				activity, agent, session.EventCount, evidenceLabel(session.State), derived))
			if session.Preview != "" && session.Preview != title {
				lines = append(lines, "  "+truncateCell(session.Preview, max(1, m.renderWidth()-2)))
			} else {
				lines = append(lines, "")
			}
		} else if m.renderWidth() < 110 {
			lines = append(lines, fmt.Sprintf("%s %-21s %-11s %s",
				marker, truncateCell(activity, 21), truncateCell(agent, 11),
				truncateCell(title, max(1, m.renderWidth()-38))))
			lines = append(lines, fmt.Sprintf("  %d events · canonical %s · %s%s",
				session.EventCount, evidenceLabel(session.State), derived, previewSuffix(session, m.renderWidth()-46)))
		} else {
			label := title
			if session.Preview != "" && session.Preview != title {
				label += " — " + session.Preview
			}
			lines = append(lines, fmt.Sprintf("%s %-21s %-11s %-41s %6d  %-9s  %s",
				marker, truncateCell(activity, 21), truncateCell(agent, 11),
				truncateCell(label, 41), session.EventCount, evidenceLabel(session.State), derived))
		}
	}
	lines = append(lines, fmt.Sprintf("Page %d · %d shown%s", m.sessionsState.pageNumber+1, len(m.sessionsState.page.Sessions), nextLabel(m.sessionsState.page.NextCursor)))
	return lines
}

func (m Model) searchLines() []string {
	prompt := "Query: " + m.searchState.query
	if m.searchState.editing {
		prompt += "█"
	}
	lines := []string{"Search canonical evidence", prompt}
	if m.searchState.loading {
		return append(lines, "", m.spinner.View()+" Searching…")
	}
	if m.searchState.err != nil {
		return append(lines, "", "Search could not be completed: "+m.searchState.err.Error())
	}
	availability := m.searchState.page.Availability
	switch m.searchState.page.State {
	case app.EvidenceUnavailable:
		lines = append(lines, "Unavailable · no session has a current ready search index")
	case app.EvidencePartial:
		lines = append(lines, fmt.Sprintf("Partial · %d/%d sessions searchable", availability.Usable, availability.Sessions))
	default:
		lines = append(lines, fmt.Sprintf("Complete · %d sessions searchable", availability.Usable))
	}
	if len(m.searchState.page.Results) == 0 {
		return append(lines, "", "No matching evidence.")
	}
	lines = append(lines, "")
	for index, result := range m.searchState.page.Results {
		prefix := "  "
		if index == m.searchState.cursor {
			prefix = "> "
		}
		snippet := strings.Join(strings.Fields(sanitization.Terminal(result.Snippet)), " ")
		lines = append(lines,
			fmt.Sprintf("%s%s · %s · sequence %d", prefix, result.Kind, result.SessionID, result.Sequence),
			"    "+sanitization.Terminal(result.Summary),
			"    "+snippet,
		)
	}
	return lines
}

func (m Model) metricsLines() []string {
	labels := []string{"Sessions", "Events", "Agents", "Evidence issues"}
	values := []int64{m.sessionsState.overview.Sessions, m.sessionsState.overview.Events, m.sessionsState.overview.Agents, m.sessionsState.overview.IssueSessions}
	render := func(index, width int) string {
		value := fmt.Sprintf("%d", values[index])
		if m.sessionsState.overviewLoading {
			value = "…"
		} else if m.sessionsState.overviewErr != nil {
			value = "— unavailable"
		}
		return "┌" + strings.Repeat("─", max(1, width-2)) + "┐\n" +
			"│" + padCell(" "+labels[index], max(1, width-2)) + "│\n" +
			"│" + padCell(" "+value, max(1, width-2)) + "│\n" +
			"└" + strings.Repeat("─", max(1, width-2)) + "┘"
	}
	width := m.renderWidth()
	if width < 72 || m.height > 0 && m.height < 18 {
		parts := make([]string, len(labels))
		for i := range labels {
			value := fmt.Sprintf("%d", values[i])
			if m.sessionsState.overviewLoading {
				value = "…"
			} else if m.sessionsState.overviewErr != nil {
				value = "— unavailable"
			}
			parts[i] = labels[i] + " " + value
		}
		return []string{strings.Join(parts, " · ")}
	}
	columns := 4
	if width < 110 {
		columns = 2
	}
	cardWidth := max(12, (width-(columns-1))/columns)
	rows := make([]string, 0, 6)
	for start := 0; start < len(labels); start += columns {
		cards := make([][]string, 0, columns)
		for i := start; i < min(len(labels), start+columns); i++ {
			cards = append(cards, strings.Split(render(i, cardWidth), "\n"))
		}
		for line := 0; line < 4; line++ {
			parts := make([]string, len(cards))
			for i := range cards {
				parts[i] = cards[i][line]
			}
			rows = append(rows, strings.Join(parts, " "))
		}
	}
	return rows
}

func sessionLabel(session app.SessionSummary) string {
	if strings.TrimSpace(session.Title) != "" {
		return session.Title
	}
	if session.Preview != "" {
		return session.Preview
	}
	return truncateCell(string(session.ID), 28)
}

func previewSuffix(session app.SessionSummary, width int) string {
	if session.Preview == "" || session.Preview == sessionLabel(session) || width <= 4 {
		return ""
	}
	return " · " + truncateCell(session.Preview, width)
}

func formatActivity(value *time.Time) string {
	if value == nil {
		return "—"
	}
	return value.UTC().Format("2006-01-02 15:04 UTC")
}

func compactDerived(summary app.ProjectionSummary) string {
	result := fmt.Sprintf("derived %d usable", summary.Usable)
	if summary.Unimplemented > 0 {
		result += fmt.Sprintf(", %d n/a", summary.Unimplemented)
	}
	return result
}

func truncateCell(value string, width int) string {
	value = sanitization.Terminal(value)
	if ansi.StringWidth(value) <= width {
		return value
	}
	if width <= 1 {
		return ansi.Truncate(value, width, "")
	}
	return ansi.Truncate(value, width-1, "") + "…"
}

func padCell(value string, width int) string {
	value = truncateCell(value, width)
	return value + strings.Repeat(" ", max(0, width-ansi.StringWidth(value)))
}

// timelineHeaderLines is shared by viewport sizing and timeline rendering so
// optional diagnostics and continuation failures reserve exactly their rows.
func (m Model) timelineHeaderLines() []string {
	lines := []string{"Timeline · session " + string(m.sessionsState.selected)}
	for _, session := range m.sessionsState.page.Sessions {
		if session.ID == m.sessionsState.selected {
			lines = append(lines, fmt.Sprintf("Canonical evidence: %s · projections: %d usable, %d pending, %d running, %d failed, %d stale",
				evidenceLabel(session.State), session.Projections.Usable, session.Projections.Pending,
				session.Projections.Running, session.Projections.Failed, session.Projections.Stale))
			break
		}
	}
	if len(m.timelineState.page.Events) > 0 {
		if m.timelineState.err != nil {
			lines = append(lines, "Timeline continuation failed; loaded cards remain available. Move near the end or press r to retry.")
		}
		if m.timelineState.page.State == app.EvidencePartial {
			lines = append(lines, diagnosticSummary(m.timelineState.page.Diagnostics))
		}
		lines = append(lines, "")
	}
	return lines
}

// timelineLines renders the accumulated card timeline through one viewport.
func (m Model) timelineLines() []string {
	lines := m.timelineHeaderLines()
	switch {
	case m.timelineState.loading && len(m.timelineState.page.Events) == 0:
		return append(lines, "", m.spinner.View()+" Loading timeline events…")
	case m.timelineState.err != nil && len(m.timelineState.page.Events) == 0:
		return append(lines, "", "Could not load timeline: "+m.timelineState.err.Error(), "Press r to retry.")
	case m.timelineState.page.State == app.EvidenceNotFound:
		return append(lines, "", "This session is no longer available.")
	case len(m.timelineState.page.Events) == 0:
		if m.timelineState.page.State == app.EvidenceUnavailable {
			return append(lines, "", "Timeline evidence is unavailable.", diagnosticSummary(m.timelineState.page.Diagnostics))
		}
		return append(lines, "", "This session has no normalized events.")
	}
	lines = append(lines, strings.Split(m.timelineState.viewport.View(), "\n")...)
	return lines
}

// projectionLines renders canonical evidence availability independently from
// derived-data readiness and shows only application-safe diagnostics.
func (m Model) projectionLines() []string {
	lines := []string{"Projection lifecycle · session " + string(m.sessionsState.selected)}
	if m.projectionsState.confirmAll {
		lines = append(lines, "",
			"Rebuild every projection?",
			"Confirm [y] / cancel [n or Esc]",
			"This invalidates current derived output; all registered builders are available.")
		return lines
	}
	switch {
	case m.projectionsState.loading && len(m.projectionsState.status.Projections) == 0:
		return append(lines, "", m.spinner.View()+" Loading projection status…")
	case m.projectionsState.err != nil && len(m.projectionsState.status.Projections) == 0:
		return append(lines, "", "Could not load projection status: "+m.projectionsState.err.Error())
	case m.projectionsState.status.State == app.EvidenceNotFound:
		return append(lines, "", "This session is no longer available.")
	}
	summary := m.projectionsState.status.Summary
	lines = append(lines,
		fmt.Sprintf("Canonical evidence remains available · derived usable %d/%d · pending %d · running %d · failed %d · stale %d · not implemented %d",
			summary.Usable, len(m.projectionsState.status.Projections), summary.Pending, summary.Running, summary.Failed, summary.Stale,
			summary.Unimplemented),
		"",
	)
	if m.projectionsState.err != nil {
		lines = append(lines, "Refresh failed; showing the last observed projection status.", "")
	} else if m.projectionsState.loading {
		lines = append(lines, m.spinner.View()+" Refreshing projection status.", "")
	}
	if m.projectionsState.actionNotice != "" {
		lines = append(lines, m.projectionsState.actionNotice, "")
	}
	if diagnostic := m.projectionsState.status.OperationDiagnostic; diagnostic != nil {
		lines = append(lines, diagnostic.Code+": "+diagnostic.Summary, "")
	}
	for index, state := range m.projectionsState.status.Projections {
		marker := " "
		if index == m.projectionsState.cursor {
			marker = ">"
		}
		flags := string(state.Status)
		if state.Usable {
			flags += " · usable"
		}
		if state.Stale {
			flags += " · stale"
		}
		if !state.BuildAvailable {
			if state.Status == app.ProjectionStatusPending {
				flags = "not implemented in this build · remains pending"
			} else {
				flags += " · rebuild unavailable in this build"
			}
		}
		lines = append(lines, fmt.Sprintf("%s %-16s  %s  · target %s/%d · attempts %d",
			marker, state.Kind, flags, state.TargetVersion, state.TargetRevision, state.AttemptCount))
		if state.Diagnostic != nil {
			lines = append(lines, "    "+state.Diagnostic.Code+": "+state.Diagnostic.Summary)
		}
	}
	if m.projectionsState.status.Active || summary.Running > 0 {
		lines = append(lines, "", "Projection work is active and continues if you leave this panel.")
	}
	return lines
}

// detailContentLines renders normalized evidence as indented JSON and never renders
// the retained raw-record contents.
func (m Model) detailContentLines() []string {
	lines := []string{"Event detail · session " + string(m.sessionsState.selected)}
	switch {
	case m.detailState.loading && m.detailState.detail.Event.ID == "":
		return append(lines, "", m.spinner.View()+" Loading normalized payload…")
	case m.detailState.err != nil && m.detailState.detail.Event.ID == "":
		return append(lines, "", "Could not load event: "+m.detailState.err.Error(), "Press r to retry.")
	case m.detailState.detail.State == app.EvidenceNotFound:
		return append(lines, "", "This event is no longer available.")
	}
	if m.detailState.err != nil {
		lines = append(lines, "Refresh failed; showing the last loaded event. Press r to retry.", "")
	} else if m.detailState.loading {
		lines = append(lines, m.spinner.View()+" Refreshing normalized payload.", "")
	}
	event := m.detailState.detail.Event
	lines = append(lines,
		fmt.Sprintf("#%d · %s · %s", event.Sequence, event.Kind, evidenceLabel(m.detailState.detail.State)),
		event.Summary,
		fmt.Sprintf("Interpretation: %s · %d Unknown events · %d malformed records",
			m.detailState.detail.Interpretation.State,
			m.detailState.detail.Interpretation.UnknownEvents,
			m.detailState.detail.Interpretation.MalformedRecords),
	)
	if m.detailState.detail.Diagnostics.Total > 0 {
		lines = append(lines, diagnosticSummary(m.detailState.detail.Diagnostics))
		for _, diagnostic := range m.detailState.detail.Diagnostics.Diagnostics {
			lines = append(lines, fmt.Sprintf("[%s] %s: %s", diagnostic.Severity, diagnostic.Code, diagnostic.Message))
		}
	}
	lines = append(lines, "", "Normalized payload")
	if m.detailState.detail.State == app.EvidenceUnavailable || m.detailState.detail.Payload == nil {
		lines = append(lines, "Payload evidence is unavailable.")
		return lines
	}
	payload, err := json.MarshalIndent(m.detailState.detail.Payload, "", "  ")
	if err != nil {
		lines = append(lines, "Could not render normalized payload: "+err.Error())
	} else {
		lines = append(lines, strings.Split(string(payload), "\n")...)
	}
	if event.Kind == model.EventKindUnknown {
		lines = append(lines, "", "Retained Unknown evidence")
		switch {
		case m.detailState.inspectionLoading:
			lines = append(lines, m.spinner.View()+" Loading and redacting retained evidence…")
		case m.detailState.inspectionErr != nil:
			lines = append(lines, "Inspection failed: "+m.detailState.inspectionErr.Error())
		case m.detailState.inspection.State == app.EvidenceUnavailable:
			lines = append(lines, "Retained evidence for this event is no longer available.")
		case m.detailState.inspection.State == app.EvidenceNotFound:
			lines = append(lines, "This event's retained evidence could not be located.")
		case m.detailState.inspection.EventID != "":
			inspection := m.detailState.inspection
			lines = append(lines, fmt.Sprintf("%d of %d bytes · %d redactions · truncated: %t",
				inspection.ReturnedSize, inspection.OriginalSize, inspection.RedactionCount, inspection.Truncated))
			lines = append(lines, strings.Split(inspection.Text, "\n")...)
		default:
			lines = append(lines, "Press u to inspect a redacted, bounded copy.")
		}
	}
	return lines
}

// indexingContentLines renders aggregate progress, per-source status, failures, and
// the coordinator's bounded diagnostic synopsis.
func (m Model) indexingContentLines() []string {
	status := m.indexingState.status
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
	return lines
}

// contentHeight reserves rows for the title, persistent index summary, and
// contextual help.
func (m Model) contentHeight() int {
	height := m.height
	if height <= 0 {
		height = 24
	}
	return max(1, height-5)
}

// renderWidth supplies a deterministic fallback before the first resize event.
func (m Model) renderWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}

// helpLine uses compact controls when terminal dimensions are constrained.
func (m Model) helpLine(width int) string {
	if width < 40 || m.height > 0 && m.height < 8 {
		return "↑↓ move · ? help · q quit"
	}
	if width < 96 || m.height > 0 && m.height < 18 {
		switch m.screen {
		case sessionsScreen:
			return "↑↓ move · Enter open · i index · r rescan · ? help · q quit"
		case timelineScreen:
			return "↑↓ cards · Enter expand · x projections · Esc back · ? help"
		case projectionsScreen:
			return "↑↓ select · t retry · b rebuild · Esc back · ? help"
		default:
			return "↑↓ scroll · PgUp/PgDn · Esc back · r refresh · ? help"
		}
	}
	switch m.screen {
	case sessionsScreen:
		return "↑/↓ move · Enter open · / search · i indexing · r rescan · ? help · q quit"
	case searchScreen:
		if m.searchState.editing {
			return "Type query · Enter search · Esc results · Ctrl-C quit"
		}
		return "/ edit · ↑/↓ select · Enter detail · n/p page · Esc sessions · r refresh · ? help"
	case indexingScreen:
		return "↑/↓ or j/k scroll · g/G top/bottom · PgUp/PgDn scroll · Esc sessions · r rescan · ? help · q quit"
	case timelineScreen:
		return "↑/↓ or j/k cards · PgUp/PgDn scroll · Enter expand · u inspect Unknown · x projections · Esc sessions · ? help"
	case projectionsScreen:
		return "↑/↓ select · r refresh · t retry implemented · b rebuild selected · a rebuild all when available · Esc timeline · ? help"
	default:
		if m.detailState.detail.Event.Kind == model.EventKindUnknown {
			return "↑/↓ or j/k scroll · u inspect Unknown · Esc timeline · r reload · ? help · q quit"
		}
		return "↑/↓ or j/k scroll · g/G top/bottom · PgUp/PgDn scroll · Esc timeline · r reload · ? help · q quit"
	}
}

// projectionPolling keeps observation alive for application-owned work and
// for durable running state that may belong to an importer or another caller.
func projectionPolling(status app.ProjectionStatus) bool {
	return status.Active || status.Summary.Running > 0
}

// evidenceLabel maps application evidence states to conservative UI wording.
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

// diagnosticSummary states both observed and omitted diagnostic counts.
func diagnosticSummary(synopsis app.DiagnosticSynopsis) string {
	if synopsis.Total == 0 {
		return ""
	}
	return fmt.Sprintf("Partial evidence: %d diagnostic(s), %d omitted.", synopsis.Total, synopsis.Omitted)
}

// nextLabel reports pagination availability without exposing opaque cursors.
func nextLabel(cursor string) string {
	if cursor != "" {
		return " · more available"
	}
	return ""
}

// windowStart centers the selected row where possible while respecting bounds.
func windowStart(selected, total, visible int) int {
	if total <= visible {
		return 0
	}
	start := selected - visible/2
	return clamp(start, 0, total-visible)
}

// clamp constrains value to the inclusive interval [low, high].
func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// fit truncates already-sanitized, optionally styled lines by display cells
// and preserves contextual help as the last row.
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
		fitted = append(fitted, ansi.Truncate(line, width, "…"))
		if len(fitted) == height {
			break
		}
	}
	return strings.Join(fitted, "\n")
}

// terminalView receives content whose dynamic segments crossed sanitizeLines
// before the application-owned Lip Gloss styles were applied.
func terminalView(content string) tea.View {
	return tea.NewView(content)
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
