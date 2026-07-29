package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pooya79/AgentSession/internal/model"
)

// maxRecordDiagnostics bounds diagnostics retained for one source record.
const maxRecordDiagnostics = 8

type eventDraft struct {
	kind       model.EventKind
	summary    string
	searchable string
	data       model.NormalizedData
}

// diagnosticDraft carries adapter-local diagnostic data until evidence
// references and stable per-record codes can be attached.
type diagnosticDraft struct {
	code    string
	message string
	reason  model.InterpretationReason
}

// normalizeRecord dispatches one Claude wire record without allowing
// source-specific shapes to escape into the canonical model.
func normalizeRecord(record wireRecord) ([]eventDraft, []diagnosticDraft) {
	switch record.Type {
	case "user", "assistant":
		return normalizeMessage(record)
	case "summary":
		return []eventDraft{summaryDraft("Claude session summary", record.Summary, model.SummaryCategorySummary)}, nil
	case "system":
		return normalizeSystem(record)
	case "progress":
		return normalizeNestedMetadata("progress", nestedDiscriminant(record.Data))
	case "queue-operation":
		return normalizeQueueOperation(record)
	case "attachment":
		discriminant := nestedDiscriminant(record.Attachment)
		if !discriminant.present {
			discriminant = nestedDiscriminant(record.Data)
		}
		if subtype, present, valid := requiredString(record.Subtype); present {
			discriminant = nestedValue{value: subtype, present: true, valid: valid}
		}
		return normalizeNestedMetadata("attachment", discriminant)
	case "file-history-snapshot", "turn_duration", "status", "rate_limit_event",
		"last-prompt", "agent-name", "custom-title", "context", "context-update":
		return nil, nil
	default:
		return []eventDraft{unknownDraft(fallback(record.Type, "unknown"), "Unsupported Claude record", model.UnknownUnsupportedRecordKind)}, nil
	}
}

// normalizeSystem maps supported system subtypes and preserves future valid
// subtypes as explainable unknown events.
func normalizeSystem(record wireRecord) ([]eventDraft, []diagnosticDraft) {
	subtype, present, valid := requiredString(record.Subtype)
	if !present {
		return nil, []diagnosticDraft{missingDiagnostic("claude.system.subtype.missing", "The Claude system record has no subtype discriminant.")}
	}
	if !valid || subtype == "" {
		return nil, []diagnosticDraft{invalidDiagnostic("claude.system.subtype.invalid", "The Claude system record has an invalid subtype discriminant.")}
	}
	text, textOK := recordText(record)
	switch subtype {
	case "away_summary", "compact_boundary":
		if !textOK {
			text = ""
		}
		category := model.SummaryCategorySummary
		if subtype == "compact_boundary" {
			category = model.SummaryCategoryContext
		}
		return []eventDraft{summaryDraft("Claude "+strings.ReplaceAll(subtype, "_", " "), text, category)}, nil
	case "api_error":
		if !textOK {
			return nil, []diagnosticDraft{invalidDiagnostic("claude.system.api_error.invalid", "The Claude API error message is malformed.")}
		}
		return []eventDraft{{kind: model.EventKindError, summary: "Claude API error", searchable: text, data: model.ErrorData{Code: subtype, Message: text}}}, nil
	case "local_command", "informational":
		if !textOK {
			return nil, []diagnosticDraft{invalidDiagnostic("claude.system.message.invalid", "The Claude system message text is malformed.")}
		}
		return []eventDraft{messageDraft(model.MessageRoleSystem, text, false)}, nil
	case "turn_duration", "status", "stop_hook_summary", "task_notification":
		return nil, nil
	default:
		return []eventDraft{unknownDraft("system:"+subtype, "Unsupported Claude system variant", model.UnknownUnsupportedNestedVariant)}, nil
	}
}

