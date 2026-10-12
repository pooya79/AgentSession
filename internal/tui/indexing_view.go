package tui

import (
	"fmt"

	"github.com/pooya79/AgentSession/internal/app"
)

// indexingContentLines renders aggregate progress, per-source status, failures, and
// the coordinator's bounded diagnostic synopsis.
func (m Model) indexingContentLines() []string {
	status := m.indexingState.status
	lines := []string{"Indexing details", ""}
	switch {
	case m.indexingState.err != nil:
		lines = append(lines, "AgentSession indexing observation failed: "+m.indexingState.err.Error(), "Status unavailable · r to rescan.")
	case status.Failure != "":
		lines = append(lines, "AgentSession indexing failed; available evidence was retained.")
	case status.Active || status.Phase == app.ImportAllIndexing:
		lines = append(lines, "Indexing is active. It continues while you browse imported sessions.")
	case status.Phase == app.ImportAllUpToDate:
		lines = append(lines, "Indexing completed successfully.")
	case status.Phase == app.ImportAllIssues:
		lines = append(lines, "Indexing completed with issues; available evidence was retained.")
	default:
		lines = append(lines, "Indexing is unavailable.")
	}
	if status.Failure != "" {
		lines = append(lines, "Failure: "+status.Failure)
	}
	lines = append(lines,
		fmt.Sprintf("Sources: %d discovered · %d completed · %d failed", status.SourcesDiscovered, status.SourcesCompleted, status.SourcesFailed),
		fmt.Sprintf("Totals: %d records · %d events · %d sessions · %d unchanged", status.RecordsProcessed, status.EventsProcessed, status.SessionsObserved, status.UnchangedSessions),
		fmt.Sprintf("Diagnostics: %d total · %d omitted from bounded status", status.DiagnosticsTotal, status.DiagnosticsOmitted),
	)
	if len(status.Sources) == 0 {
		lines = append(lines, "", "No discovered sources.")
	} else {
		lines = append(lines, "", "Discovered sources")
		for _, source := range status.Sources {
			state := string(source.Phase)
			if source.Failure != "" {
				state += " · failed: " + source.Failure
			}
			lines = append(lines,
				fmt.Sprintf("%s · %s · %s", source.Kind, source.ID, state),
				fmt.Sprintf("  %s · %s · %d records · %d events · %d sessions · %d diagnostics",
					source.Path, source.Origin, source.Records, source.Events, source.Sessions, source.Diagnostics),
			)
		}
	}
	if len(status.RecentDiagnostics) > 0 {
		lines = append(lines, "", "AgentSession discovery/import diagnostics")
		for _, diagnostic := range status.RecentDiagnostics {
			where := string(diagnostic.SourceID)
			if where == "" {
				where = diagnostic.SourcePath
			}
			lines = append(lines, fmt.Sprintf("[%s] %s · %s · %s", diagnostic.Severity, diagnostic.Code, where, diagnostic.Message))
		}
	}
	return lines
}
