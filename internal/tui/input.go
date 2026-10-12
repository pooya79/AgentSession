package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// handleKey maps the documented controls to screen-aware navigation.
func (m Model) handleKey(key string) (tea.Model, tea.Cmd) {
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
			m.projectionsState.viewport.GotoTop()
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

	if m.screen == findingsScreen {
		switch key {
		case "[", "]":
			delta := 1
			if key == "[" {
				delta = -1
			}
			m.findingsState.evidenceCursor = clamp(m.findingsState.evidenceCursor+delta, 0, max(0, len(m.findingReferences())-1))
			m.syncViewports()
			m.revealFindingReference()
			return m, nil
		case "enter":
			return m.openFindingEvidence()
		}
	}
	switch key {
	case "d":
		if m.screen == timelineScreen && len(m.timelineState.page.Events) > 0 {
			event := m.timelineState.page.Events[m.timelineState.cursor]
			m.pushNavigation()
			m.screen = detailScreen
			vp := m.detailState.viewport
			vp.GotoTop()
			vp.SetXOffset(0)
			ctx := m.replaceRequest()
			m.detailState = detailState{viewport: vp, loading: true}
			return m, m.startSpinner(loadDetail(ctx, m.services, m.requestGeneration, m.sessionsState.selected, event.ID))
		}
	case "w":
		if m.screen == detailScreen {
			m.detailState.unwrapped = !m.detailState.unwrapped
			m.syncViewports()
			if !m.detailState.unwrapped {
				m.detailState.viewport.SetXOffset(0)
			}
			return m, nil
		}
	case "left", "right":
		if m.screen == detailScreen && m.detailState.unwrapped {
			m.syncViewports()
			if key == "left" {
				m.detailState.viewport.ScrollLeft(8)
			} else {
				m.detailState.viewport.ScrollRight(8)
			}
			return m, nil
		}
	case "f":
		if m.screen == timelineScreen {
			m.pushNavigation()
			m.screen = findingsScreen
			m.findingsState.evidenceCursor = 0
			vp := m.detailState.viewport
			vp.GotoTop()
			vp.SetXOffset(0)
			m.detailState = detailState{viewport: vp}
			return m, m.loadFindings()
		}
	case "esc":
		return m.back()
	case "/":
		if m.screen != searchScreen {
			m.pushNavigation()
		}
		m.screen = searchScreen
		if m.searchState.loading {
			m.cancelRequest()
		}
		m.searchState.editing = true
		m.searchState.queryCursor = len([]rune(m.searchState.query))
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
			m.pushNavigation()
			m.screen = projectionsScreen
			if m.projectionsState.status.SessionID != m.sessionsState.selected {
				m.projectionsState.status = app.ProjectionStatus{}
				m.projectionsState.actionNotice = ""
			}
			m.projectionsState.cursor = 0
			m.projectionsState.confirmAll = false
			m.projectionsState.viewport.GotoTop()
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
				m.timelineState.expanded[event.ID] = true
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
			m.projectionsState.viewport.GotoTop()
		}
	case "n":
		if m.screen == timelineScreen {
			return m, nil
		}
		return m.nextPage()
	case "up", "k":
		if m.screen == timelineScreen {
			m.moveScroll(-1)
		} else {
			m.move(-1)
		}
		return m, m.requestTimelineHistory()
	case "down", "j":
		if m.screen == timelineScreen {
			m.moveScroll(1)
		} else {
			m.move(1)
		}
		return m, m.requestTimelineHistory()
	case "[", "]":
		if m.screen == timelineScreen {
			delta := 1
			if key == "[" {
				delta = -1
			}
			m.move(delta)
			return m, m.requestTimelineHistory()
		}
	case "home", "g":
		m.moveToBoundary(false)
		return m, m.requestTimelineHistory()
	case "end", "G":
		m.moveToBoundary(true)
		return m, m.requestTimelineHistory()
	case "pgup":
		if m.screen == timelineScreen {
			m.syncViewports()
			m.timelineState.edgeStep = 0
			m.timelineState.selectionRequested = false
			m.timelineState.scrollDirection = -1
			m.timelineState.viewport.PageUp()
			return m, m.requestTimelineHistory()
		} else if m.screen == detailScreen || m.screen == findingsScreen || m.screen == indexingScreen || m.screen == projectionsScreen {
			m.moveScroll(-m.pageStep())
		} else {
			return m.previousPage()
		}
	case "pgdown":
		if m.screen == timelineScreen {
			m.syncViewports()
			m.timelineState.edgeStep = 0
			m.timelineState.selectionRequested = false
			m.timelineState.scrollDirection = 1
			m.timelineState.viewport.PageDown()
			return m, m.requestTimelineHistory()
		} else if m.screen == detailScreen || m.screen == findingsScreen || m.screen == indexingScreen || m.screen == projectionsScreen {
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
