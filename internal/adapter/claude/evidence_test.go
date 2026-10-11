package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/pooya79/AgentSession/internal/model"
	"io"
	"os"
	"testing"
)

func TestOrdinaryBashRetainsUnknownProcessResults(t *testing.T) {
	sink, _ := importSource(t, fixtureSource(t, "ordinary_tools.jsonl"), nil, nil)
	var commands []model.CommandData
	for _, e := range events(sink.records) {
		if c, ok := e.Data.(model.CommandData); ok {
			commands = append(commands, c)
		}
	}
	if len(commands) != 6 {
		t.Fatalf("commands = %d, want 6", len(commands))
	}
	for i, c := range commands {
		phase := model.CommandStarted
		if i%2 == 1 {
			phase = model.CommandCompleted
		}
		if c.Phase != phase || c.InvocationID == "" || c.ExitCode != nil {
			t.Fatalf("command %d = %#v", i, c)
		}
		if i%2 == 0 && (c.Command != "go test ./..." || c.WorkingDirectory != "/workspace/project") {
			t.Fatalf("start = %#v", c)
		}
	}
}

func TestOrdinaryEditsAndWritesConfirmRecordedEffects(t *testing.T) {
	sink, _ := importSource(t, fixtureSource(t, "ordinary_tools.jsonl"), nil, nil)
	var patches []model.PatchData
	for _, e := range events(sink.records) {
		if p, ok := e.Data.(model.PatchData); ok {
			patches = append(patches, p)
		}
	}
	if len(patches) != 4 {
		t.Fatalf("patches = %d, want 4", len(patches))
	}
	for i, p := range patches {
		status := model.ChangeRequested
		if i%2 == 1 {
			status = model.ChangeConfirmed
		}
		if p.Status != status || p.ChangeID == "" || len(p.Paths) != 1 {
			t.Fatalf("patch %d = %#v", i, p)
		}
	}
	if patches[1].Changes[0].Operation != model.FileMutationUpdate || patches[3].Changes[0].Operation != model.FileMutationCreate {
		t.Fatalf("effects = %#v", patches)
	}
}

func TestOrdinaryChangeUncertaintyAndNoOps(t *testing.T) {
	source := fixtureSource(t, "ordinary_tools.jsonl")
	stream, err := source.OpenFrom(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(stream)
	stream.Close()
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	tests := []struct {
		name            string
		request, result []byte
		status          model.ChangeStatus
	}{
		{"confirmed edit", lines[0], lines[1], model.ChangeConfirmed},
		{"failed edit with partial patch", lines[0], bytes.Replace(lines[1], []byte(`"is_error":false`), []byte(`"is_error":true`), 1), model.ChangeUnknown},
		{"path mismatch", lines[0], bytes.ReplaceAll(lines[1], []byte("main.go"), []byte("other.go")), model.ChangeUnknown},
		{"malformed patch", lines[0], bytes.Replace(lines[1], []byte(`"oldLines":1`), []byte(`"oldLines":2`), 1), model.ChangeUnknown},
		{"partial patch", lines[0], bytes.Replace(lines[1], []byte(`"-package old","+package main"`), []byte(`"-package old"`), 1), model.ChangeUnknown},
		{"missing patch", lines[0], bytes.Replace(lines[1], []byte(`"structuredPatch"`), []byte(`"unknownPatch"`), 1), model.ChangeUnknown},
		{"empty patch", lines[0], bytes.Replace(lines[1], []byte(`[{"oldStart":1,"oldLines":1,"newStart":1,"newLines":1,"lines":["-package old","+package main"]}]`), []byte(`[]`), 1), model.ChangeUnknown},
		{"null original", lines[0], bytes.Replace(lines[1], []byte(`"originalFile":"package old\n"`), []byte(`"originalFile":null`), 1), model.ChangeUnknown},
		{"user modified", lines[0], bytes.Replace(lines[1], []byte(`"userModified":false`), []byte(`"userModified":true`), 1), model.ChangeUnknown},
		{"different effect", lines[0], bytes.Replace(lines[1], []byte(`"newString":"package main"`), []byte(`"newString":"package other"`), 1), model.ChangeUnknown},
		{"confirmed create", lines[8], lines[9], model.ChangeConfirmed},
		{"create contradicts original", lines[8], bytes.Replace(lines[9], []byte(`"originalFile":null`), []byte(`"originalFile":"already here"`), 1), model.ChangeUnknown},
		{"unsupported write type", lines[8], bytes.Replace(lines[9], []byte(`"type":"create"`), []byte(`"type":"future"`), 1), model.ChangeUnknown},
		{"unconfirmed prose", lines[8], bytes.Replace(lines[9], []byte(`"toolUseResult"`), []byte(`"unused"`), 1), model.ChangeUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sink, _ := importSource(t, bytesSource(tc.name, append(append([]byte(nil), tc.request...), tc.result...)), nil, nil)
			got := events(sink.records)
			if len(got) != 2 {
				t.Fatalf("events = %#v", got)
			}
			p, ok := got[1].Data.(model.PatchData)
			if !ok || p.Status != tc.status {
				t.Fatalf("result = %#v, want %s", got[1].Data, tc.status)
			}
		})
	}
}

