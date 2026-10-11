package claude

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/pooya79/AgentSession/internal/model"
)

// Keep recent linkage, including completed calls for duplicate/contradictory
// results. Evicted or oversized identities remain generic uncertain evidence.
const maxEvidenceCalls = 256

type evidenceCall struct {
	Deferred      bool   `json:"deferred,omitempty"`
	DirectoryHash string `json:"directory_hash,omitempty"`
	ID            string `json:"id"`
	Tool          string `json:"tool"`
	InputHash     string `json:"input_hash"`
	PathHash      string `json:"path_hash,omitempty"`
	MutationHash  string `json:"mutation_hash,omitempty"`
}

type evidenceState struct{ Calls []evidenceCall }

func (s *evidenceState) remember(call evidenceCall) {
	if len(call.ID) > 512 {
		return
	}
	for i, old := range s.Calls {
		if old.ID == call.ID {
			if old != call {
				s.Calls[i].Tool = ""
			}
			return
		}
	}
	if len(s.Calls) == maxEvidenceCalls {
		s.Calls = s.Calls[1:]
	}
	s.Calls = append(s.Calls, call)
}

func (s *evidenceState) lookup(id string) evidenceCall {
	for _, call := range s.Calls {
		if call.ID == id {
			return call
		}
	}
	return evidenceCall{}
}

func evidenceHash(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }

// normalize adds only ordinary client-tool semantics. Hosted and custom tool
// shapes stay generic. A record-level toolUseResult is attributed only when
// the message contains exactly one ordinary result block.
func (s *evidenceState) normalize(record wireRecord, drafts []eventDraft) []eventDraft {
	var message struct {
		Content []json.RawMessage `json:"content"`
	}
	if json.Unmarshal(record.Message, &message) != nil {
		return drafts
	}
	ordinaryCalls := make(map[string]bool)
	ordinaryResults := make(map[string]bool)
	callCounts := make(map[string]int)
	resultCount := 0
	for _, raw := range message.Content {
		var block struct {
			Type, ID  string
			ToolUseID string          `json:"tool_use_id"`
			IsError   json.RawMessage `json:"is_error"`
		}
		if json.Unmarshal(raw, &block) != nil {
			continue
		}
		if block.Type == "tool_use" || block.Type == "server_tool_use" {
			callCounts[block.ID]++
			ordinaryCalls[block.ID] = block.Type == "tool_use"
		}
		if block.Type == "tool_result" {
			var flag bool
			ordinaryResults[block.ToolUseID] = len(block.IsError) == 0 || (!isJSONNull(block.IsError) && json.Unmarshal(block.IsError, &flag) == nil)
		}
		if block.Type == "tool_result" || strings.HasSuffix(block.Type, "_tool_result") {
			resultCount++
		}
	}
	for i, draft := range drafts {
		switch data := draft.data.(type) {
		case model.ToolCallData:
			call := evidenceCall{ID: data.CallID, InputHash: evidenceHash(data.Input)}
			var input map[string]json.RawMessage
			_ = json.Unmarshal([]byte(data.Input), &input)
			if record.Type == "assistant" && ordinaryCalls[data.CallID] && callCounts[data.CallID] == 1 {
				switch data.ToolName {
				case "Edit", "Write":
					file, _, valid := evidenceString(input["file_path"])
					if valid && file != "" {
						call.Tool = data.ToolName
						call.PathHash = evidenceHash(file)
						if directory := evidenceDirectory(record.CWD); directory != "" {
							call.DirectoryHash = evidenceHash(directory)
						}
						call.MutationHash = mutationHash(input, data.ToolName)
						draft = patchDraft(model.PatchData{ChangeID: data.CallID, Status: model.ChangeRequested, Paths: []string{file}, WorkingDirectory: evidenceDirectory(record.CWD)})
					}
				case "Bash":
					command, _, valid := evidenceString(input["command"])
					if valid && command != "" {
						call.Tool = "Bash"
						if raw, ok := input["run_in_background"]; ok {
							var background bool
							call.Deferred = isJSONNull(raw) || json.Unmarshal(raw, &background) != nil || background
						}
						draft = commandDraft(model.CommandData{InvocationID: data.CallID, Phase: model.CommandStarted, Command: command, WorkingDirectory: evidenceDirectory(record.CWD)})
					}
				}
			}
			s.remember(call)
			if s.lookup(call.ID).Tool == "" {
				if patch, ok := draft.data.(model.PatchData); ok {
					patch.Status = model.ChangeUnknown
					draft = patchDraft(patch)
				}
			}
			drafts[i] = draft
		case model.ToolResultData:
			call := s.lookup(data.CallID)
			if record.Type != "user" || !ordinaryResults[data.CallID] || resultCount != 1 {
				continue
			}
			if call.Tool == "Edit" || call.Tool == "Write" {
				drafts[i] = patchDraft(normalizeChangeResult(record, data, call))
			}
			if call.Tool == "Bash" && !call.Deferred && (data.ToolName == "" || data.ToolName == "Bash") {
				// A tool result is completion evidence only for the understood foreground
				// envelope. It never establishes a numeric process exit.
				var result struct {
					Stdout         *string         `json:"stdout"`
					Stderr         *string         `json:"stderr"`
					Interrupted    *bool           `json:"interrupted"`
					IsImage        *bool           `json:"isImage"`
					Background     json.RawMessage `json:"backgroundTaskId"`
					AutoBackground json.RawMessage `json:"assistantAutoBackgrounded"`
				}
				if json.Unmarshal(record.ToolUseResult, &result) == nil && result.Stdout != nil && result.Stderr != nil && result.Interrupted != nil && !*result.Interrupted && result.IsImage != nil && !*result.IsImage && len(result.Background) == 0 && len(result.AutoBackground) == 0 {
					drafts[i] = commandDraft(model.CommandData{InvocationID: data.CallID, Phase: model.CommandCompleted, Output: data.Output, WorkingDirectory: evidenceDirectory(record.CWD)})
				}
			}
		}
	}
	return drafts
}

