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
	}

}

// syncDetailViewport retains a logical line and display-cell position through
// wrapping, panning and viewport clamping. The independent anchor survives when
// a short unwrapped document cannot put its target line at the top of the view.
func (m *Model) syncDetailViewport(width, height int) {
	state := &m.detailState
	if m.screen == findingsScreen {
		state.viewport.SoftWrap = true
		state.viewport.SetWidth(width)
		state.viewport.SetHeight(height)
		state.viewport.SetContentLines(sanitizeLines(m.findingsContentLines()))
		return
	}
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
	var lines []string
	var positions []inspectionPosition
	physical := m.detailContentLines()
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
	lines := []string{
		"Keyboard help",
		"",
		"Global",
		"  ?            open or close this help",
		"  Esc          close an overlay or return to the parent screen",
		"  q / Ctrl-C   quit",
		"",
		"Navigation",
		"  ↑/↓ or j/k   scroll timeline text; move rows in lists",
		"  [ / ]        jump to previous/next timeline event",
		"  Home/End     jump to the first or last row",
		"  g/G          jump to the first or last row",
		"  PgUp/PgDn    scroll long evidence",
		"  n/p          next/previous session or search page",
		"  Enter        open a session or expand/collapse a timeline card",
		"  Target       footer names the event Enter, d and u act on",
		"  Esc          return to the previous reading context, including search",
		"",
		"Actions",
		"  d            inspect selected normalized event",
		"  w            toggle wrapping in inspection; ←/→ pan when unwrapped",
		"  r            refresh current evidence; rescan from overview screens",
		"  f            inspect rule assessments from the timeline",
		"  [ / ]        select assessment evidence; Enter opens timeline context",
		"  i            inspect indexing details from sessions",
		"  /            open search or edit the current search query",
		"",
		"Search editing",
		"  ←/→          move the query cursor",
		"  Home/End     beginning/end of query (also Ctrl-A/Ctrl-E)",
		"  Backspace/Del delete before/at the cursor",
		"  Paste        insert query text, including shortcut characters",
		"  Enter        submit query; Esc stops editing, then returns",
		"  r            retry search; edited queries require a new search",
		"  Filters      session: kind: after: before: file: tool: command:",
		"  Shortcuts such as q / ? [ ] remain text while editing.",
		"",
		"Evidence actions",
		"  x            inspect projection lifecycle from a timeline",
		"  u            inspect redacted retained evidence for an Unknown event",
		"  t            retry implemented pending or failed projections",
		"  b            rebuild the focused projection when implemented",
		"  a            rebuild all only when every projection is implemented",
	}
	return lines
}
