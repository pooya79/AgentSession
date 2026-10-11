package codex

import (
	"bytes"
	"testing"

	"github.com/pooya79/AgentSession/internal/model"
)

func TestExplicitCommandEvidenceRetainsDistinctLinkedRecords(t *testing.T) {
	sink, _ := importSource(t, fixtureSource(t, "explicit_commands.jsonl"), nil, nil)
	got := events(sink.records)
	if len(got) != 7 {
		t.Fatalf("events = %d, want seven start/result records", len(got))
	}
	again, _ := importSource(t, fixtureSource(t, "explicit_commands.jsonl"), nil, nil)
	for i, event := range got {
		if event.ID != events(again.records)[i].ID || event.Sequence != int64(i) {
			t.Fatal("unstable identity/order")
		}
		if !bytes.Equal(sink.records[i+1].RawRecord.Content, mustReadFixtureLine(t, "explicit_commands.jsonl", i+1)) {
			t.Fatal("raw data changed")
		}
	}
	start := got[0].Data.(model.CommandData)
	end := got[1].Data.(model.CommandData)
	if start.InvocationID != "a" || start.Phase != model.CommandStarted || start.ExitCode != nil || start.WorkingDirectory != "/workspace" {
		t.Fatalf("start = %#v", start)
	}
	if end.InvocationID != "a" || end.Phase != model.CommandCompleted || end.ExitCode == nil || *end.ExitCode != 1 {
		t.Fatalf("end = %#v", end)
	}
	if got[1].ID == got[2].ID {
		t.Fatal("duplicate linked results must remain distinct retained records")
	}
	unknown := got[6].Data.(model.CommandData)
	if unknown.WorkingDirectory != "" || unknown.ExitCode != nil {
		t.Fatalf("missing fields fabricated: %#v", unknown)
	}
}

func TestExplicitCommandTextPreservesArgumentBoundaries(t *testing.T) {
	source := bytesSource("args", []byte(`{"type":"session_meta","payload":{"id":"args"}}
{"type":"event_msg","payload":{"type":"exec_command_begin","call_id":"a","command":["printf","%s","a b"],"cwd":"/workspace"}}
{"type":"event_msg","payload":{"type":"exec_command_begin","call_id":"b","command":["printf","%s","a","b"],"cwd":"/workspace"}}
`))
	sink, _ := importSource(t, source, nil, nil)
	got := events(sink.records)
	if len(got) != 2 {
		t.Fatalf("events=%d", len(got))
	}
	if got[0].Data.(model.CommandData).Command == got[1].Data.(model.CommandData).Command {
		t.Fatal("distinct argument boundaries collapsed into the same command identity")
	}
}

func TestMalformedCommandArgumentsDoNotBecomeKnownCommands(t *testing.T) {
	for _, args := range []string{`null`, `[null]`, `["go",1]`} {
		source := bytesSource("bad-args", []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"bad-args\"}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"exec_command_begin\",\"call_id\":\"a\",\"command\":"+args+",\"cwd\":\"/workspace\"}}\n"))
		sink, _ := importSource(t, source, nil, nil)
		if len(events(sink.records)) != 0 || len(sink.records[1].Diagnostics) != 1 || sink.records[1].Diagnostics[0].InterpretationReason != model.InterpretationStructurallyInvalidKnownRecord {
			t.Fatalf("malformed arguments %s became command evidence", args)
		}
	}
}