func commandDraft(data model.CommandData) eventDraft {
	return eventDraft{kind: model.EventKindCommand, summary: "Recorded command " + string(data.Phase), searchable: data.Command + "\n" + data.Output, data: data}
}

func mutationHash(input map[string]json.RawMessage, tool string) string {
	// Hash only understood mutation fields; descriptions and request options are
	// retained raw and do not define the result's effect.
	var values []any
	if tool == "Write" {
		value, present, valid := evidenceString(input["content"])
		if !present || !valid {
			return ""
		}
		values = []any{value}
	} else {
		old, oldPresent, oldValid := evidenceString(input["old_string"])
		next, nextPresent, nextValid := evidenceString(input["new_string"])
		replace := false
		if raw, present := input["replace_all"]; present && (isJSONNull(raw) || json.Unmarshal(raw, &replace) != nil) {
			return ""
		}
		if !oldPresent || !oldValid || !nextPresent || !nextValid {
			return ""
		}
		values = []any{old, next, replace}
	}
	raw, _ := json.Marshal(values)
	return evidenceHash(string(raw))
}

func normalizeChangeResult(record wireRecord, data model.ToolResultData, call evidenceCall) model.PatchData {
	p := model.PatchData{ChangeID: data.CallID, Status: model.ChangeUnknown, WorkingDirectory: evidenceDirectory(record.CWD), Text: string(record.ToolUseResult)}
	var fields map[string]json.RawMessage
	if json.Unmarshal(record.ToolUseResult, &fields) != nil || fields == nil {
		return p
	}
	file, _, valid := evidenceString(fields["filePath"])
	if !valid || file == "" {
		return p
	}
	p.Paths = []string{file}
	p.Changes = []model.FileMutationData{{Path: file, Operation: model.FileMutationUpdate}}
	if !strings.HasPrefix(file, "/") && (p.WorkingDirectory == "" || call.DirectoryHash == "" || call.DirectoryHash != evidenceHash(p.WorkingDirectory)) {
		return p
	}
	if data.ToolName != "" && data.ToolName != call.Tool {
		return p
	}
	for key := range fields {
		switch key {
		case "filePath", "originalFile", "structuredPatch", "userModified":
		case "type", "content":
			if call.Tool != "Write" {
				return p
			}
		case "oldString", "newString", "replaceAll":
			if call.Tool != "Edit" {
				return p
			}
		default:
			return p
		}
	}
	if call.PathHash != evidenceHash(file) || call.MutationHash == "" || (data.IsError != nil && *data.IsError) {
		return p
	}
	var modified bool
	if raw, ok := fields["userModified"]; ok && (isJSONNull(raw) || json.Unmarshal(raw, &modified) != nil || modified) {
		return p
	}
	original, originalPresent, originalValid := evidenceString(fields["originalFile"])
	if call.Tool == "Write" {
		content, present, ok := evidenceString(fields["content"])
		if !present || !ok || mutationHash(map[string]json.RawMessage{"content": fields["content"]}, "Write") != call.MutationHash {
			return p
		}
		kind, _, _ := evidenceString(fields["type"])
		switch kind {
		case "create":
			// null original content plus an explicit creation result distinguishes an
			// empty newly created file from an unchanged existing empty file.
			var patch []json.RawMessage
			if isJSONNull(fields["structuredPatch"]) || json.Unmarshal(fields["structuredPatch"], &patch) != nil {
				return p
			}
			if len(patch) > 0 && patchStatus(fields["structuredPatch"], "", content) != model.ChangeConfirmed {
				return p
			}
			if !originalPresent || !isJSONNull(fields["originalFile"]) {
				return p
			}
			p.Changes[0].Operation = model.FileMutationCreate
			p.Status = model.ChangeConfirmed
		case "update":
			if !originalPresent || !originalValid {
				return p
			}
			p.Status = patchStatus(fields["structuredPatch"], original, content)
		}
		return p
	}
	old, oldPresent, oldValid := evidenceString(fields["oldString"])
	next, nextPresent, nextValid := evidenceString(fields["newString"])
	resultInput := map[string]json.RawMessage{"old_string": fields["oldString"], "new_string": fields["newString"], "replace_all": fields["replaceAll"]}
	if !originalPresent || !originalValid || !oldPresent || !oldValid || !nextPresent || !nextValid || mutationHash(resultInput, "Edit") != call.MutationHash {
		return p
	}
	var replace bool
	if json.Unmarshal(fields["replaceAll"], &replace) != nil || old == "" || !strings.Contains(original, old) {
		return p
	}
	updated := strings.Replace(original, old, next, 1)
	if replace {
		updated = strings.ReplaceAll(original, old, next)
	}
	p.Status = patchStatus(fields["structuredPatch"], original, updated)
	return p
}

