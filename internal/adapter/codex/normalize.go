package codex

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/pooya79/AgentSession/internal/model"
)

type eventDraft struct {
	kind       model.EventKind
	summary    string
	searchable string
	data       model.NormalizedData
	native     *model.NativeEventIdentity
}

type normalizationResult struct {
	drafts      []eventDraft
	diagnostics []model.Diagnostic
	handled     bool
	nested      string
}

func normalizeRecord(record wireRecord, ordinalHistory bool, sessionID string) normalizationResult {
	switch record.Type {
	case "response_item":
		return normalizeResponseItem(record.Payload, ordinalHistory, sessionID)
	case "event_msg":
		return normalizeEventMessage(record.Payload, ordinalHistory, sessionID)
	case "compacted":
		object, ok := objectValue(record.Payload)
		if !ok {
			return invalidKnown()
		}
		message, ok := stringField(object, "message")
		if !ok {
			return invalidKnown()
		}
		return handled(eventDraft{
			kind: model.EventKindSummary, summary: "Codex context compaction",
			searchable: message, data: model.SummaryData{Text: message},
		})
	case "turn_context", "world_state", "inter_agent_communication", "inter_agent_communication_metadata":
		if _, ok := objectValue(record.Payload); !ok {
			return invalidKnown()
		}
		return normalizationResult{handled: true}
	default:
		return normalizationResult{}
	}
}

