package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/sanitization"
)

// sessionsLines renders the current bounded sessions page and its evidence
// quality without exposing source-specific data.
func (m Model) sessionsLines() []string {
	lines := []string{"Sessions dashboard"}
	lines = append(lines, m.metricsLines()...)
	switch {
	case m.sessionsState.loading && len(m.sessionsState.page.Sessions) == 0:
		return append(lines, "", m.spinner.View()+" Loading imported sessions…")
	case m.sessionsState.err != nil && len(m.sessionsState.page.Sessions) == 0:
		return append(lines, "", "Could not load sessions: "+m.sessionsState.err.Error(), "Press r to retry.")
	case len(m.sessionsState.page.Sessions) == 0:
		lines = append(lines, "", "No imported sessions are available.")
		if m.indexingState.status.Active {
			lines = append(lines, "Indexing continues in the background; this list refreshes when it completes.")
		} else if m.indexingState.status.SourcesDiscovered == 0 && m.indexingState.status.Phase == app.ImportAllUpToDate {
			lines = append(lines, "No supported sources were discovered. Press r to rescan.")
		}
		return lines
	}
	if m.sessionsState.err != nil {
		lines = append(lines, "Refresh failed; showing the last loaded page. Press r to retry.")
	} else if m.sessionsState.loading {
		lines = append(lines, m.spinner.View()+" Refreshing sessions; current evidence remains visible.")
	}
	switch m.sessionsState.page.State {
	case app.EvidencePartial:
		lines = append(lines, "Some sessions contain diagnostics; available evidence is still shown.")
	case app.EvidenceUnavailable:
		lines = append(lines, "Session evidence is unavailable.")
	}
	lines = append(lines, "")
	compactLayout := m.renderWidth() < 72 || m.height > 0 && m.height < 18
	rowHeight := 1
	if m.renderWidth() < 110 {
		rowHeight = 2
	}
	if compactLayout {
		rowHeight = 3
	}
	visible := max(1, (m.contentHeight()-len(lines)-1)/rowHeight)
	start := windowStart(m.sessionsState.cursor, len(m.sessionsState.page.Sessions), visible)
	end := min(len(m.sessionsState.page.Sessions), start+visible)
	if !compactLayout && m.renderWidth() >= 110 {
		lines = append(lines, "  LAST ACTIVITY ↓       AGENT       SESSION / PREVIEW                         EVENTS  CANONICAL  DERIVED")
	} else if !compactLayout {
		lines = append(lines, "  LAST ACTIVITY ↓       AGENT       SESSION / PREVIEW")
	}
	for i := start; i < end; i++ {
		session := m.sessionsState.page.Sessions[i]
		marker := " "
		if i == m.sessionsState.cursor {
			marker = ">"
		}
		title := sessionLabel(session)
		agent := strings.ToUpper(session.AgentName)
		activity := formatActivity(session.LastActivityAt)
		derived := compactDerived(session.Projections)
		if compactLayout {
			lines = append(lines, fmt.Sprintf("%s %s", marker, truncateCell(title, max(1, m.renderWidth()-2))))
			lines = append(lines, fmt.Sprintf("  %s · %s · %d events · canonical %s · %s",
				activity, agent, session.EventCount, evidenceLabel(session.State), derived))
			if session.Preview != "" && session.Preview != title {
				lines = append(lines, "  "+truncateCell(session.Preview, max(1, m.renderWidth()-2)))
			} else {
				lines = append(lines, "")
			}
		} else if m.renderWidth() < 110 {
			lines = append(lines, fmt.Sprintf("%s %-21s %-11s %s",
				marker, truncateCell(activity, 21), truncateCell(agent, 11),
				truncateCell(title, max(1, m.renderWidth()-38))))
			lines = append(lines, fmt.Sprintf("  %d events · canonical %s · %s%s",
				session.EventCount, evidenceLabel(session.State), derived, previewSuffix(session, m.renderWidth()-46)))
		} else {
			label := title
			if session.Preview != "" && session.Preview != title {
				label += " — " + session.Preview
			}
			lines = append(lines, fmt.Sprintf("%s %-21s %-11s %-41s %6d  %-9s  %s",
				marker, truncateCell(activity, 21), truncateCell(agent, 11),
				truncateCell(label, 41), session.EventCount, evidenceLabel(session.State), derived))
		}
	}
	lines = append(lines, fmt.Sprintf("Page %d · %d shown%s", m.sessionsState.pageNumber+1, len(m.sessionsState.page.Sessions), nextLabel(m.sessionsState.page.NextCursor)))
	return lines
}

