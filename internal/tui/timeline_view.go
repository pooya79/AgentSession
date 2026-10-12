package tui

import (
	"strings"

	"github.com/pooya79/AgentSession/internal/app"
)

// timelineHeaderLines is shared by viewport sizing and timeline rendering so
// optional diagnostics and continuation failures reserve exactly their rows.
func (m Model) timelineHeaderLines() []string {
	label := "Timeline · session " + truncateCell(string(m.sessionsState.selected), max(12, m.renderWidth()/2-20))
	for _, session := range m.sessionsState.page.Sessions {
		if session.ID == m.sessionsState.selected {
			label = truncateCell(sessionLabel(session), m.renderWidth())
			if session.State != app.EvidenceComplete {
				label += " · " + evidenceLabel(session.State)
			}
			break
		}
	}
	lines := []string{label}
	if len(m.timelineState.page.Events) > 0 {
		if m.timelineState.loading {
			lines = append(lines, m.spinner.View()+" Loading local history… navigation remains available.")
		}
		if m.timelineState.err != nil {
			lines = append(lines, "Timeline read failed: "+safeInline(m.timelineState.err.Error()), "Loaded evidence retained · r to retry; scroll near end for continuation.")
		}
		if m.timelineState.page.State == app.EvidencePartial {
			lines = append(lines, diagnosticSummary(m.timelineState.page.Diagnostics))
		}
	}
	if m.timelineState.notice != "" {
		lines = append(lines, m.timelineState.notice)
	}
	return lines
}

// timelineLines renders the accumulated card timeline through one viewport.
func (m Model) timelineLines() []string {
	lines := m.timelineHeaderLines()
	switch {
	case m.timelineState.loading && (len(m.timelineState.page.Events) == 0 || m.timelineState.page.Payloads == nil):
		return append(lines, "", m.spinner.View()+" Loading timeline events…")
	case m.timelineState.err != nil && len(m.timelineState.page.Events) == 0:
		return append(lines, "", "Could not load timeline: "+m.timelineState.err.Error(), "Press r to retry.")
	case m.timelineState.page.State == app.EvidenceNotFound:
		return append(lines, "", "This session is no longer available.")
	case len(m.timelineState.page.Events) == 0:
		if m.timelineState.page.State == app.EvidenceUnavailable {
			return append(lines, "", "Timeline evidence is unavailable.", diagnosticSummary(m.timelineState.page.Diagnostics))
		}
		return append(lines, "", "This session has no normalized events.")
	}
	lines = append(lines, strings.Split(m.timelineState.viewport.View(), "\n")...)
	return lines
}
