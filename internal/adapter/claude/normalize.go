package claude

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
}

func normalizeRecord(record wireRecord) []eventDraft {
	if record.IsSidechain {
		label := "sidechain:" + fallback(record.Type, "unknown")
		return []eventDraft{unknownDraft(label, "Claude sidechain record")}
	}
	switch record.Type {
	case "user", "assistant":
		return normalizeMessage(record)
	case "summary":
		return []eventDraft{{kind: model.EventKindSummary, summary: "Claude session summary", searchable: record.Summary, data: model.SummaryData{Text: record.Summary}}}
	case "file-history-snapshot":
		return nil
	default:
		return []eventDraft{unknownDraft(fallback(record.Type, "unknown"), "Unsupported Claude record")}
	}
}

func normalizeMessage(record wireRecord) []eventDraft {
	if isJSONNull(record.Message) {
		return []eventDraft{unknownDraft(record.Type+":message", "Unsupported Claude message")}
	}
	var message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Usage   json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(record.Message, &message) != nil {
		return []eventDraft{unknownDraft(record.Type+":message", "Unsupported Claude message")}
	}
	role := messageRole(message.Role, record.Type)
	var result []eventDraft
	var text string
	if isJSONNull(message.Content) {
		result = append(result, unknownDraft(record.Type+":content", "Unsupported Claude message content"))
	} else if json.Unmarshal(message.Content, &text) == nil {
		result = append(result, messageDraft(role, text))
	} else {
		var blocks []json.RawMessage
		if json.Unmarshal(message.Content, &blocks) != nil {
			result = append(result, unknownDraft(record.Type+":content", "Unsupported Claude message content"))
		} else {
			for _, block := range blocks {
				result = append(result, normalizeBlock(block, role))
			}
		}
	}
	if record.Type == "assistant" {
		if usage, ok := normalizeUsage(message.Usage); ok {
			result = append(result, eventDraft{kind: model.EventKindUsage, summary: "Token usage", data: usage})
		}
	}
	return result
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func normalizeBlock(raw json.RawMessage, role model.MessageRole) eventDraft {
	var block map[string]json.RawMessage
	if json.Unmarshal(raw, &block) != nil {
		return unknownDraft("malformed-content-block", "Unsupported Claude content block")
	}
	typeName := rawString(block["type"])
	switch typeName {
	case "text":
		return messageDraft(role, rawString(block["text"]))
	case "tool_use":
		id := rawString(block["id"])
		name := rawString(block["name"])
		input := rawText(block["input"])
		return eventDraft{kind: model.EventKindToolCall, summary: "Tool call: " + fallback(name, "unknown"), searchable: input, data: model.ToolCallData{CallID: id, ToolName: name, Input: input}}
	case "tool_result":
		id := rawString(block["tool_use_id"])
		output := contentText(block["content"])
		var isError *bool
		if raw, ok := block["is_error"]; ok {
			var value bool
			if json.Unmarshal(raw, &value) == nil {
				isError = &value
			}
		}
		return eventDraft{kind: model.EventKindToolResult, summary: "Tool result", searchable: output, data: model.ToolResultData{CallID: id, Output: output, IsError: isError}}
	default:
		return unknownDraft("content-block:"+fallback(typeName, "unknown"), "Unsupported Claude content block")
	}
}

func normalizeUsage(raw json.RawMessage) (model.UsageData, bool) {
	var usage map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &usage) != nil {
		return model.UsageData{}, false
	}
	data := model.UsageData{
		InputTokens:      rawInt64(usage["input_tokens"]),
		OutputTokens:     rawInt64(usage["output_tokens"]),
		CacheReadTokens:  rawInt64(usage["cache_read_input_tokens"]),
		CacheWriteTokens: rawInt64(usage["cache_creation_input_tokens"]),
	}
	return data, data.InputTokens != nil || data.OutputTokens != nil || data.CacheReadTokens != nil || data.CacheWriteTokens != nil
}

func messageDraft(role model.MessageRole, value string) eventDraft {
	label := string(role)
	if label != "" {
		label = strings.ToUpper(label[:1]) + label[1:]
	}
	return eventDraft{kind: model.EventKindMessage, summary: label + " message", searchable: value, data: model.MessageData{Role: role, Text: value}}
}

func unknownDraft(label, summary string) eventDraft {
	return eventDraft{kind: model.EventKindUnknown, summary: summary + ": " + label, searchable: label, data: model.UnknownData{OriginalKind: label}}
}

func messageRole(role, topLevel string) model.MessageRole {
	if role == "" {
		role = topLevel
	}
	switch role {
	case "user":
		return model.MessageRoleUser
	case "assistant":
		return model.MessageRoleAssistant
	case "system":
		return model.MessageRoleSystem
	case "":
		return model.MessageRoleUnknown
	default:
		return model.MessageRoleOther
	}
}

func rawString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func rawText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
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
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	if len(raw) == 0 {
		return ""
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return rawText(raw)
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if text := rawString(block["text"]); text != "" {
			parts = append(parts, text)
		} else if content := rawString(block["content"]); content != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n")
}

func rawInt64(raw json.RawMessage) *int64 {
	var value int64
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func fallback(value, fallbackValue string) string {
	if value != "" {
		return value
	}
	return fallbackValue
}
