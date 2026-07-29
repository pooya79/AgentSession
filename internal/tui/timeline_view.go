package tui

import (
	"fmt"
	"strings"

	"github.com/pooya79/AgentSession/internal/app"
)

// timelineHeaderLines is shared by viewport sizing and timeline rendering so
// optional diagnostics and continuation failures reserve exactly their rows.
func (m Model) timelineHeaderLines() []string {
	lines := []string{"Timeline · session " + string(m.sessionsState.selected)}
	for _, session := range m.sessionsState.page.Sessions {
		if session.ID == m.sessionsState.selected {
			lines = append(lines, fmt.Sprintf("Canonical evidence: %s · projections: %d usable, %d pending, %d running, %d failed, %d stale",
				evidenceLabel(session.State), session.Projections.Usable, session.Projections.Pending,
				session.Projections.Running, session.Projections.Failed, session.Projections.Stale))
			break
		}
	}
	if len(m.timelineState.page.Events) > 0 {
		if m.timelineState.err != nil {
			lines = append(lines, "Timeline continuation failed; loaded cards remain available. Move near the end or press r to retry.")
		}
		if m.timelineState.page.State == app.EvidencePartial {
			lines = append(lines, diagnosticSummary(m.timelineState.page.Diagnostics))
		}
		lines = append(lines, "")
	}
	return lines
}

// timelineLines renders the accumulated card timeline through one viewport.
func (m Model) timelineLines() []string {
	lines := m.timelineHeaderLines()
	switch {
	case m.timelineState.loading && len(m.timelineState.page.Events) == 0:
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