func normalizeResponseItem(raw json.RawMessage, ordinalHistory bool, sessionID string) normalizationResult {
	item, ok := objectValue(raw)
	if !ok {
		return invalidKnown()
	}
	typeName, exists := stringField(item, "type")
	if !exists || strings.TrimSpace(typeName) == "" {
		return missingNestedDiscriminant()
	}
	native := responseNative(item, typeName, sessionID)
	switch typeName {
	case "message":
		roleName, roleOK := stringField(item, "role")
		content, contentOK, plaintext := messageContentText(item["content"], false)
		if !roleOK || !contentOK {
			return invalidKnown()
		}
		if !plaintext {
			return normalizationResult{handled: true, nested: typeName}
		}
		if !ordinalHistory {
			return normalizationResult{handled: true, nested: typeName}
		}
		role := messageRole(roleName)
		return handledNested(typeName, eventDraft{
			kind: model.EventKindMessage, summary: messageSummary(role), searchable: content,
			data: model.MessageData{Role: role, Text: content}, native: native,
		})
	case "agent_message":
		author, authorOK := stringField(item, "author")
		recipient, recipientOK := stringField(item, "recipient")
		content, contentOK, plaintext := messageContentText(item["content"], true)
		if !authorOK || strings.TrimSpace(author) == "" || !recipientOK || strings.TrimSpace(recipient) == "" || !contentOK {
			return invalidKnown()
		}
		if !plaintext {
			return normalizationResult{handled: true, nested: typeName}
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindMessage, summary: "Agent message", searchable: content,
			data: model.MessageData{Role: model.MessageRoleOther, Text: content}, native: native,
		})
	case "reasoning":
		summary, summaryOK := textBlocks(item["summary"], "summary_text", "text")
		content, contentOK := optionalTextBlocks(item["content"], "reasoning_text", "text")
		if !summaryOK || !contentOK {
			return invalidKnown()
		}
		text := strings.Join(nonEmpty(summary, content), "\n")
		if text == "" {
			if encrypted, present := item["encrypted_content"]; present && !isNull(encrypted) {
				if _, ok := stringValue(encrypted); !ok {
					return invalidKnown()
				}
			}
			return normalizationResult{handled: true, nested: typeName}
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindSummary, summary: "Codex reasoning summary", searchable: text,
			data: model.SummaryData{Text: text}, native: native,
		})
	case "function_call", "custom_tool_call":
		name, nameOK := stringField(item, "name")
		callID, callOK := stringField(item, "call_id")
		inputKey := "arguments"
		if typeName == "custom_tool_call" {
			inputKey = "input"
		}
		input, inputOK := stringField(item, inputKey)
		if !nameOK || strings.TrimSpace(name) == "" || !callOK || strings.TrimSpace(callID) == "" || !inputOK {
			return invalidKnown()
		}
		if name == "apply_patch" {
			return handledNested(typeName, eventDraft{
				kind: model.EventKindPatch, summary: "Apply patch", searchable: input,
				data: model.PatchData{Text: input}, native: native,
			})
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindToolCall, summary: "Tool call: " + name, searchable: input,
			data: model.ToolCallData{CallID: callID, ToolName: name, Input: input}, native: native,
		})
	case "function_call_output", "custom_tool_call_output":
		callID, callOK := stringField(item, "call_id")
		output, outputOK := contentTextStrict(item["output"])
		if !callOK || strings.TrimSpace(callID) == "" || !outputOK {
			return invalidKnown()
		}
		name, _ := optionalStringField(item, "name")
		isError, errorOK := optionalBoolField(item, "is_error")
		if !errorOK {
			return invalidKnown()
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindToolResult, summary: "Tool result", searchable: output,
			data:   model.ToolResultData{CallID: callID, ToolName: name, Output: output, IsError: isError},
			native: native,
		})
	case "local_shell_call":
		callID, _ := optionalStringField(item, "call_id")
		if callID == "" {
			callID, _ = optionalStringField(item, "id")
		}
		action, actionOK := typedObjectText(item["action"])
		if callID == "" || !actionOK {
			return invalidKnown()
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindToolCall, summary: "Shell command requested", searchable: action,
			data: model.ToolCallData{CallID: callID, ToolName: "shell", Input: action}, native: native,
		})
	case "web_search_call":
		status, statusOK := optionalStringField(item, "status")
		if !statusOK {
			return invalidKnown()
		}
		if raw, exists := item["action"]; !exists || isNull(raw) {
			if status == "completed" {
				return invalidKnown()
			}
			return normalizationResult{handled: true, nested: typeName}
		}
		action, actionOK := typedObjectText(item["action"])
		if !actionOK {
			return invalidKnown()
		}
		callID, _ := optionalStringField(item, "id")
		return handledNested(typeName, eventDraft{
			kind: model.EventKindToolCall, summary: "Web search", searchable: action,
			data: model.ToolCallData{CallID: callID, ToolName: "web_search", Input: action}, native: native,
		})
	case "tool_search_call":
		execution, executionOK := stringField(item, "execution")
		arguments, argumentsOK := rawJSONText(item["arguments"])
		if !executionOK || !argumentsOK {
			return invalidKnown()
		}
		callID, _ := optionalStringField(item, "call_id")
		return handledNested(typeName, eventDraft{
			kind: model.EventKindToolCall, summary: "Dynamic tool search", searchable: arguments,
			data: model.ToolCallData{CallID: callID, ToolName: execution, Input: arguments}, native: native,
		})
	case "tool_search_output":
		status, statusOK := stringField(item, "status")
		execution, executionOK := stringField(item, "execution")
		tools, toolsOK := rawJSONText(item["tools"])
		if !statusOK || !executionOK || !toolsOK {
			return invalidKnown()
		}
		callID, _ := optionalStringField(item, "call_id")
		isError := status != "completed"
		return handledNested(typeName, eventDraft{
			kind: model.EventKindToolResult, summary: "Dynamic tool search result", searchable: tools,
			data: model.ToolResultData{CallID: callID, ToolName: execution, Output: tools, IsError: &isError}, native: native,
		})
	case "compaction", "compaction_summary", "context_compaction":
		encrypted, ok := optionalStringField(item, "encrypted_content")
		if !ok {
			return invalidKnown()
		}
		_ = encrypted // Opaque compaction state is retained but deliberately not indexed.
		return normalizationResult{handled: true, nested: typeName}
	case "additional_tools", "compaction_trigger":
		return normalizationResult{handled: true, nested: typeName}
	default:
		return normalizationResult{nested: typeName}
	}
}

