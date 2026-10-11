package codex

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/storage/sqlite"
)

func TestStreamedCommandCompletionResumesAtEveryRecord(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "streamed_commands.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	full, _ := importSource(t, bytesSource("streamed", data), nil, nil)
	var commands []model.CommandData
	for _, event := range events(full.records) {
		if command, ok := event.Data.(model.CommandData); ok {
			commands = append(commands, command)
		}
	}
	if len(commands) != 8 {
		t.Fatalf("command records = %d, want four starts and four results", len(commands))
	}
	if commands[0].Command != "go test ./..." || commands[0].WorkingDirectory != "/workspace/sanitized" || commands[0].InvocationID != "first" || commands[0].Phase != model.CommandStarted {
		t.Fatalf("start = %#v", commands[0])
	}
	for _, i := range []int{1, 2} {
		if commands[i].InvocationID != "first" || commands[i].Phase != model.CommandCompleted || commands[i].ExitCode == nil || *commands[i].ExitCode != 1 {
			t.Fatalf("linked result = %#v", commands[i])
		}
	}
	if commands[6].InvocationID != "recovery" || commands[6].ExitCode == nil || *commands[6].ExitCode != 0 || commands[7].InvocationID != "pending" || commands[7].ExitCode != nil {
		t.Fatalf("recovery/pending = %#v", commands[6:])
	}
	for i, record := range full.records {
		if !bytes.Equal(record.RawRecord.Content, mustReadFixtureLine(t, "streamed_commands.jsonl", i)) {
			t.Fatal("raw evidence changed")
		}
	}
	assertCommandResumeAtEveryRecord(t, data, "streamed")
}

func TestRecordedCommandFormsAndDirectories(t *testing.T) {
	sink, _ := importSource(t, fixtureSource(t, "command_forms.jsonl"), nil, nil)
	got := events(sink.records)
	if len(got) != 10 {
		t.Fatalf("events = %d", len(got))
	}
	for i, want := range []struct{ command, directory, invocation string }{
		{"go test ./...", "/workspace/sanitized", "argv"},
		{"go test ./...", "/workspace/next/sub", "text"},
		{"printf %s 'a b'", "/workspace/local", "local"},
		{"go vet ./...", "/workspace/container", "container"},
	} {
		start, ok := got[i*2].Data.(model.CommandData)
		if !ok || start.Command != want.command || start.WorkingDirectory != want.directory || start.InvocationID != want.invocation || start.Phase != model.CommandStarted {
			t.Fatalf("start %d = %#v", i, got[i*2])
		}
		end, ok := got[i*2+1].Data.(model.CommandData)
		if !ok || end.InvocationID != want.invocation || end.ExitCode == nil {
			t.Fatalf("result %d = %#v", i, got[i*2+1])
		}
	}
	if got[8].Kind != model.EventKindToolCall || got[9].Kind != model.EventKindToolResult {
		t.Fatal("unestablished custom grammar became process evidence")
	}
}

func TestMirroredCommandRecordsKeepOneIdentity(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "mirrored_commands.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sink, _ := importSource(t, bytesSource("mirrors", data), nil, nil)
	var starts, results int
	for _, e := range events(sink.records) {
		command, ok := e.Data.(model.CommandData)
		if !ok {
			continue
		}
		if command.InvocationID != "a" {
			t.Fatalf("poll became independent command: %#v", command)
		}
		if command.Command != "" && command.Command != "go test ./..." {
			t.Fatalf("mirrored command identity differs: %#v", command)
		}
		if command.Phase == model.CommandStarted {
			starts++
		} else {
			results++
		}
	}
	if starts != 2 || results != 2 {
		t.Fatalf("starts/results = %d/%d", starts, results)
	}
	assertCommandResumeAtEveryRecord(t, data, "mirrors")
}

func assertCommandResumeAtEveryRecord(t *testing.T, data []byte, sourceID string) {
	t.Helper()
	full, _ := importSource(t, bytesSource(sourceID, data), nil, nil)
	lines := bytes.SplitAfter(data, []byte{'\n'})
	for split := 1; split < len(lines)-1; split++ {
		prefix := bytes.Join(lines[:split], nil)
		first, _ := importSource(t, bytesSource(sourceID, prefix), nil, nil)
		state := sourceState(first)
		second, change := importSource(t, bytesSource(sourceID, data), &state.Checkpoint, &state)
		if change != importer.SourceAppend {
			t.Fatalf("split %d: change = %s", split, change)
		}
		combined := append(first.records, second.records...)
		if !reflect.DeepEqual(combined, full.records) {
			t.Fatalf("split %d differs from clean import", split)
		}
	}
}

func TestUncertainCommandFormsDoNotFabricateExitsOrContext(t *testing.T) {
	sink, _ := importSource(t, fixtureSource(t, "uncertain_commands.jsonl"), nil, nil)
	for _, record := range sink.records {
		for _, e := range record.Events {
			if command, ok := e.Data.(model.CommandData); ok {
				if command.InvocationID != "conflict" && command.ExitCode != nil {
					t.Fatalf("fabricated exit = %#v", command)
				}
				if (command.InvocationID == "early" || command.InvocationID == "remote") && command.WorkingDirectory != "" {
					t.Fatalf("fabricated directory = %#v", command)
				}
				if command.InvocationID == "orphan" {
					t.Fatal("unlinked poll became an invocation")
				}
			}
		}
	}
	if len(sink.records[13].Diagnostics) != 1 || len(sink.records[15].Diagnostics) != 1 || sink.records[14].Events[0].Kind != model.EventKindUnknown {
		t.Fatal("malformed/unknown evidence lost")
	}
}

