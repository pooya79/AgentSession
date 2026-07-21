package codex

import (
	"encoding/json"
	"strings"

	"github.com/pooya79/AgentSession/internal/model"
)

func normalizeRecord(record wireRecord, ordinalHistory bool, sessionID string) (model.EventKind, string, string, model.NormalizedData, *model.NativeEventIdentity, bool) {
	switch record.Type {
	case "response_item":
		return normalizeResponseItem(record.Payload, ordinalHistory, sessionID)
	case "event_msg":
		return normalizeEventMessage(record.Payload, ordinalHistory, sessionID)
	case "compacted":
		var payload struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(record.Payload, &payload) != nil {
			return "", "", "", nil, nil, false
		}
		return model.EventKindSummary, "Codex context compaction", payload.Message, model.SummaryData{Text: payload.Message}, nil, true
	case "turn_context", "world_state", "inter_agent_communication", "inter_agent_communication_metadata":
		return "", "", "", nil, nil, false
	default:
		return "", "", "", nil, nil, false
	}
}

func normalizeResponseItem(raw json.RawMessage, ordinalHistory bool, sessionID string) (model.EventKind, string, string, model.NormalizedData, *model.NativeEventIdentity, bool) {
	var item map[string]json.RawMessage
	if json.Unmarshal(raw, &item) != nil {
		return "", "", "", nil, nil, false
	}
	typeName := rawString(item["type"])
	native := responseNative(item, typeName, sessionID)
	switch typeName {
	case "message":
		if !ordinalHistory {
			return "", "", "", nil, nil, true
		}
		role := messageRole(rawString(item["role"]))
		text := contentText(item["content"])
		return model.EventKindMessage, messageSummary(role), text, model.MessageData{Role: role, Text: text}, native, true
	case "function_call", "custom_tool_call":
		name := rawString(item["name"])
		callID := rawString(item["call_id"])
		input := rawString(item["arguments"])
		if input == "" {
			input = rawString(item["input"])
		}
		if name == "apply_patch" {
			return model.EventKindPatch, "Apply patch", input, model.PatchData{Text: input}, native, true
		}
		return model.EventKindToolCall, "Tool call: " + fallback(name, "unknown"), input, model.ToolCallData{CallID: callID, ToolName: name, Input: input}, native, true
	case "function_call_output", "custom_tool_call_output":
		callID := rawString(item["call_id"])
		name := rawString(item["name"])
		output := rawText(item["output"])
		return model.EventKindToolResult, "Tool result", output, model.ToolResultData{CallID: callID, ToolName: name, Output: output}, native, true
	case "local_shell_call":
		callID := rawString(item["call_id"])
		input := rawText(item["action"])
		return model.EventKindToolCall, "Shell command requested", input, model.ToolCallData{CallID: callID, ToolName: "shell", Input: input}, native, true
	case "compaction", "context_compaction":
		text := rawString(item["encrypted_content"])
		return model.EventKindSummary, "Codex context compaction", text, model.SummaryData{Text: text}, native, true
	default:
		return "", "", "", nil, nil, false
	}
}

func normalizeEventMessage(raw json.RawMessage, ordinalHistory bool, sessionID string) (model.EventKind, string, string, model.NormalizedData, *model.NativeEventIdentity, bool) {
	var payload map[string]json.RawMessage
	if json.Unmarshal(raw, &payload) != nil {
		return "", "", "", nil, nil, false
	}
	typeName := rawString(payload["type"])
	callID := rawString(payload["call_id"])
	native := qualifiedNative(typeName, callID, sessionID)
	switch typeName {
	case "user_message", "agent_message":
		if ordinalHistory {
			return "", "", "", nil, nil, true
		}
		role := model.MessageRoleUser
		if typeName == "agent_message" {
			role = model.MessageRoleAssistant
		}
		text := rawString(payload["message"])
		if text == "" {
			text = rawString(payload["text"])
		}
		return model.EventKindMessage, messageSummary(role), text, model.MessageData{Role: role, Text: text}, native, true
	case "exec_command_end":
		command := stringSliceText(payload["command"])
		output := rawString(payload["aggregated_output"])
		if output == "" {
			output = rawString(payload["stdout"]) + rawString(payload["stderr"])
		}
		exit := rawInt(payload["exit_code"])
		return model.EventKindCommand, "Command completed", command + "\n" + output, model.CommandData{Command: command, WorkingDirectory: rawString(payload["cwd"]), ExitCode: exit, Output: output}, native, true
	case "patch_apply_end":
		text := rawString(payload["stdout"])
		if stderr := rawString(payload["stderr"]); stderr != "" {
			text += stderr
		}
		return model.EventKindPatch, "Patch application completed", text, model.PatchData{Text: text}, native, true
	case "token_count":
		usage := payload
		var info map[string]json.RawMessage
		if json.Unmarshal(payload["info"], &info) == nil {
			if varTotal := info["total_token_usage"]; len(varTotal) > 0 {
				_ = json.Unmarshal(varTotal, &usage)
			}
		}
		data := model.UsageData{InputTokens: rawInt64(usage["input_tokens"]), OutputTokens: rawInt64(usage["output_tokens"]), CacheReadTokens: rawInt64(usage["cached_input_tokens"]), CacheWriteTokens: rawInt64(usage["cache_write_input_tokens"])}
		return model.EventKindUsage, "Token usage", "", data, native, true
	case "error", "stream_error", "turn_aborted":
		message := rawString(payload["message"])
		if message == "" {
			message = rawString(payload["reason"])
		}
		return model.EventKindError, "Codex error", message, model.ErrorData{Code: typeName, Message: message}, native, true
	case "context_compacted":
		return model.EventKindSummary, "Codex context compaction", "", model.SummaryData{}, native, true
	case "raw_response_item", "item_started", "item_completed", "exec_command_begin", "exec_command_output_delta", "patch_apply_begin", "patch_apply_updated":
		return "", "", "", nil, nil, true
	default:
		return "", "", "", nil, nil, false
	}
}

func responseNative(item map[string]json.RawMessage, typeName, sessionID string) *model.NativeEventIdentity {
	if id := rawString(item["id"]); id != "" {
		return &model.NativeEventIdentity{Scope: model.NativeEventIDGlobal, EventID: id}
	}
	return qualifiedNative(typeName, rawString(item["call_id"]), sessionID)
}

func qualifiedNative(typeName, id, sessionID string) *model.NativeEventIdentity {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	return &model.NativeEventIdentity{Scope: model.NativeEventIDSession, SessionID: sessionID, EventID: typeName + ":" + id}
}

func nestedType(raw json.RawMessage) string {
	var payload map[string]json.RawMessage
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	return rawString(payload["type"])
}

func rawString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func rawText(raw json.RawMessage) string {
	if value := rawString(raw); value != "" {
		return value
	}
	if len(raw) == 0 {
		return ""
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func contentText(raw json.RawMessage) string {
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return rawText(raw)
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		text := rawString(item["text"])
		if text == "" {
			text = rawString(item["input_text"])
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
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
	return strings.ToUpper(string(role[:1])) + string(role[1:]) + " message"
}

func rawInt(raw json.RawMessage) *int {
	var value int
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func rawInt64(raw json.RawMessage) *int64 {
	var value int64
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func stringSliceText(raw json.RawMessage) string {
	var values []string
	if json.Unmarshal(raw, &values) == nil {
		return strings.Join(values, " ")
	}
	return rawString(raw)
}

func fallback(value, fallbackValue string) string {
	if value != "" {
		return value
	}
	return fallbackValue
}