// normalizeQueueOperation validates queue discriminants while treating known
// operations as transient metadata.
func normalizeQueueOperation(record wireRecord) ([]eventDraft, []diagnosticDraft) {
	operation, present, valid := requiredString(record.Operation)
	if !present {
		operation, present, valid = requiredString(record.Subtype)
	}
	if !present {
		return nil, []diagnosticDraft{missingDiagnostic("claude.queue_operation.operation.missing", "The Claude queue operation has no operation discriminant.")}
	}
	if !valid || operation == "" {
		return nil, []diagnosticDraft{invalidDiagnostic("claude.queue_operation.operation.invalid", "The Claude queue operation has an invalid operation discriminant.")}
	}
	switch operation {
	case "enqueue", "dequeue", "remove":
		return nil, nil
	default:
		return []eventDraft{unknownDraft("queue-operation:"+operation, "Unsupported Claude queue operation", model.UnknownUnsupportedNestedVariant)}, nil
	}
}

// normalizeNestedMetadata classifies progress and attachment envelopes from
// their nested discriminator.
func normalizeNestedMetadata(parent string, discriminant nestedValue) ([]eventDraft, []diagnosticDraft) {
	if !discriminant.present {
		return nil, []diagnosticDraft{missingDiagnostic("claude."+parent+".type.missing", "The Claude "+parent+" record has no nested type discriminant.")}
	}
	if !discriminant.valid || discriminant.value == "" {
		return nil, []diagnosticDraft{invalidDiagnostic("claude."+parent+".type.invalid", "The Claude "+parent+" record has an invalid nested type discriminant.")}
	}
	var known bool
	switch parent {
	case "progress":
		switch discriminant.value {
		case "hook_progress", "mcp_progress", "agent_progress", "bash_progress", "powershell_progress":
			known = true
		}
	case "attachment":
		switch discriminant.value {
		case "attachment", "context", "file", "directory", "selection", "diagnostic",
			"task_reminder", "deferred_tools_delta", "skill_listing", "agent_listing_delta",
			"queued_command", "diagnostics", "command_permissions", "edited_text_file",
			"plan_mode_exit", "plan_mode", "nested_memory", "dynamic_skill":
			known = true
		}
	}
	if known {
		return nil, nil
	}
	return []eventDraft{unknownDraft(parent+":"+discriminant.value, "Unsupported Claude "+parent+" variant", model.UnknownUnsupportedNestedVariant)}, nil
}

// normalizeMessage independently retains valid sibling blocks and diagnostics
// so one malformed block does not suppress other evidence in the message.
func normalizeMessage(record wireRecord) ([]eventDraft, []diagnosticDraft) {
	if len(record.Message) == 0 || isJSONNull(record.Message) {
		return nil, []diagnosticDraft{invalidDiagnostic("claude.message.invalid", "The Claude message object is missing or null.")}
	}
	var message map[string]json.RawMessage
	if json.Unmarshal(record.Message, &message) != nil || message == nil {
		return nil, []diagnosticDraft{invalidDiagnostic("claude.message.invalid", "The Claude message object is malformed.")}
	}

	roleName, rolePresent, roleValid := requiredString(message["role"])
	var diagnostics []diagnosticDraft
	if !rolePresent || !roleValid || roleName == "" {
		diagnostics = append(diagnostics, invalidDiagnostic("claude.message.role.invalid", "The Claude message role is missing or malformed."))
	} else if roleName != record.Type {
		diagnostics = append(diagnostics, invalidDiagnostic("claude.message.role.conflict", "The Claude message role conflicts with its top-level type."))
		roleName = record.Type
	}
	role := messageRole(roleName, record.Type)

	content, present := message["content"]
	if !present || isJSONNull(content) {
		diagnostics = append(diagnostics, invalidDiagnostic("claude.message.content.invalid", "The Claude message content is missing or null."))
		return nil, diagnostics
	}

	var result []eventDraft
	var text string
	if json.Unmarshal(content, &text) == nil {
		result = append(result, messageDraft(role, text, record.IsSidechain))
	} else {
		var blocks []json.RawMessage
		if json.Unmarshal(content, &blocks) != nil {
			diagnostics = append(diagnostics, invalidDiagnostic("claude.message.content.invalid", "The Claude message content is neither text nor a content-block array."))
			return nil, diagnostics
		}
		for index, block := range blocks {
			draft, diagnostic, ok := normalizeBlock(block, role, record.IsSidechain)
			if diagnostic != nil {
				diagnostic.code = fmt.Sprintf("%s.%d", diagnostic.code, index)
				diagnostics = append(diagnostics, *diagnostic)
			}
			if ok {
				result = append(result, draft)
			}
		}
	}
	if record.Type == "assistant" {
		usage, hasUsage, usageDiagnostics := normalizeUsage(message["usage"])
		diagnostics = append(diagnostics, usageDiagnostics...)
		if hasUsage {
			result = append(result, eventDraft{kind: model.EventKindUsage, summary: provenanceSummary("Token usage", record.IsSidechain), data: usage})
		}
	}
	return result, diagnostics
}

