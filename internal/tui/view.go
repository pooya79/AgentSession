package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/pooya79/AgentSession/internal/app"
)

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
