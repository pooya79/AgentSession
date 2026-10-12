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
	if width < 80 || m.height > 0 && m.height < 24 {
		switch m.screen {
		case sessionsScreen:
			return "↑↓ select · Enter open · ? help"
		case timelineScreen:
			return "↑↓ scroll · [/] target · ? help"
		case searchScreen:
			if m.searchState.editing {
				return "Type query · Enter search · Esc results"
			}
			return "Enter timeline · / edit · ? help"
		case detailScreen:
			return "w wrap · ←→ pan · Esc back · ? help"
		default:
			return "↑↓ move · Esc back · ? help"
		}
	}
	if width < 96 || m.height > 0 && m.height < 18 {
		switch m.screen {
		case sessionsScreen:
			return "↑↓ move · Enter open · i index · r rescan · ? help · q quit"
		case timelineScreen:
			return "↑↓ scroll · [/] events · Enter toggle · d inspect · Esc back · ? help"
		case detailScreen:
			return "↑↓ scroll · w wrap · ←→ pan · Esc back · ? help"
		case projectionsScreen:
			return "↑↓ select · t retry · b rebuild · Esc back · ? help"
		case searchScreen:
			if m.searchState.editing {
				return "Type query · Enter search · Esc results · Ctrl-C quit"
			}
			return "/ edit · ↑↓ select · Enter timeline · n/p page · Esc back · ? help"
		default:
			return "↑↓ scroll · PgUp/PgDn · Esc back · r refresh · ? help"
		}
	}
	switch m.screen {
	case findingsScreen:
		return "↑↓ scroll · [/] evidence · Enter timeline context · r refresh · Esc back"
	case sessionsScreen:
		return "↑/↓ move · Enter open · / search · i indexing · r rescan · ? help · q quit"
	case searchScreen:
		if m.searchState.editing {
			return "Type query · Enter search · Esc results · Ctrl-C quit"
		}
		return "/ edit · ↑/↓ select · Enter timeline · n/p page · Esc sessions · r refresh · ? help"
	case indexingScreen:
		return "↑/↓ or j/k scroll · g/G top/bottom · PgUp/PgDn scroll · Esc sessions · r rescan · ? help · q quit"
	case timelineScreen:
		return "↑↓ j/k scroll · PgUp/PgDn · [/] events · Enter toggle · d inspect · u raw · f rules · x projections · Esc back · ? help"
	case projectionsScreen:
		return "↑/↓ select · r refresh · t retry implemented · b rebuild selected · a rebuild all when available · Esc timeline · ? help"
	default:
		if m.detailState.detail.Event.Kind == model.EventKindUnknown {
			return "↑/↓ or j/k scroll · u inspect Unknown · Esc timeline · r reload · ? help · q quit"
		}
		return "↑/↓ or j/k scroll · g/G top/bottom · PgUp/PgDn scroll · Esc timeline · r reload · ? help · q quit"
	}
}

// statusLine keeps the action target visible even when it is outside the
// reading viewport. Scrolling never changes which event Enter acts on.
func (m Model) statusLine() string {
	if m.screen == detailScreen && !m.helpOpen {
		if m.detailState.unwrapped {
			return fmt.Sprintf("Unwrapped · column %d · ←/→ pan · w wrap", m.detailState.viewport.XOffset()+1)
		}
		return "Wrapped · w unwrapped inspection · Esc restores timeline"
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
		target := truncateCell(string(m.timelineState.selected), max(8, m.renderWidth()/3-8))
		if len(m.timelineState.page.Events) > m.timelineState.cursor {
			sequence := fmt.Sprintf("#%d", m.timelineState.page.Events[m.timelineState.cursor].Sequence)
			if m.renderWidth() < 80 {
				target = sequence
			} else {
				target += " " + sequence
			}
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
