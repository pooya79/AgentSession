package claude

import (
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/model"
)

func TestAgentLogsUseDistinctCanonicalSessionIDs(t *testing.T) {
	first, _ := importSource(t, bytesSource("agent-one", []byte(
		`{"type":"assistant","sessionId":"parent","agentId":"agent-one","message":{"role":"assistant","content":"one"}}`+"\n",
	)), nil, nil)
	second, _ := importSource(t, bytesSource("agent-two", []byte(
		`{"type":"assistant","sessionId":"parent","agentId":"agent-two","message":{"role":"assistant","content":"two"}}`+"\n",
	)), nil, nil)

	if first.session.ID == "parent" || second.session.ID == "parent" || first.session.ID == second.session.ID {
		t.Fatalf("agent session IDs = %q, %q", first.session.ID, second.session.ID)
	}
	if first.session.ID != model.SessionID(agentSessionID("parent", "agent-one")) {
		t.Fatalf("first agent session ID = %q", first.session.ID)
	}
}

// TestMalformedBlocksDoNotSuppressValidSiblingsAndDiagnosticsAreBounded
// verifies partial normalization and the per-record diagnostic cap.
func TestMalformedBlocksDoNotSuppressValidSiblingsAndDiagnosticsAreBounded(t *testing.T) {
	record := `{"type":"assistant","message":{"role":"user","content":[null,{"text":"missing type"},{"type":"text","text":false},{"type":"tool_use","id":"","name":"Read","input":{}},{"type":"text","text":"survives"}],"usage":{"input_tokens":-1,"output_tokens":9}}}`
	sink, _ := importSource(t, bytesSource("mixed-malformed", []byte(record+"\n")), nil, nil)
	got := events(sink.records)
	if len(got) != 2 || got[0].Kind != model.EventKindMessage || got[0].Data.(model.MessageData).Text != "survives" ||
		got[1].Kind != model.EventKindUsage || got[1].Data.(model.UsageData).OutputTokens == nil {
		t.Fatalf("events = %#v", got)
	}
	if len(sink.records[0].Diagnostics) != 6 {
		t.Fatalf("diagnostics = %#v", sink.records[0].Diagnostics)
	}
	for _, diagnostic := range sink.records[0].Diagnostics {
		if len(diagnostic.EventIDs) != 2 || len(diagnostic.RawRecordIDs) != 1 {
			t.Fatalf("diagnostic evidence = %#v", diagnostic)
		}
	}

	var blocks []string
	for index := 0; index < 12; index++ {
		blocks = append(blocks, "null")
	}
	overflow := `{"type":"user","message":{"role":"user","content":[` + strings.Join(blocks, ",") + `]}}`
	bounded, _ := importSource(t, bytesSource("diagnostic-overflow", []byte(overflow+"\n")), nil, nil)
	diagnostics := bounded.records[0].Diagnostics
	if len(diagnostics) != maxRecordDiagnostics || diagnostics[len(diagnostics)-1].Code != "claude.diagnostics.truncated" {
		t.Fatalf("bounded diagnostics = %#v", diagnostics)
	}
}

func TestUUIDIdentityIsSessionScopedAndQualifiedForMultipleEvents(t *testing.T) {
	sink, _ := importSource(t, fixtureSource(t, "main.jsonl"), nil, nil)
	got := events(sink.records)
	single, err := model.NewEventID(model.EventIDInput{Native: &model.NativeEventIdentity{Scope: model.NativeEventIDSession, SessionID: "claude-session-1", EventID: "user-uuid"}})
	if err != nil || got[0].ID != single {
		t.Fatalf("single UUID identity = %q, want %q", got[0].ID, single)
	}
	qualified, _ := model.NewEventID(model.EventIDInput{Native: &model.NativeEventIdentity{Scope: model.NativeEventIDSession, SessionID: "claude-session-1", EventID: "assistant-uuid:event:0"}})
	if got[1].ID != qualified {
		t.Fatalf("qualified UUID identity = %q, want %q", got[1].ID, qualified)
	}
	for _, event := range got[1:5] {
		if event.ID == single {
			t.Fatal("multi-event UUID identities were not disambiguated")
		}
	}
}

func TestFallbackIdentityUsesPerRecordOrdinalsAndPreservesToolError(t *testing.T) {
	data := []byte("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"tool_use_id\":\"failed-tool\",\"content\":\"permission denied\",\"is_error\":true},{\"type\":\"future_block\"}]}}\n")
	sink, _ := importSource(t, bytesSource("fallback-ordinals", data), nil, nil)
	got := events(sink.records)
	if len(got) != 2 {
		t.Fatalf("events = %d, want 2", len(got))
	}
	ref := sink.records[0].RawRecord.Ref
	for ordinal := range got {
		want, err := model.NewEventID(model.EventIDInput{SourceID: ref.SourceID, RecordSequence: ref.RecordSequence, ByteRange: ref.ByteRange, RecordHash: ref.ContentHash, EventOrdinal: uint64(ordinal)})
		if err != nil || got[ordinal].ID != want {
			t.Fatalf("event %d ID = %q, want %q", ordinal, got[ordinal].ID, want)
		}
	}
	result := got[0].Data.(model.ToolResultData)
	if result.IsError == nil || !*result.IsError || result.Output != "permission denied" {
		t.Fatalf("tool error = %#v", result)
	}
}
