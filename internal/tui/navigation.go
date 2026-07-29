package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/pooya79/AgentSession/internal/app"
)

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
		m.screen = timelineScreen
		m.resetTimelineState()
		m.timelineState.loading = true
		m.stopObservation()
		ctx := m.replaceRequest()
		return m, m.startSpinner(loadTimeline(ctx, m.services, m.requestGeneration, result.SessionID, ""))
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
