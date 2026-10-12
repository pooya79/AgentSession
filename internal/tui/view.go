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

	if width < 40 || height < 12 {
		quit := "q / Ctrl-C quit"
		if m.screen == searchScreen && m.searchState.editing {
			quit = "Ctrl-C quit"
		}
		view := terminalView(fit([]string{"AgentSession", "Resize to at least 40×12", fmt.Sprintf("Current: %d×%d", width, height), quit}, width, height))
		view.AltScreen = true
		return view
	}

	snapshot := m
	snapshot.syncViewports()
	header := "AgentSession  /  " + snapshot.screenLabel()
	if width < 40 {
		header = "AgentSession / " + snapshot.screenLabel()
	}
	lines := []string{header}
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
			body = append(snapshot.detailHeaderLines(), strings.Split(snapshot.detailState.viewport.View(), "\n")...)
		case findingsScreen:
			body = strings.Split(snapshot.detailState.viewport.View(), "\n")
		case projectionsScreen:
			body = strings.Split(snapshot.projectionsState.viewport.View(), "\n")
		case searchScreen:
			body = snapshot.searchLines()
		}
	}
	lines = append(lines, body...)
	for len(lines) < height-2 {
		lines = append(lines, "")
	}
	lines = append(lines, snapshot.statusLine(), snapshot.helpLine(width))
	lines = snapshot.styleLines(lines)

	view := terminalView(fit(lines, width, height))
	view.AltScreen = true
	view.WindowTitle = "AgentSession"
	return view
}

func (m Model) screenLabel() string {
	switch m.screen {
	case findingsScreen:
		return "Assessments"
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
		return "Index unavailable · " + m.indexingState.err.Error()
	case status.Failure != "":
		return "Index unavailable · " + status.Failure
	case status.Active:
		return fmt.Sprintf("%s Indexing · %d/%d sources · %d diagnostics",
			m.spinner.View(), status.SourcesCompleted, status.SourcesDiscovered, status.DiagnosticsTotal)
	case status.Phase == app.ImportAllUpToDate:
		if status.SourcesDiscovered == 0 {
			return "No supported sources found · r rescan"
		}
		return ""
	case status.Phase == app.ImportAllIssues:
		return fmt.Sprintf("Indexing issues · %d failed · %d diagnostics · i details",
			status.SourcesFailed, status.DiagnosticsTotal)
	default:
		return "Index status unavailable · i details"
	}
}
