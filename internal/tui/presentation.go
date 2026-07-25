package tui

import (
	"strings"

	"github.com/pooya79/AgentSession/internal/sanitization"
)

func (m *Model) syncViewports() {
	width := max(1, m.renderWidth())
	height := max(1, m.contentHeight())

	m.detailState.viewport.SetWidth(width)
	m.detailState.viewport.SetHeight(height)
	m.detailState.viewport.SetContentLines(sanitizeLines(m.detailContentLines()))

	m.indexingState.viewport.SetWidth(width)
	m.indexingState.viewport.SetHeight(height)
	m.indexingState.viewport.SetContentLines(sanitizeLines(m.indexingContentLines()))

	m.helpViewport.SetWidth(width)
	m.helpViewport.SetHeight(height)
	m.helpViewport.SetContentLines(sanitizeLines(m.helpLines()))
}

func sanitizeLines(lines []string) []string {
	safe := make([]string, len(lines))
	for index, line := range lines {
		safe[index] = sanitization.Terminal(line)
	}
	return safe
}

func (m Model) styleLines(lines []string) []string {
	safe := sanitizeLines(lines)
	for index, line := range safe {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		switch {
		case index == 0:
			safe[index] = m.theme.accent.Render(line)
		case index == 1:
			safe[index] = styleStatusLine(m.theme, line)
		case strings.HasPrefix(line, ">"):
			safe[index] = m.theme.focused.Render(line)
		case strings.Contains(lower, "could not ") || strings.Contains(lower, "unavailable") || strings.Contains(lower, "failed"):
			safe[index] = m.theme.danger.Render(line)
		case strings.Contains(lower, "partial") || strings.Contains(lower, "issues") || strings.Contains(lower, "diagnostic"):
			safe[index] = m.theme.warning.Render(line)
		case strings.Contains(lower, "complete") || strings.Contains(lower, "usable"):
			safe[index] = m.theme.success.Render(line)
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

func isScreenTitle(line string) bool {
	for _, prefix := range []string{
		"Imported sessions", "Indexing details", "Timeline", "Event detail",
		"Projection lifecycle", "Keyboard help",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

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
		"  ↑/↓ or j/k   move one row or line",
		"  Home/End     jump to the first or last row",
		"  g/G          jump to the first or last row",
		"  PgUp/PgDn    scroll long evidence or change bounded pages",
		"  Enter        open the focused session or event",
		"  n/p          next or previous bounded page",
		"",
		"Actions",
		"  r            refresh current evidence; rescan from overview screens",
		"  i            inspect indexing details from sessions",
		"  x            inspect projection lifecycle from a timeline",
		"  t            retry implemented pending or failed projections",
		"  b            rebuild the focused projection when implemented",
		"  a            rebuild all only when every projection is implemented",
	}
	return lines
}
