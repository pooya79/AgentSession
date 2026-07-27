package opencode

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pooya79/AgentSession/internal/model"
)

func (p *prepared) normalize(record logicalRecord, sessionID model.SessionID, sequence int64) ([]model.Event, []model.Diagnostic, error) {
	if record.table == "session" {
		return nil, nil, nil
	}
	var data map[string]json.RawMessage
	if len(record.data) == 0 || json.Unmarshal(record.data, &data) != nil || data == nil {
		return nil, []model.Diagnostic{invalidDiagnostic(
			"opencode.record.data.malformed",
			"The OpenCode row data is malformed JSON and was retained without canonical interpretation.",
		)}, nil
	}
	switch record.table {
	case "message":
		return normalizeLegacyMessage(record, data, sessionID, sequence)
	case "part":
		return normalizeLegacyPart(record, data, sessionID, sequence)
	case "session_message":
		return normalizeSessionMessage(record, data, sessionID, sequence)
	case "event":
		return normalizeDurableEvent(record, data, sessionID, sequence)
	default:
		return nil, nil, nil
	}
}

func normalizeLegacyMessage(record logicalRecord, data map[string]json.RawMessage, sessionID model.SessionID, sequence int64) ([]model.Event, []model.Diagnostic, error) {
	role, rolePresent, roleValid := optionalString(data, "role")
	compat, typePresent, typeValid := optionalString(data, "type")
	if rolePresent && !roleValid {
		return nil, []model.Diagnostic{invalidDiagnostic(
			"opencode.message.role.missing", "The OpenCode message row has no usable role discriminator.",
		)}, nil
	}
	if !rolePresent {
		if typePresent && typeValid {
			role = compat
		} else {
			return nil, []model.Diagnostic{missingDiagnostic(
				"opencode.message.role.missing", "The OpenCode message row has no role discriminator.",
			)}, nil
		}
	}
	if typePresent && (!typeValid || compat != role) {
		return nil, []model.Diagnostic{invalidDiagnostic(
			"opencode.message.discriminant.conflict", "The OpenCode message role and compatibility discriminator conflict.",
		)}, nil
	}
	switch role {
	case "user", "system", "developer":
		return nil, nil, nil
	case "assistant":
		return normalizeAssistantEvidence(record, data, sessionID, sequence, "message")
	default:
		event, err := unknownEvent(record, sessionID, sequence, 0, "unknown", model.UnknownUnsupportedNestedVariant, "message:"+role)
		return []model.Event{event}, nil, err
	}
}

func normalizeLegacyPart(record logicalRecord, data map[string]json.RawMessage, sessionID model.SessionID, sequence int64) ([]model.Event, []model.Diagnostic, error) {
	typeName, present, valid := optionalString(data, "type")
	if !present || typeName == "" {
		return nil, []model.Diagnostic{missingDiagnostic(
			"opencode.part.type.missing", "The OpenCode part row has no type discriminator.",
		)}, nil
	}
	if !valid {
		return nil, []model.Diagnostic{invalidDiagnostic(
			"opencode.part.type.missing", "The OpenCode part row has no usable type discriminator.",
		)}, nil
	}
	switch typeName {
	case "text":
		text, exists, ok := optionalString(data, "text")
		if !exists || !ok {
			return nil, []model.Diagnostic{invalidDiagnostic(
				"opencode.part.text.invalid", "The OpenCode text part has invalid required text.",
			)}, nil
		}
		role := messageRole(record.messageData)
		event, err := newEvent(record, sessionID, sequence, 0, "text", model.EventKindMessage, roleSummary(role), text, model.MessageData{Role: role, Text: text})
		var diagnostics []model.Diagnostic
		if text == "" {
			diagnostics = append(diagnostics, model.Diagnostic{
				Code: "opencode.part.text.missing", Severity: model.SeverityWarning,
				Message: "The OpenCode text part records empty text.",
			})
		}
		return []model.Event{event}, diagnostics, err
	case "tool":
		return normalizeTool(record, data, sessionID, sequence, "opencode.part.tool.invalid", "part:tool:state:", false)
	case "patch":
		return normalizePatch(record, data, sessionID, sequence)
	case "file":
		return normalizeFile(record, data, sessionID, sequence)
	case "step-finish":
		return normalizeSettlement(record, data, sessionID, sequence, "opencode.part.step-finish.invalid", "part:step-finish:unmapped")
	case "retry":
		return normalizeRetry(record, data, sessionID, sequence, "opencode.part.retry.invalid", "part:retry:unmapped")
	default:
		event, err := unknownEvent(record, sessionID, sequence, 0, "unknown", model.UnknownUnsupportedNestedVariant, "part:"+typeName)
		return []model.Event{event}, nil, err
	}
}