func TestOrdinaryNoOpAndWriteUpdate(t *testing.T) {
	data, err := os.ReadFile("testdata/ordinary_tools.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	noOpRequest := bytes.Replace(lines[0], []byte(`"new_string":"package main"`), []byte(`"new_string":"package old"`), 1)
	noOpResult := bytes.Replace(lines[1], []byte(`"newString":"package main"`), []byte(`"newString":"package old"`), 1)
	noOpResult = bytes.Replace(noOpResult, []byte(`[{"oldStart":1,"oldLines":1,"newStart":1,"newLines":1,"lines":["-package old","+package main"]}]`), []byte(`[]`), 1)
	updateResult := bytes.Replace(lines[9], []byte(`"type":"create"`), []byte(`"type":"update"`), 1)
	updateResult = bytes.Replace(updateResult, []byte(`"originalFile":null`), []byte(`"originalFile":"package old\n"`), 1)
	updateResult = bytes.Replace(updateResult, []byte(`"structuredPatch":[]`), []byte(`"structuredPatch":[{"oldStart":1,"oldLines":1,"newStart":1,"newLines":1,"lines":["-package old","+package main"]}]`), 1)
	for _, tc := range []struct {
		name            string
		request, result []byte
		status          model.ChangeStatus
	}{
		{"edit no-op", noOpRequest, noOpResult, model.ChangeNoOp},
		{"write update", lines[8], updateResult, model.ChangeConfirmed},
		{"malformed create", lines[8], bytes.Replace(lines[9], []byte(`"structuredPatch":[]`), []byte(`"structuredPatch":null`), 1), model.ChangeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink, _ := importSource(t, bytesSource(tc.name, append(append([]byte(nil), tc.request...), tc.result...)), nil, nil)
			p := events(sink.records)[1].Data.(model.PatchData)
			if p.Status != tc.status {
				t.Fatalf("result=%#v, want %s", p, tc.status)
			}
		})
	}
}

func TestOrdinaryUnsupportedAndAmbiguousBashResults(t *testing.T) {
	data, err := os.ReadFile("testdata/ordinary_tools.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	tests := []struct {
		name            string
		request, result []byte
	}{
		{"hosted", bytes.Replace(lines[2], []byte(`"type":"tool_use"`), []byte(`"type":"server_tool_use"`), 1), lines[3]},
		{"custom", bytes.Replace(lines[2], []byte(`"name":"Bash"`), []byte(`"name":"mcp__Bash"`), 1), lines[3]},
		{"background request", bytes.Replace(lines[2], []byte(`"command":"go test ./..."`), []byte(`"command":"go test ./...","run_in_background":true`), 1), lines[3]},
		{"background", lines[2], bytes.Replace(lines[3], []byte(`"isImage":false`), []byte(`"isImage":false,"backgroundTaskId":"task-1"`), 1)},
		{"interrupted", lines[2], bytes.Replace(lines[3], []byte(`"interrupted":false`), []byte(`"interrupted":true`), 1)},
		{"malformed", lines[2], bytes.Replace(lines[3], []byte(`"interrupted":false`), []byte(`"interrupted":"false"`), 1)},
		{"missing structured result", lines[2], bytes.Replace(lines[3], []byte(`"toolUseResult"`), []byte(`"unused"`), 1)},
		{"multiple results", lines[2], bytes.Replace(lines[3], []byte(`"is_error":true}]`), []byte(`"is_error":true},{"type":"tool_result","tool_use_id":"other","content":"other"}]`), 1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sink, _ := importSource(t, bytesSource(tc.name, append(append([]byte(nil), tc.request...), tc.result...)), nil, nil)
			for _, e := range events(sink.records) {
				if c, ok := e.Data.(model.CommandData); ok && c.Phase == model.CommandCompleted {
					t.Fatalf("unsupported completion = %#v", c)
				}
			}
		})
	}
	for _, cwd := range []string{"", "relative", "/workspace/../project", `C:\project`} {
		t.Run("directory:"+cwd, func(t *testing.T) {
			var record map[string]any
			if err := json.Unmarshal(lines[2], &record); err != nil {
				t.Fatal(err)
			}
			record["cwd"] = cwd
			raw, _ := json.Marshal(record)
			sink, _ := importSource(t, bytesSource("directory", append(raw, '\n')), nil, nil)
			if c := events(sink.records)[0].Data.(model.CommandData); c.WorkingDirectory != "" {
				t.Fatalf("ambiguous directory=%#v", c)
			}
		})
	}
}

