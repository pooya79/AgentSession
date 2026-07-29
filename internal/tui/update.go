package tui

import (
	"context"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/pooya79/AgentSession/internal/app"
)

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
