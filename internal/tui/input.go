package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

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
