package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

const (
	// pageSize deliberately matches the application default so both
	// presentation layers traverse evidence with the same bounded requests.
	pageSize = app.DefaultPageSize
	// pollInterval keeps progress responsive without continuously reading the
	// coordinator's shared status.
	pollInterval = 500 * time.Millisecond
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

// unknownEvidenceLoadedMsg routes a bounded, redacted inspection back to the
// timeline card or detail view that requested it.
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

// searchLoadedMsg carries one bounded search page and its request generation.
type searchLoadedMsg struct {
	generation uint64
	page       app.SearchPage
	err        error
}

// pollProjectionsMsg schedules one observation; it is not a repeating timer.
type pollProjectionsMsg struct{ generation uint64 }

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

// loadSearch requests one opaque-cursor page from the shared search service.
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
