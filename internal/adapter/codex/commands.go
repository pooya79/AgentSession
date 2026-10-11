package codex

import (
	"crypto/sha256"
	"encoding/json"
	"path"
	"strconv"
	"strings"

	"github.com/pooya79/AgentSession/internal/model"
)

// Correlation contains identifiers and script digests, never complete logs or output.
// On exhaustion, subsequent tool records retain their generic interpretation,
// which exposes an evidence gap to analysis instead of guessing a relationship.
const maxCommandLinks = 4096
const maxCommandLinkBytes = 512

type commandLink struct {
	invocation string
	form       string
	scriptHash [32]byte
	hasScript  bool
}

type commandContext struct {
	cwd       string
	calls     map[string]commandLink
	processes map[int]string
}

func (c *commandContext) remember(id string, link commandLink) bool {
	if id == "" || len(id) > maxCommandLinkBytes {
		return false
	}
	if c.calls == nil {
		c.calls = make(map[string]commandLink)
	}
	if old, exists := c.calls[id]; exists {
		if old.invocation == link.invocation && old.form == "event" {
			c.calls[id] = link
			return true
		}
		if old != link {
			c.calls[id] = commandLink{}
			return false
		}
		return true
	}
	if len(c.calls) >= maxCommandLinks {
		return false
	}
	c.calls[id] = link
	return true
}

func commandDraft(data model.CommandData) normalizationResult {
	summary := "Command started"
	if data.Phase == model.CommandCompleted {
		summary = "Command completed"
	}
	return handled(eventDraft{kind: model.EventKindCommand, summary: summary, searchable: data.Command + "\n" + data.Output, data: data})
}

func commandProgress(output string) normalizationResult {
	return handled(eventDraft{kind: model.EventKindSummary, summary: "Command result without final process exit", searchable: output, data: model.SummaryData{Category: model.SummaryCategoryContext, Text: output}})
}