func normalizeTool(record logicalRecord, data map[string]json.RawMessage, sessionID model.SessionID, sequence int64, diagnosticCode, residualPrefix string, allowAliases bool) ([]model.Event, []model.Diagnostic, error) {
	callID, callPresent, callOK := optionalString(data, "callID")
	toolName, toolPresent, toolOK := optionalString(data, "tool")
	if allowAliases {
		if !callPresent {
			callID, callPresent, callOK = optionalString(data, "id")
		}
		if !toolPresent {
			toolName, toolPresent, toolOK = optionalString(data, "name")
		}
	}
	state, stateOK := objectValue(data["state"])
	status, statusPresent, statusOK := optionalString(state, "status")
	var diagnostics []model.Diagnostic
	if !callPresent || !callOK || callID == "" || !toolPresent || !toolOK || toolName == "" || !stateOK || !statusPresent || !statusOK || status == "" {
		diagnostics = append(diagnostics, invalidDiagnostic(diagnosticCode, "The OpenCode tool record has invalid required fields."))
	}
	if callID == "" || toolName == "" || !stateOK {
		return nil, diagnostics, nil
	}
	input, inputOK := compactValue(state["input"])
	if !inputOK && len(state["input"]) > 0 {
		return nil, append(diagnostics, invalidDiagnostic(diagnosticCode, "The OpenCode tool input is malformed.")), nil
	}
	call, err := newEvent(record, sessionID, sequence, 0, "call", model.EventKindToolCall, "OpenCode tool call", input, model.ToolCallData{CallID: callID, ToolName: toolName, Input: input})
	if err != nil {
		return nil, nil, err
	}
	events := []model.Event{call}
	switch status {
	case "pending", "running":
	case "completed", "error":
		isError := status == "error"
		outputRaw := state["output"]
		if len(outputRaw) == 0 {
			outputRaw = state["content"]
		}
		if len(outputRaw) == 0 {
			outputRaw = state["result"]
		}
		if isError && len(outputRaw) == 0 {
			outputRaw = state["error"]
		}
		output, _ := compactToolOutput(outputRaw)
		result, err := newEvent(record, sessionID, sequence+1, 1, "result", model.EventKindToolResult, "OpenCode tool result", output, model.ToolResultData{
			CallID: callID, ToolName: toolName, Output: output, IsError: &isError,
		})
		if err != nil {
			return nil, nil, err
		}
		events = append(events, result)
	default:
		if status != "" {
			residual, err := unknownEvent(record, sessionID, sequence+int64(len(events)), uint64(len(events)), "state-unknown", model.UnknownUnsupportedNestedVariant, residualPrefix+status)
			if err != nil {
				return nil, nil, err
			}
			events = append(events, residual)
		}
	}
	return events, diagnostics, nil
}

func normalizePatch(record logicalRecord, data map[string]json.RawMessage, sessionID model.SessionID, sequence int64) ([]model.Event, []model.Diagnostic, error) {
	var diagnostics []model.Diagnostic
	var paths []string
	filesRaw, filesPresent := data["files"]
	if !filesPresent {
		diagnostics = append(diagnostics, invalidDiagnostic("opencode.part.patch.invalid", "The OpenCode patch file list is missing."))
	} else {
		var values []json.RawMessage
		if json.Unmarshal(filesRaw, &values) != nil {
			diagnostics = append(diagnostics, invalidDiagnostic("opencode.part.patch.invalid", "The OpenCode patch file list is malformed."))
		} else {
			for _, value := range values {
				var path string
				if json.Unmarshal(value, &path) == nil {
					paths = append(paths, path)
				} else {
					diagnostics = append(diagnostics, invalidDiagnostic("opencode.part.patch.invalid", "The OpenCode patch file list contains a malformed path."))
				}
			}
		}
	}
	text, textPresent, textOK := optionalString(data, "text")
	if textPresent && !textOK {
		diagnostics = append(diagnostics, invalidDiagnostic("opencode.part.patch.invalid", "The OpenCode patch text is malformed."))
	}
	if !textPresent || !textOK {
		if compact, ok := compactValue(filesRaw); ok {
			text = compact
		}
	}
	event, err := newEvent(record, sessionID, sequence, 0, "patch", model.EventKindPatch, "OpenCode patch", text, model.PatchData{Text: text, Paths: paths})
	return []model.Event{event}, diagnostics, err
}