func (m Model) metricsLines() []string {
	labels := []string{"Sessions", "Events", "Agents", "Evidence issues"}
	values := []int64{m.sessionsState.overview.Sessions, m.sessionsState.overview.Events, m.sessionsState.overview.Agents, m.sessionsState.overview.IssueSessions}
	render := func(index, width int) string {
		value := fmt.Sprintf("%d", values[index])
		if m.sessionsState.overviewLoading {
			value = "…"
		} else if m.sessionsState.overviewErr != nil {
			value = "— unavailable"
		}
		return "┌" + strings.Repeat("─", max(1, width-2)) + "┐\n" +
			"│" + padCell(" "+labels[index], max(1, width-2)) + "│\n" +
			"│" + padCell(" "+value, max(1, width-2)) + "│\n" +
			"└" + strings.Repeat("─", max(1, width-2)) + "┘"
	}
	width := m.renderWidth()
	if width < 72 || m.height > 0 && m.height < 18 {
		parts := make([]string, len(labels))
		for i := range labels {
			value := fmt.Sprintf("%d", values[i])
			if m.sessionsState.overviewLoading {
				value = "…"
			} else if m.sessionsState.overviewErr != nil {
				value = "— unavailable"
			}
			parts[i] = labels[i] + " " + value
		}
		return []string{strings.Join(parts, " · ")}
	}
	columns := 4
	if width < 110 {
		columns = 2
	}
	cardWidth := max(12, (width-(columns-1))/columns)
	rows := make([]string, 0, 6)
	for start := 0; start < len(labels); start += columns {
		cards := make([][]string, 0, columns)
		for i := start; i < min(len(labels), start+columns); i++ {
			cards = append(cards, strings.Split(render(i, cardWidth), "\n"))
		}
		for line := 0; line < 4; line++ {
			parts := make([]string, len(cards))
			for i := range cards {
				parts[i] = cards[i][line]
			}
			rows = append(rows, strings.Join(parts, " "))
		}
	}
	return rows
}

func sessionLabel(session app.SessionSummary) string {
	if strings.TrimSpace(session.Title) != "" {
		return session.Title
	}
	if session.Preview != "" {
		return session.Preview
	}
	return truncateCell(string(session.ID), 28)
}

func previewSuffix(session app.SessionSummary, width int) string {
	if session.Preview == "" || session.Preview == sessionLabel(session) || width <= 4 {
		return ""
	}
	return " · " + truncateCell(session.Preview, width)
}

func formatActivity(value *time.Time) string {
	if value == nil {
		return "—"
	}
	return value.UTC().Format("2006-01-02 15:04 UTC")
}

func compactDerived(summary app.ProjectionSummary) string {
	result := fmt.Sprintf("derived %d usable", summary.Usable)
	if summary.Unimplemented > 0 {
		result += fmt.Sprintf(", %d n/a", summary.Unimplemented)
	}
	return result
}

func truncateCell(value string, width int) string {
	value = sanitization.Terminal(value)
	if ansi.StringWidth(value) <= width {
		return value
	}
	if width <= 1 {
		return ansi.Truncate(value, width, "")
	}
	return ansi.Truncate(value, width-1, "") + "…"
}

func padCell(value string, width int) string {
	value = truncateCell(value, width)
	return value + strings.Repeat(" ", max(0, width-ansi.StringWidth(value)))
}
