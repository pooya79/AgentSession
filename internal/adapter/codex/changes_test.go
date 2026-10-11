package codex

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/model"
)

func TestConfirmedPatchEvidence(t *testing.T) {
	sink, _ := importSource(t, fixtureSource(t, "changes.jsonl"), nil, nil)
	var patches []model.PatchData
	for _, event := range events(sink.records) {
		if p, ok := event.Data.(model.PatchData); ok {
			patches = append(patches, p)
		}
	}
	if len(patches) != 3 || patches[0].Status != model.ChangeRequested || patches[1].Status != model.ChangeConfirmed || patches[2].Status != model.ChangeNoOp {
		t.Fatalf("patches = %#v", patches)
	}
	got := patches[1]
	if got.ChangeID != "edit" || got.WorkingDirectory != "/workspace" || len(got.Changes) != 1 || got.Changes[0].Path != "internal/core/core.go" || got.Changes[0].Operation != model.FileMutationUpdate {
		t.Fatalf("confirmed = %#v", got)
	}
}

func TestPatchConfirmationLimits(t *testing.T) {
	tests := []struct {
		name, status string
		success      bool
		changes      string
		want         model.ChangeStatus
	}{
		{"failed", "failed", false, `{}`, model.ChangeFailed},
		{"declined", "declined", false, `{}`, model.ChangeFailed},
		{"contradictory status", "failed", true, `{"core.go":{"type":"add","content":"x"}}`, model.ChangeUnknown},
		{"missing paths", "completed", true, `null`, model.ChangeUnknown},
		{"unknown change kind", "completed", true, `{"core.go":{"type":"future"}}`, model.ChangeUnknown},
		{"malformed known change", "completed", true, `{"core.go":{"type":"add","content":3}}`, model.ChangeUnknown},
		{"empty update", "completed", true, `{"core.go":{"type":"update","unified_diff":"","move_path":null}}`, model.ChangeNoOp},
		{"context only", "completed", true, `{"core.go":{"type":"update","unified_diff":"@@ -1 +1 @@\n context","move_path":null}}`, model.ChangeUnknown},
		{"added content resembling header", "completed", true, `{"core.go":{"type":"update","unified_diff":"@@ -1 +1,2 @@\n context\n+++ note","move_path":null}}`, model.ChangeConfirmed},
		{"deleted content resembling header", "completed", true, `{"core.go":{"type":"update","unified_diff":"@@ -1,2 +1 @@\n context\n--- note","move_path":null}}`, model.ChangeConfirmed},
		{"malformed nonempty diff", "completed", true, `{"core.go":{"type":"update","unified_diff":"@@garbage","move_path":null}}`, model.ChangeUnknown},
		{"move", "completed", true, `{"core.go":{"type":"update","unified_diff":"","move_path":"next.go"}}`, model.ChangeConfirmed},
		{"empty created file", "completed", true, `{"core.go":{"type":"add","content":""}}`, model.ChangeConfirmed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var changes any
			if err := json.Unmarshal([]byte(tt.changes), &changes); err != nil {
				t.Fatal(err)
			}
			payload := map[string]any{"type": "patch_apply_end", "call_id": "edit", "stdout": "Success. Updated files: core.go", "stderr": "", "success": tt.success, "status": tt.status, "changes": changes}
			record, _ := json.Marshal(map[string]any{"type": "event_msg", "payload": payload})
			data := append([]byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"patch-limits\",\"cwd\":\"/workspace\"}}\n"), append(record, '\n')...)
			sink, _ := importSource(t, bytesSource("limits", data), nil, nil)
			got := events(sink.records)
			if len(got) != 1 {
				t.Fatalf("events = %#v", got)
			}
			patch := got[0].Data.(model.PatchData)
			if patch.Status != tt.want {
				t.Fatalf("patch = %#v", patch)
			}
			if tt.name == "move" && (patch.Changes[0].PreviousPath != "core.go" || patch.Changes[0].Path != "next.go") {
				t.Fatalf("move = %#v", patch)
			}
			if !bytes.Equal(sink.records[1].RawRecord.Content, append(record, '\n')) {
				t.Fatal("raw result changed")
			}
		})
	}
}

func TestPatchContextResumesFromVerifiedPrefix(t *testing.T) {
	source := fixtureSource(t, "changes.jsonl")
	full, _ := importSource(t, source, nil, nil)
	prefix := append(mustReadFixtureLine(t, "changes.jsonl", 0), mustReadFixtureLine(t, "changes.jsonl", 1)...)
	partial := mustReadFixtureLine(t, "changes.jsonl", 2)
	prefix = append(prefix, partial[:len(partial)/2]...)
	first, _ := importSource(t, bytesSource(string(source.ID), prefix), nil, nil)
	state := sourceState(first)
	resumed, _ := importSource(t, source, &first.checkpoint, &state)
	combined := append(events(first.records), events(resumed.records)...)
	if !reflect.DeepEqual(events(full.records), combined) {
		t.Fatal("resume changed canonical evidence or identities")
	}
	// Large structured paths remain canonical evidence; analysis owns its bounds.
	line := strings.Replace(string(mustReadFixtureLine(t, "changes.jsonl", 2)), "internal/core/core.go", strings.Repeat("a", 5000)+".go", 1)
	large := append(mustReadFixtureLine(t, "changes.jsonl", 0), []byte(line)...)
	sink, _ := importSource(t, bytesSource("large", large), nil, nil)
	patch := events(sink.records)[0].Data.(model.PatchData)
	if len(patch.Changes[0].Path) != 5003 || patch.Status != model.ChangeConfirmed {
		t.Fatal("large canonical path lost")
	}
}
