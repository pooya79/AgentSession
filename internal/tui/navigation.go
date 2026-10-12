package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// navigationContext retains a reading context while a temporary screen or a
// supporting-evidence timeline is open. Requests are canceled before saving;
// their late responses cannot modify the saved context.
type navigationContext struct {
	screen      screen
	session     model.SessionID
	timeline    timelineState
	detail      detailState
	findings    findingsState
	search      searchState
	projections projectionsState
}

func (m *Model) pushNavigation() {
	if m.screen == detailScreen && m.detailState.inspection.State != "" {
		m.syncViewports()
	}
	m.cancelRequest()
	m.captureTimelineAnchor()
	// Raw inspection is explicitly requested, short-lived presentation data.
	// Do not retain it in a saved reader context or a rendered-card cache.
	m.timelineState.inspections = make(map[model.EventID]app.UnknownEvidenceInspection)
	m.timelineState.inspectionErrors = make(map[model.EventID]error)
	m.timelineState.cards = nil
	m.invalidateTimelineRender()
	m.timelineState.viewport.SetContentLines(nil)
	if m.detailState.inspection.State != "" {
		// The logical inspection anchor survives clearing the viewport buffer;
		// the next synchronization rebuilds only normalized evidence.
		m.detailState.viewport.SetContentLines(nil)
		m.detailState.viewport.SetXOffset(0)
		m.detailState.renderedPositions = nil
		m.detailState.lastTop, m.detailState.lastX = 0, 0
	}
	m.detailState.inspection = app.UnknownEvidenceInspection{}
	m.detailState.inspectionErr = nil
	if m.screen == projectionsScreen {
		m.stopProjectionObservation()
	}
	m.navigation = append(m.navigation, navigationContext{
		screen: m.screen, session: m.sessionsState.selected,
		timeline: m.timelineState, detail: m.detailState,
		findings: m.findingsState, search: m.searchState,
		projections: m.projectionsState,
	})
}

