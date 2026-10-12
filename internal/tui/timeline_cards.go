package tui

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"unicode"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

type timelineCardRange struct {
	start int
	end   int
	// offsets locate rows within content independently of wrapping. Whitespace
	// added or removed by wrapping/field indentation does not advance position.
	offsets []int
}

type timelineCachedCard struct {
	lines      []string
	offsets    []int
	event      model.EventSummary
	payload    model.NormalizedData
	width      int
	expanded   bool
	inspection app.UnknownEvidenceInspection
	loading    bool
	err        error
	bytes      int
}

func (m *Model) timelineContent() ([]string, map[model.EventID]timelineCardRange) {
	width := max(20, m.renderWidth()-2)
	if m.timelineState.page.Payloads == nil && m.timelineState.loading {
		return nil, nil
	}
	if m.timelineState.cachedLines != nil &&
		m.timelineState.cachedRevision == m.timelineState.renderRevision &&
		m.timelineState.cachedWidth == width {
		return m.timelineState.cachedLines, m.timelineState.cachedRanges
	}
	if m.timelineState.cards == nil {
		m.timelineState.cards = make(map[model.EventID]timelineCachedCard)
	}
	startIndex, endIndex := m.timelineState.residentStart, m.timelineState.residentEnd
	if endIndex == 0 {
		endIndex = len(m.timelineState.page.Events)
	}
	endIndex = min(endIndex, len(m.timelineState.page.Events))
	startIndex = min(startIndex, endIndex)
	lines := make([]string, 0, (endIndex-startIndex)*4)
	ranges := make(map[model.EventID]timelineCardRange, endIndex-startIndex)
	for _, event := range m.timelineState.page.Events[startIndex:endIndex] {
		payload := m.timelineState.page.Payloads[event.ID]
		expanded := m.timelineState.expanded[event.ID]
		inspection := m.timelineState.inspections[event.ID]
		loading, inspectionErr := m.timelineState.inspectionLoading[event.ID], m.timelineState.inspectionErrors[event.ID]
		cached, ok := m.timelineState.cards[event.ID]
		if !ok || cached.width != width || cached.expanded != expanded || !reflect.DeepEqual(cached.event, event) || !reflect.DeepEqual(cached.payload, payload) || !reflect.DeepEqual(cached.inspection, inspection) || cached.loading != loading || !reflect.DeepEqual(cached.err, inspectionErr) {
			card := timelineCardLines(event, payload, width, false, expanded, inspection, loading, inspectionErr)
			offsets := make([]int, len(card)+1)
			position, size := 0, 40 // include the inter-card blank row
			for row, line := range card {
				offsets[row] = position
				size += len(line) + 40 // card/composite string headers and row offsets
				if row == 0 {
					continue
				}
				for _, r := range line {
					if !unicode.IsSpace(r) {
						position++
					}
				}
			}
			offsets[len(card)] = position
			cached = timelineCachedCard{lines: card, offsets: offsets, event: event, payload: payload, width: width, expanded: expanded, inspection: inspection, loading: loading, err: inspectionErr, bytes: size}
			m.timelineState.cards[event.ID] = cached
		}
		start := len(lines)
		lines = append(lines, cached.lines...)
		if event.ID == m.timelineState.selected && len(cached.lines) > 0 {
			lines[start] = ">" + lines[start][1:]
		}
		ranges[event.ID] = timelineCardRange{start: start, end: len(lines) + 1, offsets: cached.offsets}
		lines = append(lines, "")
	}
	// Newly formatted cards can exceed the rendered-byte budget independently of
	// payload size (for example, many short lines). Evict and rebuild the layout.
	if m.boundTimeline() {
		return m.timelineContent()
	}
	if m.timelineState.loading && len(m.timelineState.page.Events) > 0 {
		lines = append(lines, "Loading more events…")
	} else if m.timelineState.page.NextCursor == "" && endIndex == len(m.timelineState.page.Events) && len(m.timelineState.page.Events) > 0 {
		lines = append(lines, fmt.Sprintf("End of timeline · %d events loaded", len(m.timelineState.page.Events)))
	} else if len(m.timelineState.page.Events) > 0 {
		lines = append(lines, fmt.Sprintf("%d events loaded · more available", len(m.timelineState.page.Events)))
	}
	m.timelineState.cachedRevision = m.timelineState.renderRevision
	m.timelineState.cachedWidth = width
	m.timelineState.cachedLines = lines
	m.timelineState.cachedRanges = ranges
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

	title := strings.TrimPrefix(timelineActivityTitle(payload, event.Summary), "▸ ")
	disclosure := "+"
	if expanded {
		disclosure = "-"
	}
	lines := []string{truncateCell(fmt.Sprintf("%s #%d %s %s", marker, event.Sequence, disclosure, title), width)}
	if !expanded {
		return lines
	}
	addField := func(label, value string) {
		lines = append(lines, wrappedField(label, value, width)...)
	}

	switch value := payload.(type) {
	case model.SummaryData:
		addField("Summary", value.Text)
	case model.ToolCallData:
		addField("Tool", value.ToolName)
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
		addField("Error", optionalBool(value.IsError))
		output := value.Output
		if output == "" {
			output = "No output recorded."
		}
		addField("Output", output)
	case model.CommandData:
		addField("Command", value.Command)
		addField("Working directory", reportedString(value.WorkingDirectory))
		addField("Exit status", optionalInt(value.ExitCode))
		output := value.Output
		if output == "" {
			output = "No output recorded."
		}
		addField("Output", output)
	case model.PatchData:
		addField("Status", reportedString(string(value.Status)))
		addField("Working directory", reportedString(value.WorkingDirectory))
		for _, change := range value.Changes {
			addField("Change", string(change.Operation)+" · "+change.Path)
			if change.PreviousPath != "" {
				addField("Renamed from", change.PreviousPath)
			}
		}
		addField("Paths", strings.Join(value.Paths, ", "))
		addField("Patch", value.Text)
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
		addField("Error", value.Message)
	case model.UnknownData:
		addField("Reason", string(value.Reason))
		addField("Original kind", value.OriginalKind)
		switch {
		case inspectionLoading:
			lines = append(lines, "  Loading bounded, redacted retained evidence…")
		case inspectionErr != nil:
			lines = append(lines, "  Could not inspect retained evidence: "+safeInline(inspectionErr.Error()))
		case inspection.State != "":
			switch inspection.State {
			case app.EvidenceUnavailable:
				lines = append(lines, "  Retained evidence for this event is unavailable.")
			case app.EvidenceNotFound:
				lines = append(lines, "  This event's retained evidence could not be located.")
			default:
				addField("Retained evidence", inspection.Text)
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
	switch message.Role {
	case model.MessageRoleUser:
		label = "You"
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
	header := marker + " " + label
	text := message.Text
	if text == "" {
		text = "—"
	}
	lines := []string{header}
	lines = append(lines, markdownLines(text, width)...)
	return lines
}

func timelineActivityTitle(payload model.NormalizedData, fallback string) string {
	var title string
	switch value := payload.(type) {
	case model.SummaryData:
		title = app.SummaryCategoryTitle(value.Category)
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
		if value.IsError == nil {
			title += " · RESULT UNAVAILABLE"
		} else if *value.IsError {
			title += " · RECORDED FAILURE"
		} else {
			title += " · recorded tool success"
		}
	case model.CommandData:
		title = "Command · RESULT UNAVAILABLE"
		if value.ExitCode != nil {
			if *value.ExitCode != 0 {
				title = fmt.Sprintf("Command · RECORDED FAILURE (exit %d)", *value.ExitCode)
			} else {
				title = "Command · recorded exit 0"
			}
		}
		if value.Command != "" {
			title += " · " + value.Command
		}
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
		title = "Recorded error"
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
