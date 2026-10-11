package opencode

import (
	"crypto/sha256"
	"encoding/json"
	"path"
	"strings"

	"github.com/pooya79/AgentSession/internal/model"
)

// Durable results omit the tool name. Keep bounded attribution, reconstructed
// from the selected snapshot's prefix on every import, including resume.
const maxEvidenceCalls = 256

type evidenceCall struct {
	id, tool string
	input    [32]byte
}

type evidenceState struct{ calls []evidenceCall }

func (s *evidenceState) remember(id, tool, input string) {
	if len(id) > 512 || len(tool) > 512 {
		return
	}
	hash := sha256.Sum256([]byte(input))
	for i, call := range s.calls {
		if call.id == id {
			if call.tool != tool || call.input != hash {
				s.calls[i].tool = ""
			}
			return
		}
	}
	if len(s.calls) == maxEvidenceCalls {
		s.calls = s.calls[1:]
	}
	s.calls = append(s.calls, evidenceCall{id: id, tool: tool, input: hash})
}

func (s *evidenceState) tool(id string) string {
	for _, call := range s.calls {
		if call.id == id {
			return call.tool
		}
	}
	return ""
}

func evidenceDirectory(input map[string]json.RawMessage) string {
	directory, _, valid := optionalString(input, "workdir")
	if !valid || !strings.HasPrefix(directory, "/") || strings.HasPrefix(directory, "//") ||
		strings.ContainsAny(directory, "\x00\r\n\\:") || path.Clean(directory) != directory {
		return ""
	}
	return directory
}

func evidenceCallData(id, tool string, input map[string]json.RawMessage) model.NormalizedData {
	switch tool {
	case "bash":
		command, present, valid := optionalString(input, "command")
		if present && valid && command != "" {
			return model.CommandData{InvocationID: id, Phase: model.CommandStarted, Command: command, WorkingDirectory: evidenceDirectory(input)}
		}
	case "edit", "write", "apply_patch":
		p := model.PatchData{ChangeID: id, Status: model.ChangeRequested}
		if file, _, valid := firstString(input, "path", "filePath"); valid && file != "" {
			p.Paths = []string{file}
		}
		if text, _, valid := optionalString(input, "patchText"); valid {
			p.Text = text
		}
		return p
	}
	return nil
}

func evidenceResultData(id, tool, output string, structured map[string]json.RawMessage, failed bool) model.NormalizedData {
	switch tool {
	case "bash":
		c := model.CommandData{InvocationID: id, Output: output}
		// Tool success/error and output prose do not establish process results.
		var exit int
		var timedOut bool
		timeoutValid := true
		if raw, exists := structured["timeout"]; exists {
			timeoutValid = strings.TrimSpace(string(raw)) != "null" && json.Unmarshal(raw, &timedOut) == nil
		}
		if raw := structured["exit"]; !failed && len(raw) > 0 && strings.TrimSpace(string(raw)) != "null" && json.Unmarshal(raw, &exit) == nil && timeoutValid && !timedOut {
			c.Phase, c.ExitCode = model.CommandCompleted, &exit
		}
		return c
	case "edit", "write", "apply_patch":
		p := model.PatchData{ChangeID: id, Status: model.ChangeUnknown}
		if failed {
			return p
		}
		changes, valid := confirmedToolChanges(tool, structured)
		if !valid {
			return p
		}
		p.Status, p.Changes = model.ChangeConfirmed, changes
		for _, change := range changes {
			p.Paths = append(p.Paths, change.Path)
		}
		if len(changes) == 0 {
			p.Status = model.ChangeNoOp
		}
		return p
	}
	return nil
}

// Only tool-specific application results confirm effects. Patch previews,
// outputPaths, generic success and requested paths are insufficient.
func confirmedToolChanges(tool string, fields map[string]json.RawMessage) ([]model.FileMutationData, bool) {
	if tool == "write" {
		operation, _, _ := optionalString(fields, "operation")
		target, _, valid := optionalString(fields, "target")
		var existed bool
		if operation != "write" || !valid || !absoluteEvidencePath(target) || strings.TrimSpace(string(fields["existed"])) == "null" || json.Unmarshal(fields["existed"], &existed) != nil {
			return nil, false
		}
		mutation := model.FileMutationCreate
		if existed {
			mutation = model.FileMutationUpdate
		}
		return []model.FileMutationData{{Path: target, Operation: mutation}}, true
	}
	key := "files"
	if tool == "apply_patch" {
		key = "applied"
	}
	var entries []map[string]json.RawMessage
	if strings.TrimSpace(string(fields[key])) == "null" || json.Unmarshal(fields[key], &entries) != nil || (tool == "edit" && len(entries) != 1) {
		return nil, false
	}
	var changes []model.FileMutationData
	for _, entry := range entries {
		file, _, valid := optionalString(entry, "file")
		status, _, _ := optionalString(entry, "status")
		if tool == "apply_patch" {
			file, _, valid = optionalString(entry, "target")
			status, _, _ = optionalString(entry, "type")
		}
		if tool == "edit" && status != "modified" {
			return nil, false
		}
		if !valid || !absoluteEvidencePath(file) {
			return nil, false
		}
		mutation := model.FileMutationUnknown
		switch status {
		case "modified", "update":
			mutation = model.FileMutationUpdate
		case "added", "add":
			mutation = model.FileMutationCreate
		case "deleted", "delete":
			mutation = model.FileMutationDelete
		default:
			return nil, false
		}
		changes = append(changes, model.FileMutationData{Path: file, Operation: mutation})
	}
	return changes, true
}

func absoluteEvidencePath(value string) bool {
	return strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") &&
		!strings.ContainsAny(value, "\x00\r\n\\:") && path.Clean(value) == value
}

func withEvidence(event model.Event, data model.NormalizedData) model.Event {
	if data == nil {
		return event
	}
	event.Data = data
	switch data := data.(type) {
	case model.CommandData:
		event.Kind, event.Summary = model.EventKindCommand, "OpenCode command evidence"
		event.SearchableText = data.Command + "\n" + data.Output
	case model.PatchData:
		event.Kind, event.Summary = model.EventKindPatch, "OpenCode change evidence"
		event.SearchableText = strings.Join(data.Paths, "\n")
	}
	return event
}