func normalizeFile(record logicalRecord, data map[string]json.RawMessage, sessionID model.SessionID, sequence int64) ([]model.Event, []model.Diagnostic, error) {
	source, sourceOK := objectValue(data["source"])
	sourceType, _, _ := optionalString(source, "type")
	path, pathPresent, pathOK := optionalString(source, "path")
	if sourceOK && (sourceType == "file" || sourceType == "symbol") && pathPresent && pathOK && path != "" {
		event, err := newEvent(record, sessionID, sequence, 0, "unknown", model.EventKindFileRead, "OpenCode file read", path, model.FileReadData{Path: path})
		return []model.Event{event}, nil, err
	}
	var diagnostics []model.Diagnostic
	if sourceOK && (sourceType == "file" || sourceType == "symbol") && (!pathPresent || !pathOK || path == "") {
		diagnostics = append(diagnostics, invalidDiagnostic("opencode.part.file.invalid", "The OpenCode file source path is malformed."))
	}
	label := sourceType
	if label == "" {
		label = "attachment"
	}
	event, err := unknownEvent(record, sessionID, sequence, 0, "unknown", model.UnknownUnsupportedNestedVariant, "part:file:"+label)
	return []model.Event{event}, diagnostics, err
}

func normalizeSettlement(record logicalRecord, data map[string]json.RawMessage, sessionID model.SessionID, sequence int64, diagnosticCode, residualKind string) ([]model.Event, []model.Diagnostic, error) {
	usage, hasUsage, diagnostics := parseUsage(data["tokens"], diagnosticCode)
	var events []model.Event
	if hasUsage {
		event, err := newEvent(record, sessionID, sequence, 0, "usage", model.EventKindUsage, "OpenCode token usage", "", usage)
		if err != nil {
			return nil, nil, err
		}
		events = append(events, event)
	}
	residual, err := unknownEvent(record, sessionID, sequence+int64(len(events)), uint64(len(events)), "unmapped", model.UnknownUnsupportedNestedVariant, residualKind)
	if err != nil {
		return nil, nil, err
	}
	return append(events, residual), diagnostics, nil
}

func normalizeRetry(record logicalRecord, data map[string]json.RawMessage, sessionID model.SessionID, sequence int64, diagnosticCode, residualKind string) ([]model.Event, []model.Diagnostic, error) {
	errorObject, ok := objectValue(data["error"])
	message, present, valid := optionalString(errorObject, "message")
	if !ok || !present || !valid {
		return nil, []model.Diagnostic{invalidDiagnostic(diagnosticCode, "The OpenCode retry error message is malformed.")}, nil
	}
	errEvent, err := newEvent(record, sessionID, sequence, 0, "error", model.EventKindError, "OpenCode retry error", message, model.ErrorData{Code: "opencode_retry", Message: message})
	if err != nil {
		return nil, nil, err
	}
	residual, err := unknownEvent(record, sessionID, sequence+1, 1, "unmapped", model.UnknownUnsupportedNestedVariant, residualKind)
	if err != nil {
		return nil, nil, err
	}
	return []model.Event{errEvent, residual}, nil, nil
}