func normalizeEventMessage(raw json.RawMessage, ordinalHistory bool, sessionID string) normalizationResult {
	payload, ok := objectValue(raw)
	if !ok {
		return invalidKnown()
	}
	typeName, exists := stringField(payload, "type")
	if !exists || strings.TrimSpace(typeName) == "" {
		return missingNestedDiscriminant()
	}
	callID, _ := optionalStringField(payload, "call_id")
	native := qualifiedNative(typeName, callID, sessionID)
	switch typeName {
	case "user_message", "agent_message":
		message, ok := firstStringField(payload, "message", "text")
		if !ok {
			return invalidKnown()
		}
		if ordinalHistory {
			return normalizationResult{handled: true, nested: typeName}
		}
		role := model.MessageRoleUser
		if typeName == "agent_message" {
			role = model.MessageRoleAssistant
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindMessage, summary: messageSummary(role), searchable: message,
			data: model.MessageData{Role: role, Text: message}, native: native,
		})
	case "agent_reasoning":
		text, ok := stringField(payload, "text")
		if !ok {
			return invalidKnown()
		}
		if ordinalHistory {
			return normalizationResult{handled: true, nested: typeName}
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindSummary, summary: "Codex reasoning summary", searchable: text,
			data: model.SummaryData{Text: text}, native: native,
		})
	case "exec_command_end":
		command, commandOK := stringSliceTextStrict(payload["command"])
		cwd, cwdOK := optionalStringField(payload, "cwd")
		exit, exitOK := intField(payload, "exit_code")
		if !commandOK || !cwdOK || !exitOK {
			return invalidKnown()
		}
		output, _ := optionalStringField(payload, "aggregated_output")
		if output == "" {
			stdout, stdoutOK := optionalStringField(payload, "stdout")
			stderr, stderrOK := optionalStringField(payload, "stderr")
			if !stdoutOK || !stderrOK {
				return invalidKnown()
			}
			output = stdout + stderr
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindCommand, summary: "Command completed", searchable: command + "\n" + output,
			data:   model.CommandData{Command: command, WorkingDirectory: cwd, ExitCode: &exit, Output: output},
			native: native,
		})
	case "patch_apply_end":
		stdout, stdoutOK := stringField(payload, "stdout")
		stderr, stderrOK := stringField(payload, "stderr")
		_, successOK := boolField(payload, "success")
		if !stdoutOK || !stderrOK || !successOK {
			return invalidKnown()
		}
		text := stdout + stderr
		return handledNested(typeName, eventDraft{
			kind: model.EventKindPatch, summary: "Patch application completed", searchable: text,
			data: model.PatchData{Text: text}, native: native,
		})
	case "token_count":
		infoRaw, exists := payload["info"]
		if !exists || isNull(infoRaw) {
			if _, direct := payload["input_tokens"]; !direct {
				return normalizationResult{handled: true, nested: typeName}
			}
		} else {
			info, ok := objectValue(infoRaw)
			if !ok {
				return invalidKnown()
			}
			usage, ok := objectValue(info["total_token_usage"])
			if !ok {
				return invalidKnown()
			}
			payload = usage
		}
		data, ok := usageData(payload)
		if !ok {
			return invalidKnown()
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindUsage, summary: "Token usage", data: data, native: native,
		})
	case "error", "stream_error", "turn_aborted":
		message, ok := firstStringField(payload, "message", "reason")
		if !ok {
			return invalidKnown()
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindError, summary: "Codex error", searchable: message,
			data: model.ErrorData{Code: typeName, Message: message}, native: native,
		})
	case "context_compacted":
		return handledNested(typeName, eventDraft{
			kind: model.EventKindSummary, summary: "Codex context compaction",
			data: model.SummaryData{}, native: native,
		})
	case "plan_update":
		plan, ok := rawJSONText(payload["plan"])
		if !ok {
			return invalidKnown()
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindSummary, summary: "Codex plan update", searchable: plan,
			data: model.SummaryData{Text: plan}, native: native,
		})
	case "turn_diff":
		diff, ok := stringField(payload, "unified_diff")
		if !ok {
			return invalidKnown()
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindPatch, summary: "Turn diff", searchable: diff,
			data: model.PatchData{Text: diff}, native: native,
		})
	case "mcp_tool_call_begin":
		invocation, ok := mcpInvocation(payload)
		if callID == "" || !ok {
			return invalidKnown()
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindToolCall, summary: "MCP tool call", searchable: invocation.input,
			data: model.ToolCallData{CallID: callID, ToolName: invocation.name, Input: invocation.input}, native: native,
		})
	case "mcp_tool_call_end":
		invocation, ok := mcpInvocation(payload)
		if callID == "" || !ok {
			return invalidKnown()
		}
		output, outputOK := rawJSONText(payload["result"])
		if !outputOK {
			return invalidKnown()
		}
		isError := mcpResultIsError(payload["result"])
		return handledNested(typeName, eventDraft{
			kind: model.EventKindToolResult, summary: "MCP tool result", searchable: output,
			data: model.ToolResultData{CallID: callID, ToolName: invocation.name, Output: output, IsError: &isError}, native: native,
		})
	case "web_search_end":
		query, ok := stringField(payload, "query")
		if callID == "" || !ok {
			return invalidKnown()
		}
		action, actionOK := typedObjectText(payload["action"])
		if !actionOK {
			return invalidKnown()
		}
		if ordinalHistory {
			return normalizationResult{handled: true, nested: typeName}
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindToolResult, summary: "Web search result", searchable: query + "\n" + action,
			data: model.ToolResultData{CallID: callID, ToolName: "web_search", Output: action}, native: native,
		})
	case "dynamic_tool_call_request":
		tool, toolOK := stringField(payload, "tool")
		arguments, argumentsOK := rawJSONText(payload["arguments"])
		if callID == "" || !toolOK || !argumentsOK {
			return invalidKnown()
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindToolCall, summary: "Dynamic tool call: " + tool, searchable: arguments,
			data: model.ToolCallData{CallID: callID, ToolName: tool, Input: arguments}, native: native,
		})
	case "dynamic_tool_call_response":
		tool, toolOK := stringField(payload, "tool")
		success, successOK := boolField(payload, "success")
		output, outputOK := rawJSONText(payload["content_items"])
		if callID == "" || !toolOK || !successOK || !outputOK {
			return invalidKnown()
		}
		isError := !success
		if message, _ := optionalStringField(payload, "error"); message != "" {
			output = strings.TrimSpace(output + "\n" + message)
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindToolResult, summary: "Dynamic tool result", searchable: output,
			data: model.ToolResultData{CallID: callID, ToolName: tool, Output: output, IsError: &isError}, native: native,
		})
	case "collab_agent_spawn_begin", "collab_agent_interaction_begin":
		prompt, promptOK := stringField(payload, "prompt")
		if callID == "" || !promptOK {
			return invalidKnown()
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindToolCall, summary: "Collaboration request", searchable: prompt,
			data: model.ToolCallData{CallID: callID, ToolName: typeName, Input: prompt}, native: native,
		})
	case "collab_waiting_begin", "collab_close_begin", "collab_resume_begin":
		if callID == "" {
			return invalidKnown()
		}
		input, ok := rawJSONText(raw)
		if !ok {
			return invalidKnown()
		}
		return handledNested(typeName, eventDraft{
			kind: model.EventKindToolCall, summary: "Collaboration request", searchable: input,
			data: model.ToolCallData{CallID: callID, ToolName: typeName, Input: input}, native: native,
		})
	case "collab_agent_spawn_end", "collab_agent_interaction_end", "collab_waiting_end", "collab_close_end", "collab_resume_end":
		if callID == "" {
			return invalidKnown()
		}
		output, ok := rawJSONText(raw)
		if !ok {
			return invalidKnown()
		}
		isError := collabFailed(payload)
		return handledNested(typeName, eventDraft{
			kind: model.EventKindToolResult, summary: "Collaboration result", searchable: output,
			data: model.ToolResultData{CallID: callID, ToolName: typeName, Output: output, IsError: &isError}, native: native,
		})
	case "raw_response_item", "raw_response_completed", "item_started", "item_completed",
		"exec_command_begin", "exec_command_output_delta", "terminal_interaction",
		"patch_apply_begin", "patch_apply_updated", "web_search_begin",
		"mcp_startup_update", "mcp_startup_complete", "task_started", "turn_started",
		"task_complete", "turn_complete", "agent_message_content_delta", "plan_delta",
		"reasoning_content_delta", "reasoning_raw_content_delta", "agent_reasoning_section_break",
		"session_configured", "shutdown_complete":
		return normalizationResult{handled: true, nested: typeName}
	default:
		return normalizationResult{nested: typeName}
	}
}