func (m *Model) runSearch(cursor string) tea.Cmd {
	m.searchState.refreshing = cursor == "" && !m.searchState.dirty && len(m.searchState.page.Results) > 0
	ctx := m.replaceRequest()
	m.searchState.loading = true
	m.searchState.err = nil
	m.searchState.notice = ""
	if m.searchState.dirty {
		m.searchState.page = app.SearchPage{}
		m.searchState.cursor = 0
	}
	m.searchState.dirty = false
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
	if len(m.navigation) > 0 {
		m.cancelRequest()
		m.stopProjectionObservation()
		last := len(m.navigation) - 1
		previous := m.navigation[last]
		m.navigation[last] = navigationContext{} // release payload references
		m.navigation = m.navigation[:last]
		m.screen, m.sessionsState.selected = previous.screen, previous.session
		m.timelineState, m.detailState = previous.timeline, previous.detail
		m.findingsState, m.searchState = previous.findings, previous.search
		if m.screen == projectionsScreen {
			generation := m.projectionsState.generation
			m.projectionsState = previous.projections
			m.projectionsState.generation = generation
			m.projectionsState.cancel = nil
		}
		if m.screen == timelineScreen && len(m.timelineState.page.Events) > 0 && m.timelineState.page.Payloads == nil {
			notice := m.timelineState.notice
			cmd := m.reloadTimeline()
			m.timelineState.notice = notice
			return m, cmd
		}
		m.syncViewports()
		switch m.screen {
		case sessionsScreen:
			return m, tea.Batch(m.reloadSessions(), m.observeNow())
		case projectionsScreen:
			return m, m.observeProjectionsNow()
		case indexingScreen:
			return m, m.observeNow()
		case timelineScreen:
			if len(m.timelineState.page.Events) == 0 {
				return m, m.reloadTimeline()
			}
		case findingsScreen:
			if m.findingsState.page.State != app.EvidenceComplete {
				return m, m.loadFindings()
			}
		}
		return m, nil
	}
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
	case findingsScreen:
		m.cancelRequest()
		m.findingsState = findingsState{}
		m.screen = timelineScreen
		if len(m.timelineState.page.Events) == 0 {
			return m, m.reloadTimeline()
		}
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
	case findingsScreen:
		return m, m.loadFindings()
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
			m.timelineState.selectionRequested = true
			m.timelineState.scrollDirection = delta
			next := m.timelineState.cursor + delta
			m.timelineState.cursor = clamp(next, 0, len(m.timelineState.page.Events)-1)
			m.timelineState.edgeStep = next - m.timelineState.cursor
			m.timelineState.selected = m.timelineState.page.Events[m.timelineState.cursor].ID
			m.invalidateTimelineRender()
			m.syncViewports()
			m.revealTimelineSelection(m.timelineState.cachedRanges)
			m.captureTimelineAnchor()
		}
	case projectionsScreen:
		if len(m.projectionsState.status.Projections) > 0 {
			m.projectionsState.cursor = clamp(m.projectionsState.cursor+delta, 0, len(m.projectionsState.status.Projections)-1)
			m.projectionsState.revealTarget = true
			m.syncViewports()
		}
	case detailScreen, findingsScreen, indexingScreen:
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
		m.timelineState.selectionRequested = true
		m.timelineState.scrollDirection = 1
		m.timelineState.edgeStep = 0
		m.timelineState.cursor = 0
		if last {
			m.timelineState.cursor = len(m.timelineState.page.Events) - 1
		}
		m.timelineState.selected = m.timelineState.page.Events[m.timelineState.cursor].ID
		m.invalidateTimelineRender()
		m.syncViewports()
		m.revealTimelineSelection(m.timelineState.cachedRanges)
	case projectionsScreen:
		if len(m.projectionsState.status.Projections) == 0 {
			return
		}
		m.projectionsState.cursor = 0
		if last {
			m.projectionsState.cursor = len(m.projectionsState.status.Projections) - 1
		}
		m.projectionsState.revealTarget = true
		m.syncViewports()
	case detailScreen, findingsScreen:
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
	m.timelineState.edgeStep = 0
	m.timelineState.selectionRequested = false
	m.timelineState.scrollDirection = delta
	m.syncViewports()
	target := &m.detailState.viewport
	if m.screen == timelineScreen {
		target = &m.timelineState.viewport
	}
	if m.screen == indexingScreen {
		target = &m.indexingState.viewport
	}
	if m.screen == projectionsScreen {
		target = &m.projectionsState.viewport
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
		if m.searchState.page.NextCursor == "" || m.searchState.loading || m.searchState.dirty {
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
		if m.searchState.page.PreviousCursor == "" || m.searchState.loading || m.searchState.dirty {
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
		if m.searchState.dirty || !m.searchState.loading && m.searchState.page.State == "" && len(m.searchState.page.Results) == 0 {
			return m, m.runSearch("")
		}
		if m.searchState.loading || len(m.searchState.page.Results) == 0 {
			return m, nil
		}
		session := m.searchState.page.Results[m.searchState.cursor].SessionID
		m.pushNavigation()
		return m.openSessionTimeline(session)
	case sessionsScreen:
		if m.sessionsState.loading || len(m.sessionsState.page.Sessions) == 0 {
			return m, nil
		}
		return m.openSessionTimeline(m.sessionsState.page.Sessions[m.sessionsState.cursor].ID)
	case timelineScreen:
		if len(m.timelineState.page.Events) == 0 {
			return m, nil
		}
		event := m.timelineState.page.Events[m.timelineState.cursor]
		if _, resident := m.timelineState.page.Payloads[event.ID]; !resident {
			m.timelineState.selectionRequested = true
			return m, m.requestTimelineHistory()
		}
		if timelinePayloadTruncatable(m.timelineState.page.Payloads[event.ID]) ||
			m.timelineState.inspections[event.ID].EventID != "" {
			m.captureTimelineAnchor()
			wasExpanded := m.timelineState.expanded[event.ID]
			if wasExpanded && m.timelineState.anchor.eventID == event.ID {
				m.timelineState.disclosureAnchors[event.ID] = m.timelineState.anchor
			}
			m.timelineState.expanded[event.ID] = !m.timelineState.expanded[event.ID]
			m.invalidateTimelineRender()
			if wasExpanded && m.timelineState.anchor.eventID == event.ID {
				m.timelineState.anchor = timelineReadingAnchor{eventID: event.ID, header: true}
			} else if saved, ok := m.timelineState.disclosureAnchors[event.ID]; !wasExpanded && ok {
				m.timelineState.anchor = saved
			}
			m.syncViewports()
		}
		return m, m.requestTimelineHistory()
	}
	return m, nil
}

func (m *Model) openSessionTimeline(sessionID model.SessionID) (tea.Model, tea.Cmd) {
	m.sessionsState.selected = sessionID
	m.screen = timelineScreen
	m.resetTimelineState()
	m.stopObservation()
	ctx := m.replaceRequest()
	m.timelineState.loading = true
	return m, m.startSpinner(loadTimeline(ctx, m.services, m.requestGeneration, sessionID, ""))
}