func TestOrdinaryRelativeChangesRequireMatchingRecordedBases(t *testing.T) {
	data, err := os.ReadFile("testdata/ordinary_tools.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	request := bytes.ReplaceAll(lines[0], []byte("/workspace/project/main.go"), []byte("main.go"))
	result := bytes.ReplaceAll(lines[1], []byte("/workspace/project/main.go"), []byte("main.go"))
	for _, tc := range []struct {
		name   string
		result []byte
		status model.ChangeStatus
	}{
		{"same base", result, model.ChangeConfirmed},
		{"conflicting base", bytes.ReplaceAll(result, []byte("/workspace/project"), []byte("/other/project")), model.ChangeUnknown},
		{"missing base", bytes.ReplaceAll(result, []byte(`"cwd":"/workspace/project",`), nil), model.ChangeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink, _ := importSource(t, bytesSource(tc.name, append(append([]byte(nil), request...), tc.result...)), nil, nil)
			if p := events(sink.records)[1].Data.(model.PatchData); p.Status != tc.status {
				t.Fatalf("result = %#v, want %s", p, tc.status)
			}
		})
	}
}

func TestOrdinaryWriteDoesNotConfirmUnrepresentedTerminalNewlineChange(t *testing.T) {
	data, err := os.ReadFile("testdata/ordinary_tools.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	result := bytes.Replace(lines[9], []byte(`"type":"create"`), []byte(`"type":"update"`), 1)
	result = bytes.Replace(result, []byte(`"originalFile":null`), []byte(`"originalFile":"package main"`), 1)
	result = bytes.Replace(result, []byte(`"structuredPatch":[]`), []byte(`"structuredPatch":[{"oldStart":1,"oldLines":1,"newStart":1,"newLines":1,"lines":[" package main"]}]`), 1)
	sink, _ := importSource(t, bytesSource("newline", append(append([]byte(nil), lines[8]...), result...)), nil, nil)
	if p := events(sink.records)[1].Data.(model.PatchData); p.Status != model.ChangeUnknown {
		t.Fatalf("unrepresented mutation = %#v", p)
	}
}

func TestOrdinaryChangesDoNotBorrowUnsupportedVariantSemantics(t *testing.T) {
	data, err := os.ReadFile("testdata/ordinary_tools.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	mixed := bytes.Replace(lines[0], []byte(`"content":[`), []byte(`"content":[{"type":"server_tool_use","id":"edit-1","name":"Edit","input":{"file_path":"/workspace/project/main.go","old_string":"package old","new_string":"package main"}},`), 1)
	for _, tc := range []struct {
		name            string
		request, result []byte
	}{
		{"mixed hosted call ID", mixed, lines[1]},
		{"unsupported effect status", lines[0], bytes.Replace(lines[1], []byte(`"filePath"`), []byte(`"status":"partial","filePath"`), 1)},
		{"null error flag", lines[0], bytes.Replace(lines[1], []byte(`"is_error":false`), []byte(`"is_error":null`), 1)},
		{"different named result", lines[0], bytes.Replace(lines[1], []byte(`"is_error":false`), []byte(`"is_error":false,"name":"CustomEdit"`), 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink, _ := importSource(t, bytesSource(tc.name, append(append([]byte(nil), tc.request...), tc.result...)), nil, nil)
			for _, e := range events(sink.records) {
				if p, ok := e.Data.(model.PatchData); ok && p.Status == model.ChangeConfirmed {
					t.Fatalf("unsupported confirmation = %#v", p)
				}
			}
		})
	}
}
