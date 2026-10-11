package codex

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/model"
)

func TestCurrentFixtureInventoryNormalizesDurableShapes(t *testing.T) {
	sink, _ := importSource(t, fixtureSource(t, "current_0.145.0.jsonl"), nil, nil)
	got := events(sink.records)
	wantKinds := []model.EventKind{
		model.EventKindSummary,
		model.EventKindToolCall,
		model.EventKindToolResult,
		model.EventKindPatch,
		model.EventKindToolResult,
		model.EventKindToolCall,
		model.EventKindToolCall,
		model.EventKindToolCall,
		model.EventKindToolResult,
		model.EventKindToolCall,
		model.EventKindToolResult,
		model.EventKindToolCall,
		model.EventKindToolResult,
		model.EventKindSummary,
	}
	if len(got) != len(wantKinds) {
		t.Fatalf("events = %d, want %d: %#v", len(got), len(wantKinds), got)
	}
	for index, want := range wantKinds {
		if got[index].Kind != want || got[index].Sequence != int64(index) {
			t.Fatalf("event %d = kind %q sequence %d, want %q/%d", index, got[index].Kind, got[index].Sequence, want, index)
		}
	}
	if got[0].SearchableText != "Inspect the relevant package." {
		t.Fatalf("reasoning summary = %q", got[0].SearchableText)
	}
	if got[0].Data.(model.SummaryData).Category != model.SummaryCategoryReasoning ||
		got[len(got)-1].Data.(model.SummaryData).Category != model.SummaryCategoryPlan {
		t.Fatalf("summary categories = %#v / %#v", got[0].Data, got[len(got)-1].Data)
	}
	for _, event := range got {
		if strings.Contains(event.SearchableText, "opaque-only-sanitized") {
			t.Fatal("opaque encrypted reasoning was indexed")
		}
	}
	dynamicResult := got[10].Data.(model.ToolResultData)
	if dynamicResult.IsError == nil || !*dynamicResult.IsError {
		t.Fatalf("dynamic result error state = %#v", dynamicResult.IsError)
	}
	if len(sink.records[2].Events) != 0 || len(sink.records[9].Events) != 0 ||
		len(sink.records[17].Events) != 0 || len(sink.records[18].Events) != 0 {
		t.Fatal("opaque or transient projection emitted canonical evidence")
	}
}

func TestMalformedKnownAndFutureVariantsStayDistinct(t *testing.T) {
	sink, _ := importSource(t, fixtureSource(t, "malformed_future_0.145.0.jsonl"), nil, nil)
	if len(sink.records) != 8 {
		t.Fatalf("imported records = %d, want 8 before variant checks", len(sink.records))
	}
	for _, index := range []int{1, 2, 3} {
		record := sink.records[index]
		if len(record.Events) != 0 || len(record.Diagnostics) != 1 {
			t.Fatalf("malformed record %d = %#v", index, record)
		}
		want := model.InterpretationStructurallyInvalidKnownRecord
		if index == 1 {
			want = model.InterpretationMissingDiscriminant
		}
		if record.Diagnostics[0].InterpretationReason != want {
			t.Fatalf("malformed record %d reason = %q, want %q", index, record.Diagnostics[0].InterpretationReason, want)
		}
	}
	for index, wantReason := range map[int]model.UnknownReason{
		4: model.UnknownUnsupportedNestedVariant,
		5: model.UnknownUnsupportedNestedVariant,
		6: model.UnknownUnsupportedRecordKind,
	} {
		record := sink.records[index]
		if len(record.Events) != 1 || record.Events[0].Kind != model.EventKindUnknown {
			t.Fatalf("future record %d = %#v", index, record)
		}
		if got := record.Events[0].Data.(model.UnknownData).Reason; got != wantReason {
			t.Fatalf("future record %d reason = %q, want %q", index, got, wantReason)
		}
		if !bytes.Equal(record.RawRecord.Content, mustReadFixtureLine(t, "malformed_future_0.145.0.jsonl", index)) {
			t.Fatalf("future record %d raw bytes changed", index)
		}
	}
	if last := events(sink.records)[len(events(sink.records))-1]; last.Kind != model.EventKindMessage {
		t.Fatalf("normalization did not continue: %#v", last)
	}
}

