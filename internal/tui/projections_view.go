package tui

import (
	"fmt"

	"github.com/pooya79/AgentSession/internal/app"
)

// projectionLines renders canonical evidence availability independently from
// derived-data readiness and shows only application-safe diagnostics.
func (m Model) projectionLines() []string {
	lines := []string{"Derived views · session " + truncateCell(string(m.sessionsState.selected), max(12, m.renderWidth()-25))}
	if m.projectionsState.confirmAll {
		lines = append(lines, "",
			"Rebuild every projection?",
			"Confirm [y] / cancel [n or Esc]",
			"This invalidates current derived output; all registered builders are available.")
		return lines
	}
	switch {
	case m.projectionsState.loading && len(m.projectionsState.status.Projections) == 0:
		return append(lines, "", m.spinner.View()+" Loading projection status…")
	case m.projectionsState.err != nil && len(m.projectionsState.status.Projections) == 0:
		return append(lines, "", "Could not load projection status: "+m.projectionsState.err.Error(), "Press r to retry.")
	case m.projectionsState.status.State == app.EvidenceNotFound:
		return append(lines, "", "This session is no longer available.")
	case m.projectionsState.status.State != app.EvidenceComplete && m.projectionsState.status.State != app.EvidencePartial:
		return append(lines, "", "Projection status unavailable · r to retry.")
	}
	summary := m.projectionsState.status.Summary
	if m.projectionsState.showDetails {
		lines = append(lines, fmt.Sprintf("Canonical evidence remains available · %s · derived usable %d/%d · pending %d · running %d · failed %d · stale %d · not implemented %d",
			evidenceLabel(m.projectionsState.status.State), summary.Usable, len(m.projectionsState.status.Projections), summary.Pending, summary.Running, summary.Failed, summary.Stale,
			summary.Unimplemented))
	} else {
		lines = append(lines, "Canonical evidence: "+evidenceLabel(m.projectionsState.status.State))
	}
	lines = append(lines, "")
	if m.projectionsState.err != nil {
		lines = append(lines, "Refresh/action failed: "+m.projectionsState.err.Error(), "Previous snapshot · freshness unconfirmed · r to retry.", "")
	} else if m.projectionsState.loading {
		lines = append(lines, m.spinner.View()+" Refreshing projection status.", "")
	}
	if m.projectionsState.actionNotice != "" {
		lines = append(lines, m.projectionsState.actionNotice, "")
	}
	if diagnostic := m.projectionsState.status.OperationDiagnostic; diagnostic != nil {
		lines = append(lines, diagnostic.Code+": "+diagnostic.Summary, "")
	}
	for index, state := range m.projectionsState.status.Projections {
		marker := " "
		if index == m.projectionsState.cursor {
			marker = ">"
		}
		flags := string(state.Status)
		if state.Usable {
			if m.projectionsState.showDetails {
				flags += " · usable"
			}
		}
		if state.Stale {
			flags += " · stale"
		}
		if !state.BuildAvailable {
			if state.Status == app.ProjectionStatusPending {
				flags = "not implemented in this build"
			} else {
				flags += " · rebuild unavailable in this build"
			}
		}
		line := fmt.Sprintf("%s %-16s  %s", marker, state.Kind, flags)
		if m.projectionsState.showDetails {
			line += fmt.Sprintf(" · target %s/%d · attempts %d", state.TargetVersion, state.TargetRevision, state.AttemptCount)
		}
		lines = append(lines, line)
		if state.Diagnostic != nil {
			lines = append(lines, "    "+state.Diagnostic.Code+": "+state.Diagnostic.Summary)
		}
	}
	if !m.projectionsState.showDetails {
		lines = append(lines, "", "v versions, attempts and counters")
	}
	if m.projectionsState.status.Active || summary.Running > 0 {
		lines = append(lines, "", "Projection work is active and continues if you leave this panel.")
	}
	return lines
}