func TestLargeStreamedResultRetainsRawAndNormalizedEvidence(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "large.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := sqlite.NewImportStore(db)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := importer.NewCoordinator(store, []importer.Adapter{New()}, nil, importer.Options{BatchRecords: 1})
	if err != nil {
		t.Fatal(err)
	}
	text := "Wall time: 0.1000 seconds\nProcess exited with code 1\nOutput:\n" + strings.Repeat("sanitized output\n", 1<<17) + "Process exited with code 0"
	data := []byte(fmt.Sprintf("{\"type\":\"session_meta\",\"payload\":{\"id\":\"large-streamed\",\"cwd\":\"/workspace\"}}\n{\"type\":\"response_item\",\"payload\":{\"type\":\"function_call\",\"call_id\":\"a\",\"name\":\"exec_command\",\"arguments\":\"{\\\"cmd\\\":\\\"go test ./...\\\"}\"}}\n{\"type\":\"response_item\",\"payload\":{\"type\":\"function_call_output\",\"call_id\":\"a\",\"output\":%q}}\n", text))
	result, err := coordinator.Import(ctx, bytesSource("large-streamed", data))
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(ctx, result.SessionID)
	if err != nil || len(events) != 2 {
		t.Fatalf("events = %d, %v", len(events), err)
	}
	command := events[1].Data.(model.CommandData)
	if command.ExitCode == nil || *command.ExitCode != 1 || command.Output != text {
		t.Fatal("large result truncated or output body interpreted as an exit")
	}
	raw, found, err := store.RawRecord(ctx, events[1].RawRecord.ID)
	lines := bytes.SplitAfter(data, []byte{'\n'})
	if err != nil || !found || !bytes.Equal(raw.Content, lines[2]) {
		t.Fatal("large raw result changed")
	}
}

func TestCommandCorrelationLimitRetainsUncertainEvidence(t *testing.T) {
	var source strings.Builder
	source.WriteString("{\"type\":\"session_meta\",\"payload\":{\"id\":\"bounded\",\"cwd\":\"/workspace\"}}\n")
	for i := 0; i <= 4096; i++ {
		fmt.Fprintf(&source, "{\"type\":\"response_item\",\"payload\":{\"type\":\"function_call\",\"call_id\":\"call-%d\",\"name\":\"exec_command\",\"arguments\":\"{\\\"cmd\\\":\\\"false\\\"}\"}}\n", i)
	}
	source.WriteString("{\"type\":\"response_item\",\"payload\":{\"type\":\"function_call_output\",\"call_id\":\"call-4096\",\"output\":\"Wall time: 0.1 seconds\\nProcess exited with code 0\\nOutput:\\nok\"}}\n")
	sink, _ := importSource(t, bytesSource("bounded", []byte(source.String())), nil, nil)
	got := events(sink.records)
	if len(got) != 4098 || got[4096].Kind != model.EventKindToolCall || got[4097].Kind != model.EventKindToolResult {
		t.Fatal("correlation limit silently discarded or guessed command evidence")
	}
}

func TestUnestablishedCommandResultsRemainUnknown(t *testing.T) {
	tests := []struct{ name, record string }{
		{"declined", `{"type":"event_msg","payload":{"type":"exec_command_end","call_id":"a","exit_code":-1,"status":"declined"}}`},
		{"cancelled", `{"type":"event_msg","payload":{"type":"exec_command_end","call_id":"a","exit_code":0,"status":"cancelled"}}`},
		{"unknown status", `{"type":"event_msg","payload":{"type":"exec_command_end","call_id":"a","exit_code":0,"status":"future"}}`},
		{"inconsistent header", `{"type":"response_item","payload":{"type":"function_call_output","call_id":"a","output":"Wall time: 0.1 seconds\nProcess exited with code 0\nProcess exited with code 1\nOutput:\nok"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"unestablished\",\"cwd\":\"/workspace\"}}\n{\"type\":\"response_item\",\"payload\":{\"type\":\"function_call\",\"name\":\"exec_command\",\"call_id\":\"a\",\"arguments\":\"{\\\"cmd\\\":\\\"go test ./...\\\"}\"}}\n" + tt.record + "\n")
			sink, _ := importSource(t, bytesSource("unestablished", data), nil, nil)
			for _, e := range events(sink.records) {
				if c, ok := e.Data.(model.CommandData); ok && c.ExitCode != nil {
					t.Fatalf("fabricated result = %#v", c)
				}
			}
		})
	}
}

func TestStandaloneShellCommandsKeepDistinctIdentity(t *testing.T) {
	data := []byte(`{"type":"session_meta","payload":{"id":"distinct-shells","cwd":"/workspace"}}
{"type":"event_msg","payload":{"type":"exec_command_begin","call_id":"sh","command":["/bin/sh","-c","false"],"cwd":"/workspace"}}
{"type":"event_msg","payload":{"type":"exec_command_end","call_id":"sh","command":["/bin/sh","-c","false"],"cwd":"/workspace","exit_code":1}}
{"type":"event_msg","payload":{"type":"exec_command_begin","call_id":"bash","command":["/bin/bash","-lc","false"],"cwd":"/workspace"}}
{"type":"event_msg","payload":{"type":"exec_command_end","call_id":"bash","command":["/bin/bash","-lc","false"],"cwd":"/workspace","exit_code":1}}
`)
	sink, _ := importSource(t, bytesSource("distinct-shells", data), nil, nil)
	got := events(sink.records)
	if got[0].Data.(model.CommandData).Command != "/bin/sh -c false" || got[2].Data.(model.CommandData).Command != "/bin/bash -lc false" {
		t.Fatal("different recorded shell executables/options collapsed into one command")
	}
}