// normalize interprets only recorded Codex command forms. The ordinary
// normalizer remains responsible for unrelated tools and unsupported forms.
func (c *commandContext) normalize(record wireRecord) (normalizationResult, bool) {
	item, ok := objectValue(record.Payload)
	if !ok {
		return normalizationResult{}, false
	}
	if record.Type == "session_meta" || record.Type == "turn_context" {
		if raw, exists := item["cwd"]; exists {
			cwd, valid := stringValue(raw)
			c.cwd = ""
			if valid && len(cwd) <= 4096 {
				c.cwd = cwd
			}
		}
		return normalizationResult{}, false
	}
	if record.Type == "event_msg" {
		kind, _ := stringField(item, "type")
		if kind != "exec_command_begin" && kind != "exec_command_end" {
			return normalizationResult{}, false
		}
		result := normalizeEventMessage(record.Payload, false, "")
		if len(result.drafts) == 0 {
			return result, true
		}
		data := result.drafts[0].data.(model.CommandData)
		status, statusOK := optionalStringField(item, "status")
		if !statusOK {
			return invalidKnown(), true
		}
		if status != "" && status != "completed" && status != "failed" {
			data.ExitCode = nil
		}
		data.Command, _ = stringSliceTextStrict(item["command"])
		if _, exists := item["command"]; !exists {
			data.Command = ""
		}
		source, _ := stringField(item, "source")
		if source == "unified_exec_interaction" {
			processText, _ := stringField(item, "process_id")
			process, err := strconv.Atoi(processText)
			original := c.processes[process]
			if err != nil || original == "" {
				data.InvocationID = ""
				return commandDraft(data), true
			}
			data.InvocationID = original
			if data.Phase == model.CommandStarted {
				return commandProgress("Interaction with recorded command process"), true
			}
		} else if data.Phase == model.CommandStarted {
			if _, exists := c.calls[data.InvocationID]; !exists {
				c.remember(data.InvocationID, commandLink{invocation: data.InvocationID, form: "event"})
			}
			if processText, valid := stringField(item, "process_id"); valid {
				if process, err := strconv.Atoi(processText); err == nil {
					c.rememberProcess(process, data.InvocationID)
				}
			}
		}
		// An event's shell wrapper describes the same script only when the
		// recorded call already links it to an identical script-text request.
		// Standalone argv commands keep their shell and flags in their identity.
		if link := c.calls[data.InvocationID]; link.hasScript {
			if script, ok := recordedShellScript(item["command"]); ok && sha256.Sum256([]byte(script)) == link.scriptHash {
				data.Command = script
			}
		}
		return commandDraft(data), true
	}
	if record.Type != "response_item" {
		return normalizationResult{}, false
	}
	kind, _ := stringField(item, "type")
	id, _ := stringField(item, "call_id")
	if kind == "local_shell_call" {
		if id == "" {
			id, _ = stringField(item, "id")
		}
		action, valid := objectValue(item["action"])
		actionType, _ := stringField(action, "type")
		if !valid || actionType != "exec" {
			return normalizationResult{}, false
		}
		command, commandOK := stringSliceTextStrict(action["command"])
		cwd, cwdOK := optionalStringField(action, "working_directory")
		if !commandOK || !cwdOK {
			return invalidKnown(), true
		}
		if !c.remember(id, commandLink{invocation: id, form: "shell"}) {
			return normalizationResult{}, false
		}
		return commandDraft(model.CommandData{InvocationID: id, Phase: model.CommandStarted, Command: command, WorkingDirectory: c.directory(cwd)}), true
	}
	if kind == "function_call" {
		name, _ := stringField(item, "name")
		if name != "exec_command" && name != "write_stdin" && name != "shell" && name != "container.exec" && name != "shell_command" {
			return normalizationResult{}, false
		}
		input, valid := stringField(item, "arguments")
		args, argsOK := objectValue(json.RawMessage(input))
		if !valid || !argsOK || strings.TrimSpace(id) == "" {
			return invalidKnown(), true
		}
		if name == "write_stdin" {
			process, valid := intField(args, "session_id")
			if !valid {
				return invalidKnown(), true
			}
			original := c.processes[process]
			if original == "" || !c.remember(id, commandLink{invocation: original, form: "exec"}) {
				return normalizationResult{}, false
			}
			return commandProgress("Polling recorded command process"), true
		}
		command, valid := stringField(args, "cmd")
		form := "exec"
		if name != "exec_command" {
			// Other argument grammars remain generic, uncertain tool evidence.
			if _, present := args["command"]; !present {
				return normalizationResult{}, false
			}
			form = "shell"
			if name == "shell_command" {
				command, valid = stringField(args, "command")
			} else {
				command, valid = stringSliceTextStrict(args["command"])
			}
		}
		cwd, cwdOK := optionalStringField(args, "workdir")
		if !valid || !cwdOK {
			return invalidKnown(), true
		}
		environment, environmentOK := optionalStringField(args, "environment_id")
		if !environmentOK {
			return invalidKnown(), true
		}
		if environment != "" && !path.IsAbs(cwd) {
			cwd = ""
		} else {
			cwd = c.directory(cwd)
		}
		link := commandLink{invocation: id, form: form}
		if name == "exec_command" || name == "shell_command" {
			link.hasScript = true
			link.scriptHash = sha256.Sum256([]byte(command))
		}
		if !c.remember(id, link) {
			return normalizationResult{}, false
		}
		return commandDraft(model.CommandData{InvocationID: id, Phase: model.CommandStarted, Command: command, WorkingDirectory: cwd}), true
	}
	if kind != "function_call_output" && kind != "custom_tool_call_output" {
		return normalizationResult{}, false
	}
	link, linked := c.calls[id]
	if !linked || link.invocation == "" {
		return normalizationResult{}, false
	}
	output, valid := contentTextStrict(item["output"])
	if !valid {
		return invalidKnown(), true
	}
	if _, valid := optionalBoolField(item, "is_error"); !valid {
		return invalidKnown(), true
	}
	exit, process, recognized := execResultHeader(output)
	if link.form == "shell" {
		exit, recognized = shellResultHeader(output)
		process = nil
	}
	if link.form == "event" && !recognized {
		exit, recognized = shellResultHeader(output)
	}
	if !recognized {
		return commandProgress(output), true
	}
	if process != nil {
		c.rememberProcess(*process, link.invocation)
		return commandProgress(output), true
	}
	return commandDraft(model.CommandData{InvocationID: link.invocation, Phase: model.CommandCompleted, ExitCode: exit, Output: output}), true
}

