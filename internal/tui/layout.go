package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// contentHeight reserves one title row and two persistent footer rows.
func (m Model) contentHeight() int {
	height := m.height
	if height <= 0 {
		height = 24
	}
	return max(1, height-3)
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
	compact := width < 80 || m.height > 0 && m.height < 24
	if m.helpOpen {
		if compact {
			return "↑↓ scroll · PgUp/Dn · Esc close"
		}
		return "↑↓ scroll · PgUp/PgDn · g/G ends · Esc close · ? close"
	}
	switch m.screen {
	case findingsScreen:
		if compact {
			return "[] refs · Enter open · Esc · ? help"
		}
		return "[] evidence · Enter open · v explain · Esc back · ? help"
	case projectionsScreen:
		if m.projectionsState.confirmAll {
			return "y confirm · n/Esc cancel · ? help"
		}
		if compact {
			return "↑↓ target · t/b act · Esc · ? help"
		}
		return "↑↓ select · b rebuild · v details · Esc back · ? help"
	case indexingScreen:
		if compact {
			return "↑↓ scroll · r rescan · Esc · ? help"
		}
		return "↑↓ read · v details · r rescan · Esc back · ? help"
	case detailScreen:
		if m.detailState.detail.Event.Kind == model.EventKindUnknown {
			if compact {
				return "↑↓ scroll · u raw · Esc back · ? help"
			}
			return "↑↓ read · u raw · v metadata · Esc back · ? help"
		}
		if compact {
			return "↑↓ scroll · w wrap · Esc back · ? help"
		}
		return "↑↓ read · w wrap · v metadata · Esc back · ? help"
	case sessionsScreen:
		if compact {
			return "↑↓ select · Enter open · ? help"
		}
		return "↑↓ choose · Enter open · / search · v details · ? help"
	case searchScreen:
		if m.searchState.editing {
			if compact {
				return "Type query · Enter search · Esc results"
			}
			return "Type query · Enter search · Esc results · Ctrl-C quit"
		}
		if compact {
			return "Enter open · / edit · ? help"
		}
		return "↑↓ choose · Enter open · / edit · v details · Esc back · ? help"
	case timelineScreen:
		if compact {
			return "↑↓ read · [] events · Esc · ? help"
		}
		if timelinePayloadTruncatable(m.timelineState.page.Payloads[m.timelineState.selected]) {
			action := "expand"
			if m.timelineState.expanded[m.timelineState.selected] {
				action = "collapse"
			}
			return "↑↓ read · [] events · Enter " + action + " · d inspect · Esc · ? help"
		}
		return "↑↓ read · [] events · d inspect · / search · Esc back · ? help"
	default:
		return "Esc back · ? help"
	}
}

// statusLine keeps the action target visible even when it is outside the
// reading viewport. Scrolling never changes which event Enter acts on.
func (m Model) statusLine() string {
	if m.helpOpen {
		return "[FOCUS] Help · returns to " + m.screenLabel()
	}
	if m.screen == projectionsScreen && !m.helpOpen {
		if m.projectionsState.confirmAll {
			return "[FOCUS] Confirm rebuild all · y / n"
		}
		if m.projectionsState.err != nil {
			return "[FOCUS] Projection ERROR · r retry"
		}
		if len(m.projectionsState.status.Projections) > 0 {
			state := m.projectionsState.status.Projections[m.projectionsState.cursor]
			status := string(state.Status)
			if state.Stale {
				status = "stale · " + status
			}
			if m.projectionsState.loading {
				status = "refreshing / admitting action"
			}
			return "[FOCUS] " + state.Kind + " · " + status
		}
		if m.projectionsState.loading {
			return "[FOCUS] Projections · loading status/action"
		}
		return "[FOCUS] Projections · r retry status"
	}
	if m.screen == searchScreen && !m.helpOpen {
		if m.searchState.editing {
			return "[FOCUS] Query · ←/→ Home/End · Del/Backspace"
		}
		return "[FOCUS] Results · r retry · Esc back"
	}
	if m.screen == detailScreen && !m.helpOpen {
		if m.detailState.unwrapped {
			return fmt.Sprintf("[FOCUS] Unwrapped · column %d · ←/→ pan", m.detailState.viewport.XOffset()+1)
		}
		return "[FOCUS] Wrapped · " + evidenceLabel(m.detailState.detail.State)
	}
	if m.screen == findingsScreen {
		refs := m.findingReferences()
		if len(refs) > 0 && m.findingsState.page.State == app.EvidenceComplete {
			return fmt.Sprintf("[FOCUS] Evidence %d/%d · Enter opens context", m.findingsState.evidenceCursor+1, len(refs))
		}
		return "[FOCUS] Assessments · " + evidenceLabel(m.findingsState.page.State)
	}
	if m.screen == indexingScreen {
		return "[FOCUS] Indexing · " + m.compactIndexStatus()
	}
	if m.screen == timelineScreen && !m.helpOpen && m.timelineState.selected != "" {
		state := "open"
		if m.timelineState.page.Payloads[m.timelineState.selected] == nil {
			state = "unavailable"
			if _, resident := m.timelineState.page.Payloads[m.timelineState.selected]; !resident {
				state = "outside working set · Enter reload"
			}
		}
		if timelinePayloadTruncatable(m.timelineState.page.Payloads[m.timelineState.selected]) {
			state = "+ closed"
			if m.timelineState.expanded[m.timelineState.selected] {
				state = "- expanded"
			}
		}
		if m.renderWidth() < 80 {
			state = strings.TrimSuffix(strings.TrimSuffix(state, " closed"), " expanded")
		}
		if r, ok := m.timelineState.cachedRanges[m.timelineState.selected]; ok {
			top := m.timelineState.viewport.YOffset()
			if r.end <= top || r.start >= top+m.timelineState.viewport.Height() {
				state += " off-screen"
			}
		}
		target := "unavailable"
		if len(m.timelineState.page.Events) > m.timelineState.cursor {
			target = fmt.Sprintf("#%d", m.timelineState.page.Events[m.timelineState.cursor].Sequence)
		}
		return "Target: " + target + " · " + state + " · " + m.compactIndexStatus()
	}
	return m.indexSummary()
}

func (m Model) compactIndexStatus() string {
	status := m.indexingState.status
	switch {
	case m.indexingState.err != nil || status.Failure != "":
		return "Index: ERROR · i in sessions"
	case status.Active:
		return "Index: indexing"
	case status.Phase == app.ImportAllIssues:
		return "Index: issues"
	case status.Phase == app.ImportAllUpToDate:
		return "Index: complete"
	default:
		return "Index: unavailable"
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