// Validate the entire recorded patch against the structured before/after
// evidence. Partial hunks, malformed counts and inconsistent content cannot
// confirm a mutation. No live file is read or written.
func patchStatus(raw json.RawMessage, original, updated string) model.ChangeStatus {
	var hunks []struct {
		OldStart *int     `json:"oldStart"`
		OldLines *int     `json:"oldLines"`
		NewStart *int     `json:"newStart"`
		NewLines *int     `json:"newLines"`
		Lines    []string `json:"lines"`
	}
	if isJSONNull(raw) || json.Unmarshal(raw, &hunks) != nil {
		return model.ChangeUnknown
	}
	if len(hunks) == 0 {
		if original == updated {
			return model.ChangeNoOp
		}
		return model.ChangeUnknown
	}
	// The represented hunks have no end-of-file newline markers. Do not
	// erase a byte-level discrepancy and then call it a confirmed effect.
	if original != "" && updated != "" && strings.HasSuffix(original, "\n") != strings.HasSuffix(updated, "\n") {
		return model.ChangeUnknown
	}
	before := strings.Split(strings.TrimSuffix(original, "\n"), "\n")
	if original == "" {
		before = nil
	}
	var after []string
	position := 0
	for _, h := range hunks {
		if h.OldStart == nil || h.OldLines == nil || h.NewStart == nil || h.NewLines == nil || *h.OldLines < 0 || *h.NewLines < 0 {
			return model.ChangeUnknown
		}
		start := *h.OldStart - 1
		if *h.OldStart == 0 && *h.OldLines == 0 {
			start = 0
		}
		if start < position || start > len(before) {
			return model.ChangeUnknown
		}
		after = append(after, before[position:start]...)
		expectedNewStart := len(after) + 1
		if *h.NewLines == 0 && *h.NewStart == 0 {
			expectedNewStart = 0
		}
		if *h.NewStart != expectedNewStart {
			return model.ChangeUnknown
		}
		position = start
		oldCount, newCount := 0, 0
		for _, line := range h.Lines {
			if len(line) == 0 {
				return model.ChangeUnknown
			}
			switch line[0] {
			case ' ', '-':
				if position >= len(before) || before[position] != line[1:] {
					return model.ChangeUnknown
				}
				position++
				oldCount++
				if line[0] == ' ' {
					after = append(after, line[1:])
					newCount++
				}
			case '+':
				after = append(after, line[1:])
				newCount++
			default:
				return model.ChangeUnknown
			}
		}
		if oldCount != *h.OldLines || newCount != *h.NewLines {
			return model.ChangeUnknown
		}
	}
	after = append(after, before[position:]...)
	if strings.Join(after, "\n") != strings.TrimSuffix(updated, "\n") {
		return model.ChangeUnknown
	}
	if original == updated {
		return model.ChangeNoOp
	}
	return model.ChangeConfirmed
}

func patchDraft(data model.PatchData) eventDraft {
	return eventDraft{kind: model.EventKindPatch, summary: "Recorded change " + string(data.Status), searchable: strings.Join(data.Paths, "\n"), data: data}
}

func evidenceString(raw json.RawMessage) (string, bool, bool) {
	if isJSONNull(raw) {
		return "", true, false
	}
	return requiredString(raw)
}

func evidenceDirectory(value string) string {
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\x00\r\n\\:") || path.Clean(value) != value {
		return ""
	}
	return value
}