func normalizeSessionMessage(record logicalRecord, data map[string]json.RawMessage, sessionID model.SessionID, sequence int64) ([]model.Event, []model.Diagnostic, error) {
	typeName := record.rowType
	if record.rowTypePresent && !record.rowTypeValid {
		return nil, []model.Diagnostic{invalidDiagnostic("opencode.session_message.type.missing", "The OpenCode session message row has an invalid type discriminator.")}, nil
	}
	if typeName == "" {
		return nil, []model.Diagnostic{missingDiagnostic("opencode.session_message.type.missing", "The OpenCode session message row has no type discriminator.")}, nil
	}
	var diagnostics []model.Diagnostic
	if nested, present, valid := optionalString(data, "type"); present && (!valid || nested != typeName) {
		diagnostics = append(diagnostics, invalidDiagnostic("opencode.session_message.type.conflict", "The OpenCode session message row and data type discriminators conflict."))
	}
	switch typeName {
	case "user", "synthetic", "system":
		text, present, valid := optionalString(data, "text")
		if !present || !valid {
			return nil, append(diagnostics, invalidDiagnostic("opencode.session_message."+typeName+".invalid", "The OpenCode session message has invalid required text.")), nil
		}
		role := model.MessageRoleUser
		if typeName == "synthetic" {
			role = model.MessageRoleOther
		} else if typeName == "system" {
			role = model.MessageRoleSystem
		}
		event, err := newEvent(record, sessionID, sequence, 0, "message", model.EventKindMessage, roleSummary(role), text, model.MessageData{Role: role, Text: text})
		return []model.Event{event}, diagnostics, err
	case "shell":
		callID, ip, iv := optionalString(data, "callID")
		command, cp, cv := optionalString(data, "command")
		output, op, ov := optionalString(data, "output")
		if !ip || !iv || callID == "" || !cp || !cv || !op || !ov {
			return nil, append(diagnostics, invalidDiagnostic("opencode.session_message.shell.invalid", "The OpenCode shell message has invalid required fields.")), nil
		}
		searchable := command
		if output != "" {
			searchable += "\n" + output
		}
		event, err := newEvent(record, sessionID, sequence, 0, "command", model.EventKindCommand, "OpenCode shell command", searchable, model.CommandData{Command: command, Output: output})
		return []model.Event{event}, diagnostics, err
	case "assistant":
		events, nestedDiagnostics, err := normalizeSessionAssistant(record, data, sessionID, sequence)
		return events, append(diagnostics, nestedDiagnostics...), err
	case "compaction":
		summary, present, valid := optionalString(data, "summary")
		if !present || !valid {
			return nil, append(diagnostics, invalidDiagnostic("opencode.session_message.compaction.invalid", "The OpenCode compaction message has invalid required summary text.")), nil
		}
		event, err := newEvent(record, sessionID, sequence, 0, "summary", model.EventKindSummary, "OpenCode compaction summary", summary, model.SummaryData{Text: summary})
		return []model.Event{event}, diagnostics, err
	default:
		event, err := unknownEvent(record, sessionID, sequence, 0, "unknown", model.UnknownUnsupportedRecordKind, "session_message:"+typeName)
		return []model.Event{event}, diagnostics, err
	}
}

func normalizeSessionAssistant(record logicalRecord, data map[string]json.RawMessage, sessionID model.SessionID, sequence int64) ([]model.Event, []model.Diagnostic, error) {
	var entries []json.RawMessage
	var diagnostics []model.Diagnostic
	if raw, present := data["content"]; !present || json.Unmarshal(raw, &entries) != nil {
		diagnostics = append(diagnostics, invalidDiagnostic("opencode.session_message.assistant.invalid", "The OpenCode assistant content array is malformed."))
	}
	var events []model.Event
	for index, raw := range entries {
		var entry map[string]json.RawMessage
		if json.Unmarshal(raw, &entry) != nil || entry == nil {
			diagnostics = append(diagnostics, invalidDiagnostic("opencode.session_message.assistant.invalid", "An OpenCode assistant content entry is malformed."))
			continue
		}
		typeName, present, valid := optionalString(entry, "type")
		if !present || typeName == "" {
			diagnostics = append(diagnostics, missingDiagnostic("opencode.session_message.assistant.invalid", "An OpenCode assistant content entry has no discriminator."))
			continue
		}
		if !valid {
			diagnostics = append(diagnostics, invalidDiagnostic("opencode.session_message.assistant.invalid", "An OpenCode assistant content entry has an invalid discriminator."))
			continue
		}
		switch typeName {
		case "text":
			text, present, valid := optionalString(entry, "text")
			if !present || !valid {
				diagnostics = append(diagnostics, invalidDiagnostic("opencode.session_message.assistant.invalid", "An OpenCode assistant text entry is malformed."))
				continue
			}
			event, err := newEvent(record, sessionID, sequence+int64(len(events)), uint64(index), fmt.Sprintf("content-%d", index), model.EventKindMessage, "Assistant message", text, model.MessageData{Role: model.MessageRoleAssistant, Text: text})
			if err != nil {
				return nil, nil, err
			}
			events = append(events, event)
		case "tool":
			toolEvents, toolDiagnostics, err := normalizeTool(record, entry, sessionID, sequence+int64(len(events)), "opencode.session_message.assistant.invalid", "session_message:assistant:tool:state:", true)
			if err != nil {
				return nil, nil, err
			}
			// Ensure identities remain tied to source content position.
			for i := range toolEvents {
				toolEvents[i], err = reidentifyEvent(toolEvents[i], record, sessionID, uint64(index*4+i), fmt.Sprintf("content-%d-%d", index, i))
				if err != nil {
					return nil, nil, err
				}
			}
			events = append(events, toolEvents...)
			diagnostics = append(diagnostics, toolDiagnostics...)
		default:
			event, err := unknownEvent(record, sessionID, sequence+int64(len(events)), uint64(index), fmt.Sprintf("content-%d", index), model.UnknownUnsupportedNestedVariant, "session_message:assistant:"+typeName)
			if err != nil {
				return nil, nil, err
			}
			events = append(events, event)
		}
	}
	usage, hasUsage, usageDiagnostics := parseUsage(data["tokens"], "opencode.session_message.assistant.invalid")
	diagnostics = append(diagnostics, usageDiagnostics...)
	if hasUsage {
		event, err := newEvent(record, sessionID, sequence+int64(len(events)), uint64(len(entries)*4+1), "usage", model.EventKindUsage, "OpenCode token usage", "", usage)
		if err != nil {
			return nil, nil, err
		}
		events = append(events, event)
	}
	if raw, present := data["error"]; present && string(raw) != "null" {
		errorData, ok := parseError(raw, "assistant_error")
		if !ok {
			diagnostics = append(diagnostics, invalidDiagnostic("opencode.session_message.assistant.invalid", "The OpenCode assistant error is malformed."))
		} else {
			event, err := newEvent(record, sessionID, sequence+int64(len(events)), uint64(len(entries)*4+2), "error", model.EventKindError, "OpenCode assistant error", errorData.Message, errorData)
			if err != nil {
				return nil, nil, err
			}
			events = append(events, event)
		}
	}
	return events, diagnostics, nil
}