// normalizeBlock maps one message content block; its final result reports
// whether a canonical event was produced.
func normalizeBlock(raw json.RawMessage, role model.MessageRole, sidechain bool) (eventDraft, *diagnosticDraft, bool) {
	var block map[string]json.RawMessage
	if json.Unmarshal(raw, &block) != nil || block == nil {
		d := invalidDiagnostic("claude.content_block.invalid", "A Claude content block is not an object.")
		return eventDraft{}, &d, false
	}
	typeName, present, valid := requiredString(block["type"])
	if !present {
		d := missingDiagnostic("claude.content_block.type.missing", "A Claude content block has no type discriminant.")
		return eventDraft{}, &d, false
	}
	if !valid || typeName == "" {
		d := invalidDiagnostic("claude.content_block.type.invalid", "A Claude content block has an invalid type discriminant.")
		return eventDraft{}, &d, false
	}
	switch typeName {
	case "text":
		text, textPresent, textValid := requiredString(block["text"])
		if !textPresent || !textValid {
			d := invalidDiagnostic("claude.content_block.text.invalid", "A Claude text block has malformed text.")
			return eventDraft{}, &d, false
		}
		return messageDraft(role, text, sidechain), nil, true
	case "tool_use", "server_tool_use":
		id, idPresent, idValid := requiredString(block["id"])
		name, namePresent, nameValid := requiredString(block["name"])
		input, inputPresent, inputValid := compactValue(block["input"])
		if !idPresent || !idValid || id == "" || !namePresent || !nameValid || name == "" || !inputPresent || !inputValid {
			d := invalidDiagnostic("claude.content_block."+typeName+".invalid", "A Claude tool-call block has invalid required fields.")
			return eventDraft{}, &d, false
		}
		return eventDraft{
			kind: model.EventKindToolCall, summary: provenanceSummary("Tool call: "+name, sidechain), searchable: input,
			data: model.ToolCallData{CallID: id, ToolName: name, Input: input},
		}, nil, true
	case "tool_result":
		return normalizeToolResult(typeName, block, sidechain)
	case "web_search_tool_result", "web_fetch_tool_result", "tool_search_tool_result",
		"code_execution_tool_result", "text_editor_code_execution_tool_result",
		"bash_code_execution_tool_result", "mcp_tool_result", "advisor_tool_result":
		return normalizeToolResult(typeName, block, sidechain)
	case "thinking", "redacted_thinking", "image", "document", "search_result", "tool_reference":
		if !validUnsupportedBlock(typeName, block) {
			d := invalidDiagnostic("claude.content_block."+typeName+".invalid", "A known unsupported Claude content block is structurally malformed.")
			return eventDraft{}, &d, false
		}
		return unknownDraft("content-block:"+typeName, provenanceSummary("Unsupported Claude content block", sidechain), model.UnknownUnsupportedNestedVariant), nil, true
	default:
		return unknownDraft("content-block:"+typeName, provenanceSummary("Unsupported Claude content block", sidechain), model.UnknownUnsupportedNestedVariant), nil, true
	}
}

// validUnsupportedBlock distinguishes structurally valid opaque evidence from
// malformed instances of known but unsupported block types.
func validUnsupportedBlock(typeName string, block map[string]json.RawMessage) bool {
	switch typeName {
	case "thinking":
		_, present, valid := requiredString(block["thinking"])
		if !present || !valid {
			return false
		}
		if _, signaturePresent, signatureValid := requiredString(block["signature"]); signaturePresent && !signatureValid {
			return false
		}
		return true
	case "redacted_thinking":
		_, present, valid := requiredString(block["data"])
		return present && valid
	case "image", "document":
		var source map[string]json.RawMessage
		return json.Unmarshal(block["source"], &source) == nil && source != nil
	case "search_result":
		_, titlePresent, titleValid := requiredString(block["title"])
		_, contentPresent, contentValid := requiredString(block["content"])
		return (titlePresent && titleValid) || (contentPresent && contentValid)
	case "tool_reference":
		_, present, valid := firstRequiredString(block, "tool_name", "name")
		return present && valid
	default:
		return false
	}
}

