package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/pooya79/AgentSession/internal/sanitization"
)

// syncViewports refreshes dimensions and content after state or terminal-size
// changes. Viewport models own offset clamping across both operations.
func (m *Model) syncViewports() {
	width := max(1, m.renderWidth())
	height := max(1, m.contentHeight())

	if m.helpOpen {
		m.helpViewport.SetWidth(width)
		m.helpViewport.SetHeight(height)
		m.helpViewport.SetContentLines(sanitizeLines(m.helpLines()))
		return
	}
	switch m.screen {
	case timelineScreen:
		m.captureTimelineAnchor()
		changed := m.timelineState.cachedLines == nil || m.timelineState.cachedRevision != m.timelineState.renderRevision || m.timelineState.cachedWidth != max(20, m.renderWidth()-2)
		timelineLines, timelineRanges := m.timelineContent()
		m.timelineState.viewport.SetWidth(width)
		m.timelineState.viewport.SetHeight(max(1, height-len(m.timelineHeaderLines())))
		if changed {
			m.timelineState.viewport.SetContentLines(timelineLines)
		}
		m.restoreTimelineAnchor(timelineRanges)
	case detailScreen, findingsScreen:
		detailHeight := height
		if m.screen == detailScreen {
			detailHeight -= len(m.detailHeaderLines())
		}
		m.syncDetailViewport(width, max(1, detailHeight))
	case indexingScreen:
		m.indexingState.viewport.SetWidth(width)
		m.indexingState.viewport.SetHeight(height)
		m.indexingState.viewport.SetContentLines(sanitizeLines(m.indexingContentLines()))
	case projectionsScreen:
		m.syncProjectionViewport(width, height)
	}

}

// syncProjectionViewport wraps diagnostics without clipping them and reveals
// the action target only on selection changes, so page scrolling stays put.
func (m *Model) syncProjectionViewport(width, height int) {
	state := &m.projectionsState
	var lines []string
	target := -1
	for _, line := range m.projectionLines() {
		line = terminalText(line)
		if strings.HasPrefix(line, "> ") {
			target = len(lines)
		}
		lines = append(lines, wrapIndented(line, width, true)...)
	}
	state.viewport.SetWidth(width)
	state.viewport.SetHeight(height)
	state.viewport.SetContentLines(lines)
	if state.revealTarget && target >= 0 {
		state.viewport.SetYOffset(target)
		state.revealTarget = false
	}
}

// syncDetailViewport retains a logical line and display-cell position through
// wrapping, panning and viewport clamping. The independent anchor survives when
// a short unwrapped document cannot put its target line at the top of the view.
func (m *Model) syncDetailViewport(width, height int) {
	state := &m.detailState
	top, x := state.viewport.YOffset(), state.viewport.XOffset()
	if !state.anchorSet || top != state.lastTop {
		if len(state.renderedPositions) > 0 {
			state.anchor = state.renderedPositions[min(top, len(state.renderedPositions)-1)]
			if state.renderedUnwrapped {
				state.anchor.column += x
			}
		}
	} else if state.renderedUnwrapped && x != state.lastX {
		state.anchor.column = max(0, state.anchor.column+x-state.lastX)
	}
	// When several physical lines are visible, retain their beginnings while
	// switching to unwrapped text. A long single-line viewport keeps its column.
	if state.unwrapped && !state.renderedUnwrapped && len(state.renderedPositions) > 0 {
		last := min(top+state.viewport.Height()-1, len(state.renderedPositions)-1)
		if state.renderedPositions[last].line > state.anchor.line {
			state.anchor.column = 0
		}
	}
	var lines []string
	var positions []inspectionPosition
	physical := m.detailContentLines()
	if m.screen == findingsScreen {
		physical = m.findingsContentLines()
	}
	for index, line := range physical {
		line = terminalText(line)
		parts := []string{line}
		indent := 0
		if !state.unwrapped {
			parts = wrapIndented(line, width, true)
			indent = len(line) - len(strings.TrimLeft(line, " "))
			if indent >= width {
				indent = 0
			}
		}
		column := 0
		for i, part := range parts {
			positions = append(positions, inspectionPosition{line: index, column: column})
			lines = append(lines, part)
			column += ansi.StringWidth(part)
			if i > 0 {
				column -= indent
			}
		}
	}
	state.anchor.line = clamp(state.anchor.line, 0, max(0, len(physical)-1))
	row := 0
	for i, position := range positions {
		if position.line == state.anchor.line && position.column <= state.anchor.column {
			row = i
		}
	}
	state.viewport.SoftWrap = false
	state.viewport.SetWidth(width)
	state.viewport.SetHeight(height)
	state.viewport.SetContentLines(lines)
	state.viewport.SetYOffset(row)
	if state.unwrapped {
		state.viewport.SetXOffset(state.anchor.column)
	} else {
		state.viewport.SetXOffset(0)
	}
	state.renderedPositions, state.renderedUnwrapped = positions, state.unwrapped
	state.anchorSet = true
	state.lastTop, state.lastX = state.viewport.YOffset(), state.viewport.XOffset()
}

// sanitizeLines establishes the terminal-safety boundary before any
// application-owned ANSI styling is added.
func sanitizeLines(lines []string) []string {
	safe := make([]string, len(lines))
	for index, line := range lines {
		safe[index] = sanitization.Terminal(line)
	}
	return safe
}