func normalizeAssistantEvidence(record logicalRecord, data map[string]json.RawMessage, sessionID model.SessionID, sequence int64, diagnosticPrefix string) ([]model.Event, []model.Diagnostic, error) {
	usage, hasUsage, diagnostics := parseUsage(data["tokens"], "opencode."+diagnosticPrefix+".tokens.invalid")
	var events []model.Event
	if hasUsage {
		event, err := newEvent(record, sessionID, sequence, 0, "usage", model.EventKindUsage, "OpenCode token usage", "", usage)
		if err != nil {
			return nil, nil, err
		}
		events = append(events, event)
	}
	if raw, present := data["error"]; present && string(raw) != "null" {
		errorData, ok := parseError(raw, "assistant_error")
		if !ok {
			diagnostics = append(diagnostics, invalidDiagnostic("opencode."+diagnosticPrefix+".error.invalid", "The OpenCode assistant error is malformed."))
		} else {
			event, err := newEvent(record, sessionID, sequence+int64(len(events)), uint64(len(events)), "error", model.EventKindError, "OpenCode assistant error", errorData.Message, errorData)
			if err != nil {
				return nil, nil, err
			}
			events = append(events, event)
		}
	}
	return events, diagnostics, nil
}

func normalizeDurableEvent(record logicalRecord, data map[string]json.RawMessage, sessionID model.SessionID, sequence int64) ([]model.Event, []model.Diagnostic, error) {
	typeName := record.rowType
	if record.rowTypePresent && !record.rowTypeValid {
		return nil, []model.Diagnostic{invalidDiagnostic("opencode.event.type.missing", "The OpenCode durable event row has an invalid type discriminator.")}, nil
	}
	if typeName == "" {
		return nil, []model.Diagnostic{missingDiagnostic("opencode.event.type.missing", "The OpenCode durable event row has no type discriminator.")}, nil
	}
	var diagnostics []model.Diagnostic
	if nested, present, valid := optionalString(data, "type"); present && (!valid || nested != typeName) {
		diagnostics = append(diagnostics, invalidDiagnostic("opencode.event.type.conflict", "The OpenCode durable event row and data type discriminators conflict."))
	}
	switch typeName {
	case "session.next.prompted", "session.next.prompt.admitted", "session.next.synthetic", "session.next.context.updated", "session.next.text.ended":
		text, present, valid := firstString(data, "text", "prompt", "content")
		if prompt, ok := objectValue(data["prompt"]); ok {
			text, present, valid = firstString(data, "text", "content")
			if nested, nestedPresent, nestedValid := optionalString(prompt, "text"); nestedPresent && nestedValid {
				text, present, valid = nested, true, true
			}
		}
		if !present || !valid {
			return nil, append(diagnostics, durableInvalid(typeName, "The OpenCode durable message event has invalid required text.")), nil
		}
		role := model.MessageRoleUser
		if typeName == "session.next.synthetic" {
			role = model.MessageRoleOther
		} else if typeName == "session.next.context.updated" {
			role = model.MessageRoleSystem
		} else if typeName == "session.next.text.ended" {
			role = model.MessageRoleAssistant
		}
		event, err := newEvent(record, sessionID, sequence, 0, "message", model.EventKindMessage, roleSummary(role), text, model.MessageData{Role: role, Text: text})
		return []model.Event{event}, diagnostics, err
	case "session.next.shell.started":
		callID, cp, cv := firstString(data, "callID", "callId", "id")
		command, mp, mv := firstString(data, "command", "input")
		if !cp || !cv || callID == "" || !mp || !mv {
			return nil, append(diagnostics, durableInvalid(typeName, "The OpenCode shell-start event has invalid required fields.")), nil
		}
		event, err := newEvent(record, sessionID, sequence, 0, "call", model.EventKindToolCall, "OpenCode shell call", command, model.ToolCallData{CallID: callID, ToolName: "shell", Input: command})
		return []model.Event{event}, diagnostics, err
	case "session.next.shell.ended":
		callID, cp, cv := firstString(data, "callID", "callId", "id")
		output, op, ov := firstString(data, "output")
		if !cp || !cv || callID == "" || !op || !ov {
			return nil, append(diagnostics, durableInvalid(typeName, "The OpenCode shell-end event has invalid required fields.")), nil
		}
		event, err := newEvent(record, sessionID, sequence, 0, "result", model.EventKindToolResult, "OpenCode shell result", output, model.ToolResultData{CallID: callID, ToolName: "shell", Output: output})
		return []model.Event{event}, diagnostics, err
	case "session.next.tool.called":
		return normalizeDurableToolCall(record, data, sessionID, sequence, diagnostics)
	case "session.next.tool.success", "session.next.tool.failed":
		return normalizeDurableToolResult(record, data, sessionID, sequence, diagnostics, typeName == "session.next.tool.failed")
	case "session.next.step.ended":
		events, extra, err := normalizeSettlement(record, data, sessionID, sequence, durableCode(typeName), "event:"+typeName+":unmapped")
		return events, append(diagnostics, extra...), err
	case "session.next.step.failed":
		errorRaw := data["error"]
		if len(errorRaw) == 0 {
			errorRaw = data["message"]
		}
		errorData, ok := parseError(errorRaw, "step_failed")
		if !ok {
			return nil, append(diagnostics, durableInvalid(typeName, "The OpenCode step-failed event has an invalid error.")), nil
		}
		event, err := newEvent(record, sessionID, sequence, 0, "error", model.EventKindError, "OpenCode step error", errorData.Message, errorData)
		return []model.Event{event}, diagnostics, err
	case "session.next.retried":
		events, extra, err := normalizeRetry(record, data, sessionID, sequence, durableCode(typeName), "event:"+typeName+":unmapped")
		return events, append(diagnostics, extra...), err
	case "session.next.compaction.ended":
		summary, present, valid := firstString(data, "summary", "text")
		if !present || !valid {
			return nil, append(diagnostics, durableInvalid(typeName, "The OpenCode compaction event has invalid required summary text.")), nil
		}
		event, err := newEvent(record, sessionID, sequence, 0, "summary", model.EventKindSummary, "OpenCode compaction summary", summary, model.SummaryData{Text: summary})
		return []model.Event{event}, diagnostics, err
	default:
		event, err := unknownEvent(record, sessionID, sequence, 0, "unknown", model.UnknownUnsupportedRecordKind, "event:"+typeName)
		return []model.Event{event}, diagnostics, err
	}
}