// normalizeToolResult preserves textual or structured tool output while
// rejecting missing, null, or malformed result fields.
func normalizeToolResult(typeName string, block map[string]json.RawMessage, sidechain bool) (eventDraft, *diagnosticDraft, bool) {
	callID, idPresent, idValid := firstRequiredString(block, "tool_use_id", "tool_call_id", "id")
	if !idPresent || !idValid || callID == "" {
		d := invalidDiagnostic("claude.content_block."+typeName+".invalid", "A Claude tool-result block has no valid call identifier.")
		return eventDraft{}, &d, false
	}
	outputRaw, outputPresent := block["content"]
	if !outputPresent {
		outputRaw, outputPresent = block["output"]
	}
	if outputPresent && isJSONNull(outputRaw) {
		outputPresent = false
	}
	output, outputValid := structuredOutput(outputRaw)
	if !outputPresent || !outputValid {
		d := invalidDiagnostic("claude.content_block."+typeName+".invalid", "A Claude tool-result block has malformed output.")
		return eventDraft{}, &d, false
	}
	var isError *bool
	if raw, ok := block["is_error"]; ok {
		var value bool
		if json.Unmarshal(raw, &value) != nil {
			d := invalidDiagnostic("claude.content_block."+typeName+".invalid", "A Claude tool-result block has a malformed error flag.")
			return eventDraft{}, &d, false
		}
		isError = &value
	}
	toolName, toolNamePresent, toolNameValid := firstRequiredString(block, "name", "tool_name")
	if toolNamePresent && !toolNameValid {
		d := invalidDiagnostic("claude.content_block."+typeName+".invalid", "A Claude tool-result block has a malformed tool name.")
		return eventDraft{}, &d, false
	}
	return eventDraft{
		kind: model.EventKindToolResult, summary: provenanceSummary("Tool result", sidechain), searchable: output,
		data: model.ToolResultData{CallID: callID, ToolName: toolName, Output: output, IsError: isError},
	}, nil, true
}

// normalizeUsage retains valid non-negative counters and diagnoses malformed
// counters independently.
func normalizeUsage(raw json.RawMessage) (model.UsageData, bool, []diagnosticDraft) {
	if len(raw) == 0 || isJSONNull(raw) {
		return model.UsageData{}, false, nil
	}
	var usage map[string]json.RawMessage
	if json.Unmarshal(raw, &usage) != nil || usage == nil {
		return model.UsageData{}, false, []diagnosticDraft{invalidDiagnostic("claude.usage.invalid", "Claude token usage is malformed.")}
	}
	var diagnostics []diagnosticDraft
	read := func(name string) *int64 {
		value, present := usage[name]
		if !present {
			return nil
		}
		var count int64
		if json.Unmarshal(value, &count) != nil || count < 0 {
			diagnostics = append(diagnostics, invalidDiagnostic("claude.usage."+name+".invalid", "A Claude token-usage counter is malformed."))
			return nil
		}
		return &count
	}
	data := model.UsageData{
		InputTokens:      read("input_tokens"),
		OutputTokens:     read("output_tokens"),
		CacheReadTokens:  read("cache_read_input_tokens"),
		CacheWriteTokens: read("cache_creation_input_tokens"),
	}
	return data, data.InputTokens != nil || data.OutputTokens != nil || data.CacheReadTokens != nil || data.CacheWriteTokens != nil, diagnostics
}

// messageDraft constructs a canonical message draft with source provenance in
// its presentation summary.
func messageDraft(role model.MessageRole, value string, sidechain bool) eventDraft {
	label := string(role)
	if label != "" {
		label = strings.ToUpper(label[:1]) + label[1:]
	}
	return eventDraft{kind: model.EventKindMessage, summary: provenanceSummary(label+" message", sidechain), searchable: value, data: model.MessageData{Role: role, Text: value}}
}