// styleLines applies semantic color while preserving text labels for terminals
// that omit or down-sample color.
func (m Model) styleLines(lines []string) []string {
	safe := sanitizeLines(lines)
	for index, line := range safe {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		switch {
		case index == 0:
			safe[index] = m.theme.accent.Render(line)
		case strings.Contains(line, "CANONICAL INDEX"):
			safe[index] = styleStatusLine(m.theme, line)
		case strings.HasPrefix(line, "[FOCUS]"):
			safe[index] = m.theme.title.Render(line)
		case strings.HasPrefix(line, ">"):
			safe[index] = m.theme.focused.Render(line)
		case strings.Contains(line, "╭─ You"):
			safe[index] = m.theme.accent.Render(line)
		case strings.Contains(line, "╭─ Assistant"):
			safe[index] = m.theme.info.Render(line)
		case strings.Contains(line, "╭─ System"):
			safe[index] = m.theme.muted.Render(line)
		case strings.Contains(lower, "could not ") || strings.Contains(lower, "unavailable") || strings.Contains(lower, "failed"):
			safe[index] = m.theme.danger.Render(line)
		case strings.Contains(lower, "outcome: unknown") || strings.Contains(lower, "partial") || strings.Contains(lower, "issues") || strings.Contains(lower, "diagnostic"):
			safe[index] = m.theme.warning.Render(line)
		case strings.Contains(line, "RECORDED FAILURE") || strings.Contains(line, "Index: ERROR"):
			safe[index] = m.theme.danger.Render(line)
		case strings.HasPrefix(trimmed, "§ ") || strings.HasPrefix(trimmed, "Code ·"):
			safe[index] = m.theme.title.Render(line)
		case strings.HasPrefix(lower, "loading") || strings.Contains(lower, "active"):
			safe[index] = m.theme.info.Render(line)
		case index == len(safe)-1:
			safe[index] = m.theme.footer.Render(line)
		case isScreenTitle(trimmed):
			safe[index] = m.theme.title.Render(line)
		}
	}
	return safe
}

// styleStatusLine maps the persistent status row to its semantic theme.
func styleStatusLine(styles theme, line string) string {
	lineState := strings.ToLower(line)
	switch {
	case strings.Contains(lineState, "with issues"), strings.Contains(lineState, "diagnostic"):
		return styles.warning.Render(line)
	case strings.Contains(lineState, "unavailable"):
		return styles.danger.Render(line)
	case strings.Contains(lineState, "indexing"):
		return styles.info.Render(line)
	case strings.Contains(lineState, "complete"):
		return styles.success.Render(line)
	default:
		return styles.muted.Render(line)
	}
}

// isScreenTitle recognizes section headings that are not the first rendered
// line and therefore need explicit title styling.
func isScreenTitle(line string) bool {
	for _, prefix := range []string{
		"Imported sessions", "Indexing details", "Timeline", "Event detail",
		"Projection lifecycle", "Search canonical evidence", "Keyboard help",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// helpLines returns the complete control reference; the viewport handles
// wrapping and scrolling in constrained terminals.
func (m Model) helpLines() []string {
	lines := []string{"Keyboard help · " + m.screenLabel(), ""}
	switch m.screen {
	case sessionsScreen:
		lines = append(lines,
			"  ↑/↓ or j/k   choose a session",
			"  Enter        open its timeline",
			"  /            search recorded evidence",
			"  v            show/hide coverage details",
			"  i            indexing status",
			"  r            rescan sources",
			"  n/p          next/previous page")
	case timelineScreen:
		lines = append(lines,
			"  ↑/↓ or j/k   read text",
			"  [ / ]        previous/next event",
			"  Enter        expand/collapse technical evidence",
			"  d            inspect selected event",
			"  u            inspect redacted Unknown evidence",
			"  f            rule assessments",
			"  x            derived views and rebuilds",
			"  /            search",
			"  r            refresh; retry failed reads",
			"  Target       event acted on by Enter, d and u")
	case detailScreen:
		lines = append(lines,
			"  ↑/↓ or j/k   read evidence",
			"  w            toggle wrapping",
			"  ←/→          pan when unwrapped",
			"  v            show/hide metadata and diagnostics",
			"  u            inspect redacted Unknown evidence",
			"  r            refresh; retry failed reads")
	case findingsScreen:
		lines = append(lines,
			"  ↑/↓ or j/k   read assessments",
			"  [ / ]        choose supporting evidence",
			"  Enter        open its timeline context",
			"  v            full explanations, counts and IDs",
			"  r            refresh; retry failed reads")
	case projectionsScreen:
		lines = append(lines,
			"  ↑/↓ or j/k   select a derived view",
			"  v            show/hide versions and counters",
			"  t            retry pending or failed work",
			"  b            rebuild selected view",
			"  a            rebuild all; confirmation required",
			"  r            refresh status",
			"  Unimplemented builds cannot be requested.")
	case indexingScreen:
		lines = append(lines,
			"  ↑/↓ or j/k   read source status",
			"  v            source paths, counters and diagnostics",
			"  r            rescan sources",
			"  Indexing continues while you browse.")
	case searchScreen:
		lines = append(lines,
			"  ↑/↓ or j/k   choose a matching session",
			"  Enter        open matching timeline context",
			"  /            edit query",
			"  v            show/hide filters and result details",
			"  n/p          next/previous page",
			"  r            retry the current search",
			"",
			"  While editing: ←/→ move; Home/End jump.",
			"  Backspace/Del delete; Enter searches.",
			"  Esc stops editing, then returns.",
			"  Paste and shortcut keys remain query text.",
			"  Filters: session: kind: after: before:",
			"           file: tool: command:")
	}
	return append(lines, "",
		"  PgUp/PgDn    scroll a page",
		"  Home/End     first/last row (also g/G)",
		"  Esc          return to previous screen",
		"  ?            close this help",
		"  q / Ctrl-C   quit (Ctrl-C while editing)")
}