func normalizeDurableToolCall(record logicalRecord, data map[string]json.RawMessage, sessionID model.SessionID, sequence int64, diagnostics []model.Diagnostic) ([]model.Event, []model.Diagnostic, error) {
	callID, cp, cv := firstString(data, "callID", "callId", "id")
	toolName, tp, tv := firstString(data, "tool", "name")
	input, inputOK := compactValue(data["input"])
	if !cp || !cv || callID == "" || !tp || !tv || toolName == "" || !inputOK {
		return nil, append(diagnostics, durableInvalid(record.rowType, "The OpenCode durable tool-call event has invalid required fields.")), nil
	}
	event, err := newEvent(record, sessionID, sequence, 0, "call", model.EventKindToolCall, "OpenCode tool call", input, model.ToolCallData{CallID: callID, ToolName: toolName, Input: input})
	return []model.Event{event}, diagnostics, err
}

func normalizeDurableToolResult(record logicalRecord, data map[string]json.RawMessage, sessionID model.SessionID, sequence int64, diagnostics []model.Diagnostic, failed bool) ([]model.Event, []model.Diagnostic, error) {
	callID, cp, cv := firstString(data, "callID", "callId", "id")
	toolName, tp, tv := firstString(data, "tool", "name")
	outputRaw := data["output"]
	if len(outputRaw) == 0 {
		outputRaw = data["content"]
	}
	output, outputOK := compactToolOutput(outputRaw)
	if failed && len(outputRaw) == 0 {
		if errorData, ok := parseError(data["error"], "tool_failed"); ok {
			output, outputOK = errorData.Message, true
		}
	}
	if !cp || !cv || callID == "" || !tp || !tv || toolName == "" || !outputOK {
		return nil, append(diagnostics, durableInvalid(record.rowType, "The OpenCode durable tool-result event has invalid required fields.")), nil
	}
	event, err := newEvent(record, sessionID, sequence, 0, "result", model.EventKindToolResult, "OpenCode tool result", output, model.ToolResultData{CallID: callID, ToolName: toolName, Output: output, IsError: &failed})
	return []model.Event{event}, diagnostics, err
}