// summaryDraft constructs a searchable canonical summary.
func summaryDraft(label, text string, category model.SummaryCategory) eventDraft {
	return eventDraft{kind: model.EventKindSummary, summary: label, searchable: text, data: model.SummaryData{Category: category, Text: text}}
}

// provenanceSummary marks sidechain evidence without changing canonical data.
func provenanceSummary(summary string, sidechain bool) string {
	if sidechain {
		return summary + " (sidechain)"
	}
	return summary
}

func unknownDraft(label, summary string, reason model.UnknownReason) eventDraft {
	return eventDraft{kind: model.EventKindUnknown, summary: summary + ": " + label, searchable: label, data: model.UnknownData{Reason: reason, OriginalKind: model.BoundOriginalKind(label)}}
}

// missingDiagnostic creates a missing-discriminant diagnostic draft.
func missingDiagnostic(code, message string) diagnosticDraft {
	return diagnosticDraft{code: code, message: message, reason: model.InterpretationMissingDiscriminant}
}

// invalidDiagnostic creates a structurally-invalid-record diagnostic draft.
func invalidDiagnostic(code, message string) diagnosticDraft {
	return diagnosticDraft{code: code, message: message, reason: model.InterpretationStructurallyInvalidKnownRecord}
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

// requiredString returns the decoded value followed by independent presence
// and type-validity flags.
func requiredString(raw json.RawMessage) (string, bool, bool) {
	if len(raw) == 0 {
		return "", false, false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", true, false
	}
	return value, true, true
}

// firstRequiredString reads the first present alias and reports that alias's
// validity without falling through to lower-priority fields.
func firstRequiredString(values map[string]json.RawMessage, names ...string) (string, bool, bool) {
	for _, name := range names {
		if raw, ok := values[name]; ok {
			value, _, valid := requiredString(raw)
			return value, true, valid
		}
	}
	return "", false, false
}

// compactValue validates and compacts arbitrary JSON while distinguishing an
// absent value from malformed JSON.
func compactValue(raw json.RawMessage) (string, bool, bool) {
	if len(raw) == 0 {
		return "", false, false
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return "", true, false
	}
	return compact.String(), true, true
}

// structuredOutput preserves strings directly, flattens arrays of textual
// blocks, and otherwise retains valid JSON in compact form.
func structuredOutput(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, true
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) == nil {
		parts := make([]string, 0, len(blocks))
		allText := true
		for _, block := range blocks {
			value, present, valid := firstRequiredString(block, "text", "content")
			if !present || !valid {
				allText = false
				break
			}
			parts = append(parts, value)
		}
		if allText {
			return strings.Join(parts, "\n"), true
		}
	}
	value, _, valid := compactValue(raw)
	return value, valid
}

// nestedValue records a nested discriminator's value, presence, and validity
// without collapsing malformed input into absence.
type nestedValue struct {
	value   string
	present bool
	valid   bool
}

// nestedDiscriminant extracts a nested object's type discriminator.
func nestedDiscriminant(raw json.RawMessage) nestedValue {
	if len(raw) == 0 || isJSONNull(raw) {
		return nestedValue{}
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return nestedValue{present: true}
	}
	typeName, present, valid := requiredString(value["type"])
	return nestedValue{value: typeName, present: present, valid: valid}
}

// recordText finds text across documented Claude record fields in priority
// order and reports malformed present text as invalid.
func recordText(record wireRecord) (string, bool) {
	for _, raw := range []json.RawMessage{record.Content, record.Message, record.Error, record.Data} {
		if len(raw) == 0 {
			continue
		}
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return text, true
		}
		var value map[string]json.RawMessage
		if json.Unmarshal(raw, &value) == nil {
			for _, key := range []string{"text", "message", "error", "summary", "content"} {
				if text, present, valid := requiredString(value[key]); present {
					return text, valid
				}
			}
		}
	}
	if record.Summary != "" {
		return record.Summary, true
	}
	return "", false
}

// isJSONNull recognizes JSON null with optional surrounding whitespace.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func fallback(value, fallbackValue string) string {
	if value != "" {
		return value
	}
	return fallbackValue
}
