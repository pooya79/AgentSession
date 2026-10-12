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
	lines := []string{"Recent · last activity ↓"}
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
	if m.sessionsState.page.State == app.EvidenceUnavailable {
		lines = append(lines, "Session evidence is unavailable.")
	}
	lines = append(lines, "")
	width := m.renderWidth()
	wide := width >= 110 && (m.height <= 0 || m.height >= 24)
	listWidth := width
	if wide {
		listWidth = min(52, width*45/100)
	}
	available := max(1, m.contentHeight()-len(lines)-1)
	rowHeight := 2
	if m.sessionsState.showDetails {
		rowHeight = 3
	}
	visible := max(1, available/rowHeight)
	start := windowStart(m.sessionsState.cursor, len(m.sessionsState.page.Sessions), visible)
	end := min(len(m.sessionsState.page.Sessions), start+visible)
	var list []string
	for i := start; i < end; i++ {
		session := m.sessionsState.page.Sessions[i]
		marker := " "
		if i == m.sessionsState.cursor {
			marker = ">"
		}
		metadata := strings.ToUpper(safeInline(session.AgentName)) + " · " + formatActivity(session.LastActivityAt)
		if session.State != app.EvidenceComplete {
			metadata = evidenceLabel(session.State) + " · " + metadata
		}
		list = append(list, marker+" "+truncateCell(sessionLabel(session), listWidth-2), "  "+truncateCell(metadata, listWidth-2))
		if m.sessionsState.showDetails {
			list = append(list, "  "+truncateCell(fmt.Sprintf("%d events · %s", session.EventCount, evidenceLabel(session.State)), listWidth-2))
		}
	}
	if wide {
		preview := m.sessionPreviewLines(width - listWidth - 3)
		for i := range min(available, max(len(list), len(preview))) {
			left, right := "", ""
			if i < len(list) {
				left = list[i]
			}
			if i < len(preview) {
				right = preview[i]
			}
			lines = append(lines, padCell(left, listWidth)+" │ "+right)
		}
	} else {
		lines = append(lines, list...)
	}
	if m.sessionsState.pageNumber > 0 || m.sessionsState.page.NextCursor != "" {
		lines = append(lines, fmt.Sprintf("Page %d%s", m.sessionsState.pageNumber+1, nextLabel(m.sessionsState.page.NextCursor)))
	}
	return lines
}

// sessionPreviewLines uses only the already-loaded shared session summary.
// No source-specific interpretation or inferred session outcome belongs here.
func (m Model) sessionPreviewLines(width int) []string {
	session := m.sessionsState.page.Sessions[m.sessionsState.cursor]
	lines := []string{"PREVIEW"}
	lines = append(lines, "Outcome: Unknown (not assessed)")
	if m.sessionsState.showDetails {
		lines = append(lines,
			"Canonical: "+evidenceLabel(session.State),
			"Interpretation: "+reportedString(string(session.Interpretation.State)),
			fmt.Sprintf("%d events · %d Unknown · %d diagnostics", session.EventCount, session.Interpretation.UnknownEvents, session.Diagnostics.Total),
			compactDerived(session.Projections))
	} else if session.State != app.EvidenceComplete || session.Interpretation.UnknownEvents > 0 || session.Diagnostics.Total > 0 {
		lines = append(lines, fmt.Sprintf("%s · %d Unknown · %d diagnostics", evidenceLabel(session.State), session.Interpretation.UnknownEvents, session.Diagnostics.Total))
	}
	preview := session.Preview
	if preview == "" {
		preview = "Preview evidence unavailable."
	}
	listTextWidth := m.renderWidth() - width - 5
	if preview != sessionLabel(session) || ansi.StringWidth(safeInline(preview)) > listTextWidth || m.sessionsState.showDetails {
		lines = append(lines, "")
		lines = append(lines, strings.Split(ansi.Wrap(safeInline(preview), width, ""), "\n")...)
	}
	if session.Summary != "" && session.Summary != session.Preview {
		lines = append(lines, "")
		lines = append(lines, strings.Split(ansi.Wrap(safeInline(session.Summary), width, ""), "\n")...)
	}
	return lines
}

func (m Model) metricsLines() []string {
	labels := []string{"Sessions", "Events", "Agents", "Evidence issues"}
	values := []int64{m.sessionsState.overview.Sessions, m.sessionsState.overview.Events, m.sessionsState.overview.Agents, m.sessionsState.overview.IssueSessions}
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
	if !m.sessionsState.showDetails {
		if m.sessionsState.overview.IssueSessions > 0 || m.sessionsState.overviewErr != nil {
			return []string{parts[3]}
		}
		return nil
	}
	if m.renderWidth() < 80 {
		return []string{strings.Join(parts[:2], " · "), strings.Join(parts[2:], " · ")}
	}
	return []string{strings.Join(parts, " · ")}
}

func sessionLabel(session app.SessionSummary) string {
	if strings.TrimSpace(session.Title) != "" {
		return safeInline(session.Title)
	}
	if session.Preview != "" {
		return safeInline(session.Preview)
	}
	return truncateCell(safeInline(string(session.ID)), 28)
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
