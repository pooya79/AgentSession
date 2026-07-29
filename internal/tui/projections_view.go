package tui

import (
	"fmt"

	"github.com/pooya79/AgentSession/internal/app"
)

// projectionLines renders canonical evidence availability independently from
// derived-data readiness and shows only application-safe diagnostics.
func (m Model) projectionLines() []string {
	lines := []string{"Projection lifecycle · session " + string(m.sessionsState.selected)}
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
		return append(lines, "", "Could not load projection status: "+m.projectionsState.err.Error())
	case m.projectionsState.status.State == app.EvidenceNotFound:
		return append(lines, "", "This session is no longer available.")
	}
	summary := m.projectionsState.status.Summary
	lines = append(lines,
		fmt.Sprintf("Canonical evidence remains available · derived usable %d/%d · pending %d · running %d · failed %d · stale %d · not implemented %d",
			summary.Usable, len(m.projectionsState.status.Projections), summary.Pending, summary.Running, summary.Failed, summary.Stale,
			summary.Unimplemented),
		"",
	)
	if m.projectionsState.err != nil {
		lines = append(lines, "Refresh failed; showing the last observed projection status.", "")
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
			flags += " · usable"
		}
		if state.Stale {
			flags += " · stale"
		}
		if !state.BuildAvailable {
			if state.Status == app.ProjectionStatusPending {
				flags = "not implemented in this build · remains pending"
			} else {
				flags += " · rebuild unavailable in this build"
			}
		}
		lines = append(lines, fmt.Sprintf("%s %-16s  %s  · target %s/%d · attempts %d",
			marker, state.Kind, flags, state.TargetVersion, state.TargetRevision, state.AttemptCount))
		if state.Diagnostic != nil {
			lines = append(lines, "    "+state.Diagnostic.Code+": "+state.Diagnostic.Summary)
		}
	}
	if m.projectionsState.status.Active || summary.Running > 0 {
		lines = append(lines, "", "Projection work is active and continues if you leave this panel.")
	}
	return lines
}