func compactToolOutput(raw json.RawMessage) (string, bool) {
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return compactToolBlock(raw)
	}
	var output strings.Builder
	for _, block := range blocks {
		var object map[string]json.RawMessage
		if json.Unmarshal(block, &object) == nil {
			if kind, _, _ := optionalString(object, "type"); kind == "text" {
				text, _, ok := optionalString(object, "text")
				if ok {
					output.WriteString(text)
					continue
				}
			}
		}
		compact, ok := compactToolBlock(block)
		if !ok {
			return "", false
		}
		output.WriteString(compact)
	}
	return output.String(), true
}

func compactToolBlock(raw json.RawMessage) (string, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return compactValue(raw)
	}
	delete(object, "provider")
	delete(object, "providerMetadata")
	delete(object, "resultMetadata")
	encoded, err := json.Marshal(object)
	return string(encoded), err == nil
}

func parseUsage(raw json.RawMessage, diagnosticCode string) (model.UsageData, bool, []model.Diagnostic) {
	var usage model.UsageData
	if len(raw) == 0 || string(raw) == "null" {
		return usage, false, nil
	}
	tokens, ok := objectValue(raw)
	if !ok {
		return usage, false, []model.Diagnostic{invalidDiagnostic(diagnosticCode, "The OpenCode token counters are malformed.")}
	}
	var diagnostics []model.Diagnostic
	usage.InputTokens = tokenCounter(tokens, "input", diagnosticCode, &diagnostics)
	usage.OutputTokens = tokenCounter(tokens, "output", diagnosticCode, &diagnostics)
	cache, cacheOK := objectValue(tokens["cache"])
	if len(tokens["cache"]) > 0 && !cacheOK {
		diagnostics = append(diagnostics, invalidDiagnostic(diagnosticCode, "The OpenCode cache token counters are malformed."))
	} else {
		usage.CacheReadTokens = tokenCounter(cache, "read", diagnosticCode, &diagnostics)
		usage.CacheWriteTokens = tokenCounter(cache, "write", diagnosticCode, &diagnostics)
	}
	hasUsage := usage.InputTokens != nil || usage.OutputTokens != nil || usage.CacheReadTokens != nil || usage.CacheWriteTokens != nil
	return usage, hasUsage, diagnostics
}

func tokenCounter(values map[string]json.RawMessage, key, diagnosticCode string, diagnostics *[]model.Diagnostic) *int64 {
	raw, present := values[key]
	if !present {
		return nil
	}
	var value int64
	if json.Unmarshal(raw, &value) != nil || value < 0 {
		*diagnostics = append(*diagnostics, invalidDiagnostic(diagnosticCode, "An OpenCode supported token counter is malformed."))
		return nil
	}
	return &value
}