type mcpDraft struct {
	name  string
	input string
}

func mcpInvocation(payload map[string]json.RawMessage) (mcpDraft, bool) {
	invocation, ok := objectValue(payload["invocation"])
	if !ok {
		return mcpDraft{}, false
	}
	server, serverOK := stringField(invocation, "server")
	tool, toolOK := stringField(invocation, "tool")
	arguments, argumentsOK := optionalRawJSONField(invocation, "arguments")
	if !serverOK || !toolOK || !argumentsOK {
		return mcpDraft{}, false
	}
	return mcpDraft{name: server + "/" + tool, input: arguments}, true
}

func usageData(usage map[string]json.RawMessage) (model.UsageData, bool) {
	input, inputOK := int64Field(usage, "input_tokens")
	output, outputOK := int64Field(usage, "output_tokens")
	if !inputOK || !outputOK {
		return model.UsageData{}, false
	}
	data := model.UsageData{InputTokens: &input, OutputTokens: &output}
	if _, exists := usage["cached_input_tokens"]; exists {
		cached, ok := int64Field(usage, "cached_input_tokens")
		if !ok {
			return model.UsageData{}, false
		}
		data.CacheReadTokens = &cached
	}
	if _, exists := usage["cache_write_input_tokens"]; exists {
		cacheWrite, ok := int64Field(usage, "cache_write_input_tokens")
		if !ok {
			return model.UsageData{}, false
		}
		data.CacheWriteTokens = &cacheWrite
	}
	return data, true
}