func (c *commandContext) rememberProcess(process int, invocation string) {
	if invocation == "" || len(invocation) > maxCommandLinkBytes {
		return
	}
	if c.processes == nil {
		c.processes = make(map[int]string)
	}
	if old, exists := c.processes[process]; exists && old != invocation {
		// Reuse cannot silently move an established process to another call.
		c.processes[process] = ""
	} else if !exists && len(c.processes) < maxCommandLinks {
		c.processes[process] = invocation
	}
}

func recordedShellScript(raw json.RawMessage) (string, bool) {
	var args []string
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", false
	}
	if len(args) == 3 && (args[1] == "-c" || args[1] == "-lc") {
		switch args[0] {
		case "sh", "/bin/sh", "/usr/bin/sh", "bash", "/bin/bash", "/usr/bin/bash", "zsh", "/bin/zsh", "/usr/bin/zsh":
			return args[2], true
		}
	}
	return "", false
}

func (c *commandContext) directory(directory string) string {
	if directory == "" {
		directory = c.cwd
	}
	if path.IsAbs(directory) {
		return directory
	}
	if path.IsAbs(c.cwd) {
		return path.Join(c.cwd, directory)
	}
	return ""
}

// format_exec_output_for_model (Codex 0.145.0 shell_command).
func shellResultHeader(output string) (*int, bool) {
	header, _, found := strings.Cut(output, "\nOutput:")
	if !found {
		return nil, false
	}
	lines := strings.Split(header, "\n")
	if len(lines) < 2 || len(lines) > 3 || !strings.HasPrefix(lines[0], "Exit code: ") {
		return nil, false
	}
	exit, err := strconv.Atoi(strings.TrimPrefix(lines[0], "Exit code: "))
	if err != nil || !strings.HasPrefix(lines[1], "Wall time: ") || !strings.HasSuffix(lines[1], " seconds") {
		return nil, false
	}
	if _, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimPrefix(lines[1], "Wall time: "), " seconds"), 64); err != nil {
		return nil, false
	}
	if len(lines) == 3 {
		if !strings.HasPrefix(lines[2], "Total output lines: ") {
			return nil, false
		}
		if _, err := strconv.Atoi(strings.TrimPrefix(lines[2], "Total output lines: ")); err != nil {
			return nil, false
		}
	}
	return &exit, true
}

// ExecCommandToolOutput::response_text (Codex 0.145.0). Read the envelope
// before Output: only; child output is arbitrary untrusted prose.
func execResultHeader(output string) (exit, process *int, recognized bool) {
	header, _, found := strings.Cut(output, "\nOutput:")
	if !found {
		return nil, nil, false
	}
	lines := strings.Split(header, "\n")
	i := 0
	if strings.HasPrefix(lines[0], "Chunk ID: ") {
		i++
	}
	if i >= len(lines) || !strings.HasPrefix(lines[i], "Wall time: ") || !strings.HasSuffix(lines[i], " seconds") {
		return nil, nil, false
	}
	duration := strings.TrimSuffix(strings.TrimPrefix(lines[i], "Wall time: "), " seconds")
	if _, err := strconv.ParseFloat(duration, 64); err != nil {
		return nil, nil, false
	}
	i++
	for ; i < len(lines); i++ {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "Process exited with code ") && exit == nil:
			value, err := strconv.Atoi(strings.TrimPrefix(line, "Process exited with code "))
			if err != nil {
				return nil, nil, false
			}
			exit = &value
		case strings.HasPrefix(line, "Process running with session ID ") && process == nil:
			value, err := strconv.Atoi(strings.TrimPrefix(line, "Process running with session ID "))
			if err != nil {
				return nil, nil, false
			}
			process = &value
		case strings.HasPrefix(line, "Original token count: "):
			if _, err := strconv.Atoi(strings.TrimPrefix(line, "Original token count: ")); err != nil {
				return nil, nil, false
			}
		default:
			return nil, nil, false
		}
	}
	if (exit == nil) == (process == nil) {
		return nil, nil, false
	}
	return exit, process, true
}
