package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/sanitization"
)

const timelinePrefetchThreshold = 5

type timelineCardRange struct {
	start int
	end   int
}

func (m *Model) resetTimelineState() {
	timelineViewport := m.timelineState.viewport
	timelineViewport.GotoTop()
	m.timelineState = timelineState{
		requestedCursors:  make(map[string]bool),
		expanded:          make(map[model.EventID]bool),
		inspections:       make(map[model.EventID]app.UnknownEvidenceInspection),
		inspectionErrors:  make(map[model.EventID]error),
		inspectionLoading: make(map[model.EventID]bool),
		viewport:          timelineViewport,
	}
}

func (m *Model) appendTimelinePage(next app.TimelinePage) {
	seen := make(map[model.EventID]bool, len(m.timelineState.page.Events))
	var lastSequence int64 = -1
	for _, event := range m.timelineState.page.Events {
		seen[event.ID] = true
		if event.Sequence > lastSequence {
			lastSequence = event.Sequence
		}
	}
	for _, event := range next.Events {
		if seen[event.ID] || event.Sequence <= lastSequence {
			continue
		}
		m.timelineState.page.Events = append(m.timelineState.page.Events, event)
		seen[event.ID] = true
		lastSequence = event.Sequence
	}
	if m.timelineState.page.Payloads == nil {
		m.timelineState.page.Payloads = make(map[model.EventID]model.NormalizedData)
	}
	for eventID, payload := range next.Payloads {
		if seen[eventID] {
			m.timelineState.page.Payloads[eventID] = payload
		}
	}
	m.timelineState.page.NextCursor = next.NextCursor
	m.timelineState.page.State = next.State
	m.timelineState.page.Diagnostics = next.Diagnostics
	m.timelineState.page.Interpretation = next.Interpretation
}

func (m *Model) prefetchTimelineIfNeeded() tea.Cmd {
	if m.screen != timelineScreen || m.timelineState.loading || m.services == nil {
		return nil
	}
	next := m.timelineState.page.NextCursor
	if next == "" || m.timelineState.requestedCursors[next] {
		return nil
	}
	nearSelection := len(m.timelineState.page.Events)-m.timelineState.cursor <= timelinePrefetchThreshold
	if !nearSelection && !m.timelineState.viewport.AtBottom() {
		return nil
	}
	ctx := m.replaceRequest()
	m.timelineState.loading = true
	m.timelineState.err = nil
	m.timelineState.pendingCursor = next
	m.timelineState.requestedCursors[next] = true
	return m.startSpinner(loadTimeline(
		ctx, m.services, m.requestGeneration, m.sessionsState.selected, next,
	))
}

func (m *Model) anchorTimelineSelection() {
	if m.screen != timelineScreen || m.timelineState.selected == "" {
		return
	}
	_, ranges := m.timelineContent()
	card, ok := ranges[m.timelineState.selected]
	if !ok {
		return
	}
	top := m.timelineState.viewport.YOffset()
	height := max(1, m.timelineState.viewport.Height())
	switch {
	case card.start < top:
		m.timelineState.viewport.SetYOffset(card.start)
	case card.end-card.start >= height && card.start >= top+height:
		m.timelineState.viewport.SetYOffset(card.start)
	case card.end-card.start >= height:
		// Keep the selected card's header anchored while expanded evidence
		// extends below the viewport.
		return
	case card.end > top+height:
		m.timelineState.viewport.SetYOffset(max(card.start, card.end-height))
	}
}

func (m Model) timelineContentLines() []string {
	lines, _ := m.timelineContent()
	return lines
}

func (m Model) timelineContent() ([]string, map[model.EventID]timelineCardRange) {
	lines := make([]string, 0, len(m.timelineState.page.Events)*4)
	ranges := make(map[model.EventID]timelineCardRange, len(m.timelineState.page.Events))
	width := max(20, m.renderWidth()-2)
	for index, event := range m.timelineState.page.Events {
		start := len(lines)
		card := timelineCardLines(
			event,
			m.timelineState.page.Payloads[event.ID],
			width,
			index == m.timelineState.cursor,
			m.timelineState.expanded[event.ID],
			m.timelineState.inspections[event.ID],
			m.timelineState.inspectionLoading[event.ID],
			m.timelineState.inspectionErrors[event.ID],
		)
		lines = append(lines, card...)
		ranges[event.ID] = timelineCardRange{start: start, end: len(lines)}
		lines = append(lines, "")
	}
	if m.timelineState.loading && len(m.timelineState.page.Events) > 0 {
		lines = append(lines, m.spinner.View()+" Loading more events…")
	} else if m.timelineState.page.NextCursor == "" && len(m.timelineState.page.Events) > 0 {
		lines = append(lines, fmt.Sprintf("End of timeline · %d events loaded", len(m.timelineState.page.Events)))
	} else if len(m.timelineState.page.Events) > 0 {
		lines = append(lines, fmt.Sprintf("%d events loaded · more available", len(m.timelineState.page.Events)))
	}
	return lines, ranges
}

