package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// detailContentLines renders normalized evidence as indented JSON and never renders
// the retained raw-record contents.
func (m Model) detailContentLines() []string {
	lines := []string{"Event detail · session " + string(m.sessionsState.selected)}
	switch {
	case m.detailState.loading && m.detailState.detail.Event.ID == "":
		return append(lines, "", m.spinner.View()+" Loading normalized payload…")
	case m.detailState.err != nil && m.detailState.detail.Event.ID == "":
		return append(lines, "", "Could not load event: "+m.detailState.err.Error(), "Press r to retry.")
	case m.detailState.detail.State == app.EvidenceNotFound:
		return append(lines, "", "This event is no longer available.")
	}
	if m.detailState.err != nil {
		lines = append(lines, "Refresh failed; showing the last loaded event. Press r to retry.", "")
	} else if m.detailState.loading {
		lines = append(lines, m.spinner.View()+" Refreshing normalized payload.", "")
	}
	event := m.detailState.detail.Event
	lines = append(lines,
		fmt.Sprintf("#%d · %s · %s", event.Sequence, event.Kind, evidenceLabel(m.detailState.detail.State)),
		event.Summary,
		fmt.Sprintf("Interpretation: %s · %d Unknown events · %d malformed records",
			m.detailState.detail.Interpretation.State,
			m.detailState.detail.Interpretation.UnknownEvents,
			m.detailState.detail.Interpretation.MalformedRecords),
	)
	if m.detailState.detail.Diagnostics.Total > 0 {
		lines = append(lines, diagnosticSummary(m.detailState.detail.Diagnostics))
		for _, diagnostic := range m.detailState.detail.Diagnostics.Diagnostics {
			lines = append(lines, fmt.Sprintf("[%s] %s: %s", diagnostic.Severity, diagnostic.Code, diagnostic.Message))
		}
	}
	lines = append(lines, "", "Normalized payload")
	if m.detailState.detail.State == app.EvidenceUnavailable || m.detailState.detail.Payload == nil {
		lines = append(lines, "Payload evidence is unavailable.")
		return lines
	}
	payload, err := json.MarshalIndent(m.detailState.detail.Payload, "", "  ")
	if err != nil {
		lines = append(lines, "Could not render normalized payload: "+err.Error())
	} else {
		lines = append(lines, strings.Split(string(payload), "\n")...)
	}
	if event.Kind == model.EventKindUnknown {
		lines = append(lines, "", "Retained Unknown evidence")
		switch {
		case m.detailState.inspectionLoading:
			lines = append(lines, m.spinner.View()+" Loading and redacting retained evidence…")
		case m.detailState.inspectionErr != nil:
			lines = append(lines, "Inspection failed: "+m.detailState.inspectionErr.Error())
		case m.detailState.inspection.State == app.EvidenceUnavailable:
			lines = append(lines, "Retained evidence for this event is no longer available.")
		case m.detailState.inspection.State == app.EvidenceNotFound:
			lines = append(lines, "This event's retained evidence could not be located.")
		case m.detailState.inspection.EventID != "":
			inspection := m.detailState.inspection
			lines = append(lines, fmt.Sprintf("%d of %d bytes · %d redactions · truncated: %t",
				inspection.ReturnedSize, inspection.OriginalSize, inspection.RedactionCount, inspection.Truncated))
			lines = append(lines, strings.Split(inspection.Text, "\n")...)
		default:
			lines = append(lines, "Press u to inspect a redacted, bounded copy.")
		}
	}
	return lines
}