func handled(drafts ...eventDraft) normalizationResult {
	return normalizationResult{drafts: drafts, handled: true}
}

func handledNested(nested string, drafts ...eventDraft) normalizationResult {
	return normalizationResult{drafts: drafts, handled: true, nested: nested}
}

func invalidKnown() normalizationResult {
	return normalizationResult{handled: true, diagnostics: []model.Diagnostic{{
		Code: "codex.record.structure.invalid", Severity: model.SeverityWarning,
		Message:              "A known Codex record has an invalid structure and was retained.",
		InterpretationReason: model.InterpretationStructurallyInvalidKnownRecord,
	}}}
}

func missingNestedDiscriminant() normalizationResult {
	return normalizationResult{handled: true, diagnostics: []model.Diagnostic{{
		Code: "codex.record.discriminant.missing", Severity: model.SeverityWarning,
		Message:              "A known Codex record has no nested type discriminant and was retained.",
		InterpretationReason: model.InterpretationMissingDiscriminant,
	}}}
}

func objectValue(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var value map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value == nil {
		return nil, false
	}
	return value, true
}

func stringValue(raw json.RawMessage) (string, bool) {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func stringField(object map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := object[key]
	if !ok {
		return "", false
	}
	return stringValue(raw)
}

func optionalStringField(object map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := object[key]
	if !ok || isNull(raw) {
		return "", true
	}
	return stringValue(raw)
}

func firstStringField(object map[string]json.RawMessage, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := stringField(object, key); ok {
			return value, true
		}
	}
	return "", false
}

func boolField(object map[string]json.RawMessage, key string) (bool, bool) {
	raw, ok := object[key]
	if !ok {
		return false, false
	}
	var value bool
	return value, json.Unmarshal(raw, &value) == nil
}

func optionalBoolField(object map[string]json.RawMessage, key string) (*bool, bool) {
	raw, ok := object[key]
	if !ok || isNull(raw) {
		return nil, true
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return nil, false
	}
	return &value, true
}

func intField(object map[string]json.RawMessage, key string) (int, bool) {
	raw, ok := object[key]
	if !ok {
		return 0, false
	}
	var value int
	return value, json.Unmarshal(raw, &value) == nil
}

func int64Field(object map[string]json.RawMessage, key string) (int64, bool) {
	raw, ok := object[key]
	if !ok {
		return 0, false
	}
	var value int64
	return value, json.Unmarshal(raw, &value) == nil
}

func rawJSONText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || !json.Valid(raw) {
		return "", false
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return "", false
	}
	return compact.String(), true
}

func rawObjectText(raw json.RawMessage) (string, bool) {
	if _, ok := objectValue(raw); !ok {
		return "", false
	}
	return rawJSONText(raw)
}

func typedObjectText(raw json.RawMessage) (string, bool) {
	object, ok := objectValue(raw)
	if !ok {
		return "", false
	}
	typeName, ok := stringField(object, "type")
	if !ok || strings.TrimSpace(typeName) == "" {
		return "", false
	}
	return rawJSONText(raw)
}

