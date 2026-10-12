package tui

import (
	"fmt"
	"strings"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// detailHeaderLines keeps event identity/result visible while payload text is
// panned horizontally. Full envelope metadata remains in the scrolling body.
func (m Model) detailHeaderLines() []string {
	event := m.detailState.detail.Event
	if event.ID == "" {
		return []string{"Inspection · normalized evidence"}
	}
	title := strings.TrimPrefix(timelineActivityTitle(m.detailState.detail.Payload, string(event.Kind)), "▸ ")
	if command, ok := m.detailState.detail.Payload.(model.CommandData); ok {
		title = strings.TrimSuffix(title, " · "+safeInline(command.Command))
	}
	lines := []string{fmt.Sprintf("Inspection #%d · %s", event.Sequence, title)}
	if m.detailState.err != nil {
		lines = append(lines, "Inspection refresh failed: "+safeInline(m.detailState.err.Error()), "Previous evidence retained · r retry")
	} else if m.detailState.loading {
		lines = append(lines, "Refreshing inspection · previous evidence retained")
	}
	return lines
}

// detailContentLines renders normalized evidence for reading. Retained raw
// records stay behind the explicit bounded/redacted Unknown inspection action.
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
		"Event: "+string(event.ID),
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
	metadata := lines
	lines = nil
	if m.detailState.detail.State == app.EvidenceUnavailable || m.detailState.detail.Payload == nil {
		lines = append(lines, "Payload evidence is unavailable.", "")
	} else if command, ok := m.detailState.detail.Payload.(model.CommandData); ok {
		lines = append(lines, "$ "+safeInline(command.Command), "")
		output := command.Output
		if output == "" {
			output = "No output recorded."
		}
		lines = append(lines, strings.Split(output, "\n")...)
	} else if unknown, ok := m.detailState.detail.Payload.(model.UnknownData); ok {
		lines = append(lines, strings.ReplaceAll(string(unknown.Reason), "_", " "))
	} else {
		card := timelineCardLines(event, m.detailState.detail.Payload, 1<<30, false, true, app.UnknownEvidenceInspection{}, false, nil)
		if timelinePayloadTruncatable(m.detailState.detail.Payload) {
			card = card[1 : len(card)-1]
		}
		lines = append(lines, card...)
	}
	if m.detailState.showMetadata {
		lines = append(lines, "", "Event metadata")
		lines = append(lines, metadata...)
		switch payload := m.detailState.detail.Payload.(type) {
		case model.CommandData:
			lines = append(lines, "Working directory: "+reportedString(payload.WorkingDirectory), "Exit status: "+optionalInt(payload.ExitCode),
				"Invocation ID: "+reportedString(payload.InvocationID), "Phase: "+reportedString(string(payload.Phase)))
		case model.ToolCallData:
			lines = append(lines, "Call ID: "+reportedString(payload.CallID))
		case model.ToolResultData:
			lines = append(lines, "Call ID: "+reportedString(payload.CallID))
		case model.PatchData:
			lines = append(lines, "Change ID: "+reportedString(payload.ChangeID))
		case model.UnknownData:
			lines = append(lines, "Original kind: "+safeInline(payload.OriginalKind))
		}
	} else if m.detailState.detail.Diagnostics.Total > 0 {
		lines = append(lines, "", diagnosticSummary(m.detailState.detail.Diagnostics), "v shows diagnostics and metadata")
	}
	if event.Kind == model.EventKindUnknown {
		lines = append(lines, "", "Retained Unknown evidence")
		switch {
		case m.detailState.inspectionLoading:
			lines = append(lines, m.spinner.View()+" Loading and redacting retained evidence…")
		case m.detailState.inspectionErr != nil:
			lines = append(lines, "Inspection failed: "+m.detailState.inspectionErr.Error(), "Press u to retry retained inspection.")
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