func TestLegacyNormalizationAvoidsDuplicateMessagesAndPreservesIdentity(t *testing.T) {
	sink, _ := importSource(t, fixtureSource(t, "legacy.jsonl"), nil, nil)
	got := events(sink.records)
	if len(sink.records) != 6 || len(got) != 4 {
		t.Fatalf("records/events = %d/%d, want 6/4", len(sink.records), len(got))
	}
	wantKinds := []model.EventKind{model.EventKindMessage, model.EventKindToolCall, model.EventKindToolResult, model.EventKindMessage}
	for i, want := range wantKinds {
		if got[i].Kind != want {
			t.Fatalf("event %d kind = %q, want %q", i, got[i].Kind, want)
		}
	}
	if sink.session.ID != "01940000-0000-7000-8000-000000000001" {
		t.Fatalf("session ID = %q", sink.session.ID)
	}
	global, _ := model.NewEventID(model.EventIDInput{Native: &model.NativeEventIdentity{Scope: model.NativeEventIDGlobal, EventID: "fc_global_1"}})
	if got[1].ID != global {
		t.Fatalf("global response ID = %q, want %q", got[1].ID, global)
	}
	resultID, _ := model.NewEventID(model.EventIDInput{Native: &model.NativeEventIdentity{Scope: model.NativeEventIDSession, SessionID: string(sink.session.ID), EventID: "function_call_output:call_1"}})
	if got[2].ID != resultID {
		t.Fatalf("qualified call ID = %q, want %q", got[2].ID, resultID)
	}
}

func TestOrdinalNormalizationUsesResponseMessages(t *testing.T) {
	sink, _ := importSource(t, fixtureSource(t, "ordinal.jsonl"), nil, nil)
	got := events(sink.records)
	if len(got) != 3 {
		t.Fatalf("events = %d, want two messages and usage", len(got))
	}
	if got[0].Kind != model.EventKindMessage || got[1].Kind != model.EventKindMessage || got[2].Kind != model.EventKindUsage {
		t.Fatalf("event kinds = %v, %v, %v", got[0].Kind, got[1].Kind, got[2].Kind)
	}
	usage := got[2].Data.(model.UsageData)
	if usage.InputTokens == nil || *usage.InputTokens != 12 || usage.CacheReadTokens == nil || *usage.CacheReadTokens != 3 {
		t.Fatalf("usage = %#v", usage)
	}
	for i, event := range got {
		if event.Sequence != []int64{0, 1, 2}[i] {
			t.Fatalf("event %d sequence = %d", i, event.Sequence)
		}
	}
}

func TestToolOutputContentBlocksAreFlattened(t *testing.T) {
	raw := json.RawMessage(`{"type":"custom_tool_call_output","call_id":"call-1","output":[{"type":"input_text","text":"first line\nsecond line"},{"type":"input_text","text":"third line"}]}`)
	result := normalizeResponseItem(raw, false, "session")
	if !result.handled || len(result.drafts) != 1 || result.drafts[0].kind != model.EventKindToolResult {
		t.Fatalf("normalized result = %#v", result)
	}
	searchable := result.drafts[0].searchable
	data := result.drafts[0].data
	want := "first line\nsecond line\nthird line"
	if searchable != want {
		t.Fatalf("searchable text = %q, want %q", searchable, want)
	}
	toolResult := data.(model.ToolResultData)
	if toolResult.Output != want {
		t.Fatalf("tool output = %q, want %q", toolResult.Output, want)
	}
}

func TestAgentMessagePlaintextAndEncryptedContent(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantDrafts int
	}{
		{
			name:       "plaintext",
			raw:        `{"type":"agent_message","id":"amsg_sanitized","author":"agent-a","recipient":"agent-b","content":[{"type":"input_text","text":"sanitized handoff"}]}`,
			wantDrafts: 1,
		},
		{
			name:       "encrypted",
			raw:        `{"type":"agent_message","id":"amsg_opaque","author":"agent-a","recipient":"agent-b","content":[{"type":"encrypted_content","encrypted_content":"opaque-sanitized"}]}`,
			wantDrafts: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeResponseItem(json.RawMessage(tt.raw), true, "session")
			if !result.handled || len(result.diagnostics) != 0 || len(result.drafts) != tt.wantDrafts {
				t.Fatalf("normalization = %#v", result)
			}
			if len(result.drafts) == 1 {
				if result.drafts[0].kind != model.EventKindMessage ||
					result.drafts[0].searchable != "sanitized handoff" {
					t.Fatalf("plaintext draft = %#v", result.drafts[0])
				}
			}
		})
	}
}