func optionalRawJSONField(object map[string]json.RawMessage, key string) (string, bool) {
	raw, exists := object[key]
	if !exists || isNull(raw) {
		return "", true
	}
	return rawJSONText(raw)
}

func contentTextStrict(raw json.RawMessage) (string, bool) {
	if text, ok := stringValue(raw); ok {
		return text, true
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return "", false
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		typeName, typeOK := stringField(item, "type")
		if !typeOK {
			return "", false
		}
		switch typeName {
		case "input_text", "output_text":
			text, ok := firstStringField(item, "text", "input_text")
			if !ok {
				return "", false
			}
			parts = append(parts, text)
		case "input_image":
			if _, ok := stringField(item, "image_url"); !ok {
				return "", false
			}
		case "input_audio":
			if _, ok := stringField(item, "audio_url"); !ok {
				return "", false
			}
		default:
			return "", false
		}
	}
	return strings.Join(parts, "\n"), true
}

func messageContentText(raw json.RawMessage, allowEncrypted bool) (string, bool, bool) {
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return "", false, false
	}
	parts := make([]string, 0, len(items))
	plaintext := true
	for _, item := range items {
		typeName, typeOK := stringField(item, "type")
		if !typeOK || strings.TrimSpace(typeName) == "" {
			return "", false, false
		}
		switch typeName {
		case "input_text", "output_text":
			text, ok := stringField(item, "text")
			if !ok {
				return "", false, false
			}
			parts = append(parts, text)
		case "input_image", "input_audio":
			plaintext = false
		case "encrypted_content":
			encrypted, ok := stringField(item, "encrypted_content")
			if !allowEncrypted || !ok || encrypted == "" {
				return "", false, false
			}
			plaintext = false
		default:
			return "", false, false
		}
	}
	return strings.Join(parts, "\n"), true, plaintext
}

func textBlocks(raw json.RawMessage, expectedType, textKey string) (string, bool) {
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return "", false
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		typeName, typeOK := stringField(item, "type")
		text, textOK := stringField(item, textKey)
		if !typeOK || typeName != expectedType || !textOK {
			return "", false
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n"), true
}

func optionalTextBlocks(raw json.RawMessage, expectedType, textKey string) (string, bool) {
	if len(raw) == 0 || isNull(raw) {
		return "", true
	}
	return textBlocks(raw, expectedType, textKey)
}

func stringSliceTextStrict(raw json.RawMessage) (string, bool) {
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		return "", false
	}
	return strings.Join(values, " "), true
}

func isNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func nonEmpty(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func mcpResultIsError(raw json.RawMessage) bool {
	object, ok := objectValue(raw)
	if !ok {
		return true
	}
	if _, failed := object["Err"]; failed {
		return true
	}
	if success, wrapped := object["Ok"]; wrapped {
		object, ok = objectValue(success)
		if !ok {
			return true
		}
	}
	if value, ok := optionalBoolField(object, "is_error"); ok && value != nil {
		return *value
	}
	return false
}

func collabFailed(payload map[string]json.RawMessage) bool {
	status, ok := optionalStringField(payload, "status")
	if !ok || status == "" {
		return false
	}
	switch status {
	case "completed", "idle", "running", "active":
		return false
	default:
		return true
	}
}

func responseNative(item map[string]json.RawMessage, typeName, sessionID string) *model.NativeEventIdentity {
	if id, ok := optionalStringField(item, "id"); ok && id != "" {
		return &model.NativeEventIdentity{Scope: model.NativeEventIDGlobal, EventID: id}
	}
	callID, _ := optionalStringField(item, "call_id")
	return qualifiedNative(typeName, callID, sessionID)
}

func qualifiedNative(typeName, id, sessionID string) *model.NativeEventIdentity {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	return &model.NativeEventIdentity{
		Scope: model.NativeEventIDSession, SessionID: sessionID,
		EventID: typeName + ":" + id,
	}
}

func rawString(raw json.RawMessage) string {
	value, _ := stringValue(raw)
	return value
}

func messageRole(role string) model.MessageRole {
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

func messageSummary(role model.MessageRole) string {
	value := string(role)
	if value == "" {
		return "Unknown message"
	}
	return strings.ToUpper(value[:1]) + value[1:] + " message"
}
