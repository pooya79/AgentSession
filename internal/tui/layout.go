package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// contentHeight reserves rows for the title, persistent index summary, and
// contextual help.
func (m Model) contentHeight() int {
	height := m.height
	if height <= 0 {
		height = 24
	}
	return max(1, height-5)
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
	if width < 40 || m.height > 0 && m.height < 8 {
		return "↑↓ move · ? help · q quit"
	}
	if width < 96 || m.height > 0 && m.height < 18 {
		switch m.screen {
		case sessionsScreen:
			return "↑↓ move · Enter open · i index · r rescan · ? help · q quit"
		case timelineScreen:
			return "↑↓ cards · Enter expand · x projections · Esc back · ? help"
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
		return "↑/↓ or j/k cards · PgUp/PgDn scroll · Enter expand · u inspect Unknown · x projections · Esc sessions · ? help"
	case projectionsScreen:
		return "↑/↓ select · r refresh · t retry implemented · b rebuild selected · a rebuild all when available · Esc timeline · ? help"
	default:
		if m.detailState.detail.Event.Kind == model.EventKindUnknown {
			return "↑/↓ or j/k scroll · u inspect Unknown · Esc timeline · r reload · ? help · q quit"
		}
		return "↑/↓ or j/k scroll · g/G top/bottom · PgUp/PgDn scroll · Esc timeline · r reload · ? help · q quit"
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