func timelineCardLines(
	event model.EventSummary,
	payload model.NormalizedData,
	width int,
	selected bool,
	expanded bool,
	inspection app.UnknownEvidenceInspection,
	inspectionLoading bool,
	inspectionErr error,
) []string {
	marker := " "
	if selected {
		marker = ">"
	}
	if payload == nil {
		return []string{marker + " " + safeInline(event.Summary), "  Details unavailable."}
	}
	if message, ok := payload.(model.MessageData); ok {
		return timelineMessageLines(message, width, selected)
	}

	title := timelineActivityTitle(payload, event.Summary)
	lines := []string{marker + " " + title}
	if !expanded {
		if _, ok := payload.(model.UnknownData); ok {
			lines = append(lines, "  Press Enter for details · u to inspect retained evidence")
		} else {
			lines = append(lines, "  Press Enter to show details")
		}
		return lines
	}
	lines[0] = marker + " " + strings.Replace(title, "▸", "▾", 1)
	addField := func(label, value string) {
		lines = append(lines, wrappedField(label, value, width)...)
	}
	addLong := func(label, value string) {
		lines = append(lines, wrappedField(label, value, width)...)
	}

	switch value := payload.(type) {
	case model.SummaryData:
		addLong("Summary", value.Text)
	case model.ToolCallData:
		addField("Tool", value.ToolName)
		addField("Call ID", value.CallID)
		input := value.Input
		var decoded any
		if json.Unmarshal([]byte(input), &decoded) == nil {
			if encoded, err := json.MarshalIndent(decoded, "", "  "); err == nil {
				input = string(encoded)
			}
		}
		addField("Input", input)
	case model.ToolResultData:
		addField("Tool", value.ToolName)
		addField("Call ID", value.CallID)
		addField("Error", optionalBool(value.IsError))
		addLong("Output", value.Output)
	case model.CommandData:
		addField("Command", value.Command)
		addField("Working directory", reportedString(value.WorkingDirectory))
		addField("Exit status", optionalInt(value.ExitCode))
		addLong("Output", value.Output)
	case model.PatchData:
		addField("Paths", strings.Join(value.Paths, ", "))
		addLong("Patch", value.Text)
	case model.FileReadData:
		addField("Path", value.Path)
		addField("Lines", lineRange(value.StartLine, value.EndLine))
	case model.FileMutationData:
		addField("Operation", string(value.Operation))
		addField("Path", value.Path)
		if value.PreviousPath != "" {
			addField("Renamed from", value.PreviousPath)
		}
	case model.UsageData:
		addField("Tokens", fmt.Sprintf(
			"input %s · output %s · cache read %s · cache write %s",
			optionalInt64(value.InputTokens), optionalInt64(value.OutputTokens),
			optionalInt64(value.CacheReadTokens), optionalInt64(value.CacheWriteTokens),
		))
	case model.ErrorData:
		addField("Code", reportedString(value.Code))
		addLong("Error", value.Message)
	case model.UnknownData:
		addField("Reason", string(value.Reason))
		addField("Original kind", value.OriginalKind)
		switch {
		case inspectionLoading:
			lines = append(lines, "  Loading bounded, redacted retained evidence…")
		case inspectionErr != nil:
			lines = append(lines, "  Could not inspect retained evidence: "+safeInline(inspectionErr.Error()))
		case inspection.EventID != "":
			switch inspection.State {
			case app.EvidenceUnavailable:
				lines = append(lines, "  Retained evidence for this event is unavailable.")
			case app.EvidenceNotFound:
				lines = append(lines, "  This event's retained evidence could not be located.")
			default:
				addLong("Retained evidence", inspection.Text)
				lines = append(lines, fmt.Sprintf(
					"  Returned %d/%d bytes · %d redactions · truncated %t",
					inspection.ReturnedSize, inspection.OriginalSize, inspection.RedactionCount, inspection.Truncated,
				))
			}
		default:
			lines = append(lines, "  Press u to inspect bounded, redacted retained evidence.")
		}
	}
	lines = append(lines, "  Enter to hide details")
	return lines
}