func parseError(raw json.RawMessage, fallbackCode string) (model.ErrorData, bool) {
	var message string
	if json.Unmarshal(raw, &message) == nil {
		return model.ErrorData{Code: fallbackCode, Message: message}, true
	}
	value, ok := objectValue(raw)
	if !ok {
		return model.ErrorData{}, false
	}
	message, present, valid := optionalString(value, "message")
	if !present || !valid {
		return model.ErrorData{}, false
	}
	code := fallbackCode
	if candidate, present, valid := optionalString(value, "code"); present && valid && candidate != "" {
		code = candidate
	} else if candidate, present, valid := optionalString(value, "name"); present && valid && candidate != "" {
		code = candidate
	}
	return model.ErrorData{Code: code, Message: message}, true
}

func newEvent(record logicalRecord, sessionID model.SessionID, sequence int64, ordinal uint64, suffix string, kind model.EventKind, summary, searchable string, data model.NormalizedData) (model.Event, error) {
	tableName := record.table
	if tableName == "session_message" {
		tableName = "session-message"
	}
	nativeID := "opencode:" + tableName + ":" + record.nativeID + ":" + suffix
	id, err := model.NewEventID(model.EventIDInput{
		Native:   &model.NativeEventIdentity{Scope: model.NativeEventIDSession, SessionID: string(sessionID), EventID: nativeID},
		SourceID: "fallback", RecordSequence: &sequence, RecordHash: "fallback", EventOrdinal: ordinal,
	})
	if err != nil {
		return model.Event{}, err
	}
	return model.Event{ID: id, SessionID: sessionID, Sequence: sequence, Timestamp: record.timestamp, Kind: kind, Summary: summary, SearchableText: searchable, Data: data}, nil
}

func reidentifyEvent(event model.Event, record logicalRecord, sessionID model.SessionID, ordinal uint64, suffix string) (model.Event, error) {
	updated, err := newEvent(record, sessionID, event.Sequence, ordinal, suffix, event.Kind, event.Summary, event.SearchableText, event.Data)
	if err != nil {
		return model.Event{}, err
	}
	return updated, nil
}

func unknownEvent(record logicalRecord, sessionID model.SessionID, sequence int64, ordinal uint64, suffix string, reason model.UnknownReason, originalKind string) (model.Event, error) {
	return newEvent(record, sessionID, sequence, ordinal, suffix, model.EventKindUnknown, "Unsupported OpenCode evidence", "", model.UnknownData{
		Reason: reason, OriginalKind: model.BoundOriginalKind(originalKind),
	})
}

func messageRole(raw []byte) model.MessageRole {
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil {
		return model.MessageRoleUnknown
	}
	role, present, valid := optionalString(data, "role")
	if !present {
		role, _, valid = optionalString(data, "type")
	}
	if !valid {
		return model.MessageRoleUnknown
	}
	switch role {
	case "user":
		return model.MessageRoleUser
	case "assistant":
		return model.MessageRoleAssistant
	case "system", "developer":
		return model.MessageRoleSystem
	case "":
		return model.MessageRoleUnknown
	default:
		return model.MessageRoleOther
	}
}

func roleSummary(role model.MessageRole) string {
	value := string(role)
	if value == "" {
		return "Unknown message"
	}
	return strings.ToUpper(value[:1]) + value[1:] + " message"
}

func objectValue(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var value map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value == nil {
		return nil, false
	}
	return value, true
}

func optionalString(values map[string]json.RawMessage, key string) (string, bool, bool) {
	raw, present := values[key]
	if !present {
		return "", false, false
	}
	var value string
	return value, true, json.Unmarshal(raw, &value) == nil
}

func firstString(values map[string]json.RawMessage, keys ...string) (string, bool, bool) {
	for _, key := range keys {
		if value, present, valid := optionalString(values, key); present {
			return value, true, valid
		}
	}
	return "", false, false
}

func compactValue(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", true
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, true
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err == nil
}

func rawScalar(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	return value
}

func missingDiagnostic(code, message string) model.Diagnostic {
	return model.Diagnostic{Code: code, Severity: model.SeverityWarning, Message: message, InterpretationReason: model.InterpretationMissingDiscriminant}
}

func invalidDiagnostic(code, message string) model.Diagnostic {
	return model.Diagnostic{Code: code, Severity: model.SeverityWarning, Message: message, InterpretationReason: model.InterpretationStructurallyInvalidKnownRecord}
}

func durableCode(typeName string) string {
	return "opencode.event." + typeName + ".invalid"
}

func durableInvalid(typeName, message string) model.Diagnostic {
	return invalidDiagnostic(durableCode(typeName), message)
}
