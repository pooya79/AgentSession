package codex

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/pooya79/AgentSession/internal/model"
)

// Only structured completed patch results establish mutation. No output prose
// or patch-request parsing is used to manufacture affected paths.
func (c *commandContext) normalizeChange(record wireRecord) (normalizationResult, bool) {
	item, ok := objectValue(record.Payload)
	if !ok {
		return normalizationResult{}, false
	}
	kind, _ := stringField(item, "type")
	id, _ := stringField(item, "call_id")
	if record.Type == "response_item" && (kind == "function_call" || kind == "custom_tool_call") {
		name, _ := stringField(item, "name")
		if name != "apply_patch" {
			return normalizationResult{}, false
		}
		result := normalizeResponseItem(record.Payload, false, "")
		if len(result.drafts) == 0 {
			return result, true
		}
		data := result.drafts[0].data.(model.PatchData)
		result.drafts[0].native = nil // retain repeated requests and contradictions
		data.ChangeID, data.Status, data.WorkingDirectory = id, model.ChangeRequested, c.cwd
		result.drafts[0].data = data
		c.remember(id, commandLink{invocation: id, form: "patch", directory: c.cwd})
		return result, true
	}
	if record.Type == "response_item" && (kind == "function_call_output" || kind == "custom_tool_call_output") && c.calls[id].form == "patch" {
		result := normalizeResponseItem(record.Payload, false, "")
		if len(result.drafts) == 0 {
			return result, true
		}
		data := result.drafts[0].data.(model.ToolResultData)
		return handled(eventDraft{kind: model.EventKindPatch, summary: "Patch tool response (unconfirmed)", searchable: data.Output, data: model.PatchData{Text: data.Output, ChangeID: id, Status: model.ChangeRequested}}), true
	}
	if record.Type != "event_msg" || (kind != "patch_apply_end" && kind != "patch_apply_begin" && kind != "patch_apply_updated") {
		return normalizationResult{}, false
	}
	data := model.PatchData{ChangeID: id, WorkingDirectory: c.cwd, Status: model.ChangeRequested}
	if link := c.calls[id]; link.form == "patch" {
		data.WorkingDirectory = link.directory
	}
	if kind == "patch_apply_begin" {
		c.remember(id, commandLink{invocation: id, form: "patch", directory: data.WorkingDirectory})
	}
	if kind == "patch_apply_end" {
		stdout, a := stringField(item, "stdout")
		stderr, b := stringField(item, "stderr")
		success, d := boolField(item, "success")
		if !a || !b || !d {
			return invalidKnown(), true
		}
		data.Text = stdout + stderr
		data.Status = model.ChangeUnknown
		status, valid := optionalStringField(item, "status")
		if !valid {
			return invalidKnown(), true
		}
		changes, present := item["changes"]
		parsed, validChanges := patchChanges(changes)
		data.Changes = parsed
		switch {
		case !success && (status == "failed" || status == "declined") && (!present || validChanges):
			data.Status = model.ChangeFailed
		case success && status == "completed" && present && validChanges:
			data.Status = model.ChangeConfirmed
			if len(parsed) == 0 {
				data.Status = model.ChangeNoOp
			}
		}
	} else {
		data.Text, _ = rawJSONText(record.Payload)
	}
	for _, change := range data.Changes {
		data.Paths = append(data.Paths, change.Path)
		if change.PreviousPath != "" {
			data.Paths = append(data.Paths, change.PreviousPath)
		}
	}
	return handled(eventDraft{kind: model.EventKindPatch, summary: "Recorded patch " + string(data.Status), searchable: data.Text, data: data}), true
}

func patchChanges(raw json.RawMessage) ([]model.FileMutationData, bool) {
	changes, ok := objectValue(raw)
	if !ok {
		return nil, false
	}
	paths := make([]string, 0, len(changes))
	for p := range changes {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	result := make([]model.FileMutationData, 0, len(paths))
	for _, p := range paths {
		entry, ok := objectValue(changes[p])
		kind, _ := stringField(entry, "type")
		change := model.FileMutationData{Path: p, Operation: model.FileMutationUnknown}
		switch kind {
		case "add", "delete":
			if _, valid := stringField(entry, "content"); !ok || !valid {
				return nil, false
			}
			change.Operation = model.FileMutationCreate
			if kind == "delete" {
				change.Operation = model.FileMutationDelete
			}
		case "update":
			diff, valid := stringField(entry, "unified_diff")
			move, moveOK := optionalStringField(entry, "move_path")
			if !ok || !valid || !moveOK {
				return nil, false
			}
			if !diffHasEdits(diff) && move == "" {
				if strings.TrimSpace(diff) != "" {
					return nil, false
				}
				continue
			} // explicitly empty update
			change.Operation = model.FileMutationUpdate
			if move != "" {
				change.Path, change.PreviousPath, change.Operation = move, p, model.FileMutationRename
			}
		default:
			return nil, false
		}
		result = append(result, change)
	}
	return result, true
}

func diffHasEdits(diff string) bool {
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			inHunk = true
			continue
		}
		if !inHunk && (strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ")) {
			continue
		}
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			return true
		}
	}
	return false
}