func timelinePayloadTruncatable(payload model.NormalizedData) bool {
	if payload == nil {
		return false
	}
	_, message := payload.(model.MessageData)
	return !message
}

func timelineMessageLines(message model.MessageData, width int, selected bool) []string {
	label := "Message"
	indent := ""
	switch message.Role {
	case model.MessageRoleUser:
		label = "You"
		indent = "      "
	case model.MessageRoleAssistant:
		label = "Assistant"
	case model.MessageRoleSystem:
		label = "System"
	case model.MessageRoleOther:
		label = "Participant"
	case model.MessageRoleUnknown:
		label = "Message · role unreported"
	}
	marker := " "
	if selected {
		marker = ">"
	}
	header := marker + " " + indent + "╭─ " + label
	bodyPrefix := "  " + indent + "│ "
	footer := "  " + indent + "╰─"
	safe := sanitization.Terminal(strings.ToValidUTF8(message.Text, "\uFFFD"))
	if safe == "" {
		safe = "—"
	}
	wrapped := strings.Split(ansi.Wrap(safe, max(1, width-ansi.StringWidth(bodyPrefix)), ""), "\n")
	lines := []string{header}
	for _, line := range wrapped {
		lines = append(lines, bodyPrefix+line)
	}
	return append(lines, footer)
}

func timelineActivityTitle(payload model.NormalizedData, fallback string) string {
	var title string
	switch value := payload.(type) {
	case model.SummaryData:
		title = "Conversation context"
	case model.ToolCallData:
		title = "Used tool"
		if value.ToolName != "" {
			title += " · " + value.ToolName
		}
	case model.ToolResultData:
		title = "Tool finished"
		if value.ToolName != "" {
			title += " · " + value.ToolName
		}
		if value.IsError != nil && *value.IsError {
			title += " · failed"
		}
	case model.CommandData:
		title = "Ran a command"
	case model.PatchData:
		title = "Changed files"
		if len(value.Paths) > 0 {
			title += " · " + value.Paths[0]
			if len(value.Paths) > 1 {
				title += fmt.Sprintf(" +%d more", len(value.Paths)-1)
			}
		}
	case model.FileReadData:
		title = "Read file"
		if value.Path != "" {
			title += " · " + value.Path
		}
	case model.FileMutationData:
		switch value.Operation {
		case model.FileMutationUpdate:
			title = "Updated file"
		case model.FileMutationDelete:
			title = "Deleted file"
		case model.FileMutationCreate:
			title = "Created file"
		case model.FileMutationRename:
			title = "Renamed file"
		default:
			title = "Changed file"
		}
		if value.Path != "" {
			title += " · " + value.Path
		}
	case model.UsageData:
		title = "Usage details"
	case model.ErrorData:
		title = "Error"
		if value.Code != "" {
			title += " · " + value.Code
		}
	case model.UnknownData:
		title = "Unsupported session activity"
		if value.OriginalKind != "" {
			title += " · " + value.OriginalKind
		}
	default:
		title = fallback
	}
	return "▸ " + safeInline(title)
}

func wrappedField(label, value string, width int) []string {
	prefix := "  " + safeInline(label) + ": "
	safe := sanitization.Terminal(strings.ToValidUTF8(value, "\uFFFD"))
	if safe == "" {
		safe = "—"
	}
	wrapped := ansi.Wrap(safe, max(1, width-ansi.StringWidth(prefix)), "")
	parts := strings.Split(wrapped, "\n")
	for index := range parts {
		if index == 0 {
			parts[index] = prefix + parts[index]
		} else {
			parts[index] = strings.Repeat(" ", ansi.StringWidth(prefix)) + parts[index]
		}
	}
	return parts
}

func safeInline(value string) string {
	safe := sanitization.Terminal(strings.ToValidUTF8(value, "\uFFFD"))
	return strings.Join(strings.Fields(safe), " ")
}

func optionalBool(value *bool) string {
	if value == nil {
		return "unreported"
	}
	return strconv.FormatBool(*value)
}

func optionalInt(value *int) string {
	if value == nil {
		return "unreported"
	}
	return strconv.Itoa(*value)
}

func optionalInt64(value *int64) string {
	if value == nil {
		return "unreported"
	}
	return strconv.FormatInt(*value, 10)
}

func reportedString(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unreported"
	}
	return value
}

func lineRange(start, end *int64) string {
	switch {
	case start == nil && end == nil:
		return "unreported"
	case start != nil && end != nil:
		return fmt.Sprintf("%d–%d", *start, *end)
	case start != nil:
		return fmt.Sprintf("from %d", *start)
	default:
		return fmt.Sprintf("through %d", *end)
	}
}
