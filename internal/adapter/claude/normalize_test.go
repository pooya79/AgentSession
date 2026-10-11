package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/model"
)

func TestNullMessagesAreRetainedAsMalformedDiagnostics(t *testing.T) {
	tests := map[string]string{
		"message": `{"type":"user","message":null}`,
		"content": `{"type":"assistant","message":{"role":"assistant","content":null}}`,
	}
	for name, record := range tests {
		t.Run(name, func(t *testing.T) {
			sink, _ := importSource(t, bytesSource(name, []byte(record+"\n")), nil, nil)
			if len(sink.records) != 1 || len(sink.records[0].Events) != 0 ||
				len(sink.records[0].Diagnostics) != 1 ||
				sink.records[0].Diagnostics[0].InterpretationReason != model.InterpretationStructurallyInvalidKnownRecord {
				t.Fatalf("record = %#v, want retained malformed diagnostic", sink.records)
			}
		})
	}
}

func TestNullContentBlockLinksEarlierEventsToMalformedDiagnostic(t *testing.T) {
	record := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"before null"},null]}}`
	sink, _ := importSource(t, bytesSource("null-block", []byte(record+"\n")), nil, nil)
	if len(sink.records) != 1 || len(sink.records[0].Events) != 1 || len(sink.records[0].Diagnostics) != 1 {
		t.Fatalf("record = %#v, want one event and one diagnostic", sink.records)
	}
	diagnostic := sink.records[0].Diagnostics[0]
	if diagnostic.InterpretationReason != model.InterpretationStructurallyInvalidKnownRecord ||
		len(diagnostic.EventIDs) != 1 || diagnostic.EventIDs[0] != sink.records[0].Events[0].ID {
		t.Fatalf("diagnostic = %#v, want malformed reason linked to emitted event", diagnostic)
	}
}

func TestMainThreadNormalizationRetainsRecordsAndUsesIndependentSequences(t *testing.T) {
	sink, _ := importSource(t, fixtureSource(t, "main.jsonl"), nil, nil)
	got := events(sink.records)
	if len(sink.records) != 5 || len(got) != 7 {
		t.Fatalf("records/events = %d/%d, want 5/7", len(sink.records), len(got))
	}
	wantKinds := []model.EventKind{
		model.EventKindMessage,
		model.EventKindMessage, model.EventKindToolCall, model.EventKindUnknown, model.EventKindUsage,
		model.EventKindToolResult,
		model.EventKindSummary,
	}
	if len(got) != len(wantKinds) {
		t.Fatalf("event count = %d, want %d", len(got), len(wantKinds))
	}
	for i, event := range got {
		if event.Kind != wantKinds[i] || event.Sequence != int64(i) {
			t.Fatalf("event %d = kind %q sequence %d", i, event.Kind, event.Sequence)
		}
	}
	if len(sink.records[0].Events) != 0 {
		t.Fatal("file history snapshot should be retained without an event")
	}
	call := got[2].Data.(model.ToolCallData)
	if call.CallID != "tool-1" || call.ToolName != "Read" || call.Input != `{"file_path":"/workspace/example.go"}` {
		t.Fatalf("tool call = %#v", call)
	}
	usage := got[4].Data.(model.UsageData)
	if usage.InputTokens == nil || *usage.InputTokens != 14 || usage.CacheWriteTokens == nil || *usage.CacheWriteTokens != 3 || usage.CacheReadTokens == nil || *usage.CacheReadTokens != 5 || usage.OutputTokens == nil || *usage.OutputTokens != 7 {
		t.Fatalf("usage = %#v", usage)
	}
	result := got[5].Data.(model.ToolResultData)
	if result.CallID != "tool-1" || result.Output != "package example\nfunc Example() {}" || result.IsError == nil || *result.IsError {
		t.Fatalf("tool result = %#v", result)
	}
	if summary := got[6].Data.(model.SummaryData); summary.Text != "The sanitized fixture was inspected." ||
		summary.Category != model.SummaryCategorySummary {
		t.Fatalf("summary = %#v", got[6].Data)
	}
}

func TestToolInputPreservesExactJSONNumbers(t *testing.T) {
	data := []byte(`{"type":"assistant","sessionId":"exact-numbers","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Lookup","input":{"id":9007199254740993,"fraction":1.2300,"nested":[18446744073709551615]}}]}}` + "\n")
	sink, _ := importSource(t, bytesSource("exact-numbers", data), nil, nil)
	got := events(sink.records)
	if len(got) != 1 {
		t.Fatalf("events = %d, want 1", len(got))
	}
	call := got[0].Data.(model.ToolCallData)
	want := `{"id":9007199254740993,"fraction":1.2300,"nested":[18446744073709551615]}`
	if call.Input != want || got[0].SearchableText != want {
		t.Fatalf("tool input/searchable = %q/%q, want %q", call.Input, got[0].SearchableText, want)
	}
}

// TestObservedRecordClassification verifies classifications derived from
// sanitized locally observed Claude record shapes.
func TestObservedRecordClassification(t *testing.T) {
	sink, _ := importSource(t, fixtureSource(t, "observed.jsonl"), nil, nil)
	got := events(sink.records)
	wantKinds := []model.EventKind{
		model.EventKindMessage,
		model.EventKindSummary,
		model.EventKindSummary,
		model.EventKindError,
		model.EventKindMessage,
		model.EventKindMessage,
	}
	if len(sink.records) != 18 || len(got) != len(wantKinds) {
		t.Fatalf("records/events = %d/%d, want 18/%d", len(sink.records), len(got), len(wantKinds))
	}
	for index, want := range wantKinds {
		if got[index].Kind != want {
			t.Fatalf("event %d kind = %q, want %q", index, got[index].Kind, want)
		}
	}
	if !strings.Contains(got[0].Summary, "sidechain") {
		t.Fatalf("sidechain summary = %q", got[0].Summary)
	}
	if got[1].Data.(model.SummaryData).Category != model.SummaryCategorySummary ||
		got[2].Data.(model.SummaryData).Category != model.SummaryCategoryContext {
		t.Fatalf("observed summary categories = %#v / %#v", got[1].Data, got[2].Data)
	}
	for index := 6; index < len(sink.records); index++ {
		if len(sink.records[index].Events) != 0 || len(sink.records[index].Diagnostics) != 0 {
			t.Fatalf("metadata record %d = %#v", index, sink.records[index])
		}
	}
}

// TestOfficialServerBlocksAndOpaqueBlocks verifies documented server-tool
// mappings and the search boundary for opaque evidence.
func TestOfficialServerBlocksAndOpaqueBlocks(t *testing.T) {
	sink, _ := importSource(t, fixtureSource(t, "official_blocks.jsonl"), nil, nil)
	got := events(sink.records)
	if len(got) != 15 {
		t.Fatalf("events = %d, want 15", len(got))
	}
	call, ok := got[0].Data.(model.ToolCallData)
	if !ok || call.CallID != "srv-1" || call.ToolName != "web_search" || call.Input != `{"query":"sanitized"}` {
		t.Fatalf("server tool call = %#v", got[0].Data)
	}
	for index := 1; index <= 6; index++ {
		unknown, ok := got[index].Data.(model.UnknownData)
		if !ok || unknown.Reason != model.UnknownUnsupportedNestedVariant {
			t.Fatalf("opaque block %d = %#v", index, got[index])
		}
		if strings.Contains(got[index].SearchableText, "not searchable") ||
			strings.Contains(got[index].SearchableText, "opaque encrypted fixture") {
			t.Fatalf("opaque payload leaked into search: %q", got[index].SearchableText)
		}
	}
	for index := 7; index < len(got); index++ {
		result, ok := got[index].Data.(model.ToolResultData)
		if !ok || result.CallID == "" || !json.Valid([]byte(result.Output)) {
			t.Fatalf("structured result %d = %#v", index, got[index].Data)
		}
	}
	if !bytes.Contains(sink.records[0].RawRecord.Content, []byte("opaque encrypted fixture")) ||
		!bytes.Contains(sink.records[0].RawRecord.Content, []byte("not searchable")) {
		t.Fatal("opaque evidence was not preserved in the raw record")
	}
}

// TestNestedMetadataUnknownAndMalformedClassification distinguishes future
// valid variants from malformed known envelopes.
func TestNestedMetadataUnknownAndMalformedClassification(t *testing.T) {
	tests := []struct {
		name       string
		record     string
		wantKind   model.EventKind
		wantReason model.InterpretationReason
	}{
		{"unknown system", `{"type":"system","subtype":"future_system"}`, model.EventKindUnknown, ""},
		{"unknown progress", `{"type":"progress","data":{"type":"future_progress"}}`, model.EventKindUnknown, ""},
		{"unknown queue", `{"type":"queue-operation","operation":"future_operation"}`, model.EventKindUnknown, ""},
		{"unknown attachment", `{"type":"attachment","subtype":"future_attachment"}`, model.EventKindUnknown, ""},
		{"unknown attachment object", `{"type":"attachment","attachment":{"type":"future_attachment"}}`, model.EventKindUnknown, ""},
		{"missing system subtype", `{"type":"system","content":"text"}`, "", model.InterpretationMissingDiscriminant},
		{"missing progress subtype", `{"type":"progress","data":{}}`, "", model.InterpretationMissingDiscriminant},
		{"invalid queue operation", `{"type":"queue-operation","operation":false}`, "", model.InterpretationStructurallyInvalidKnownRecord},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink, _ := importSource(t, bytesSource(tt.name, []byte(tt.record+"\n")), nil, nil)
			if tt.wantKind != "" {
				if len(sink.records[0].Events) != 1 || sink.records[0].Events[0].Kind != tt.wantKind {
					t.Fatalf("events = %#v", sink.records[0].Events)
				}
				unknown := sink.records[0].Events[0].Data.(model.UnknownData)
				if unknown.Reason != model.UnknownUnsupportedNestedVariant {
					t.Fatalf("unknown = %#v", unknown)
				}
				return
			}
			if len(sink.records[0].Events) != 0 || len(sink.records[0].Diagnostics) != 1 ||
				sink.records[0].Diagnostics[0].InterpretationReason != tt.wantReason {
				t.Fatalf("record = %#v", sink.records[0])
			}
		})
	}
}

func TestObservedAttachmentObjectsAreMetadata(t *testing.T) {
	for _, attachmentType := range []string{
		"task_reminder", "deferred_tools_delta", "skill_listing", "agent_listing_delta",
		"queued_command", "diagnostics", "command_permissions", "edited_text_file",
		"plan_mode_exit", "plan_mode", "nested_memory", "dynamic_skill",
	} {
		t.Run(attachmentType, func(t *testing.T) {
			record := fmt.Sprintf(`{"type":"attachment","attachment":{"type":%q}}`, attachmentType)
			sink, _ := importSource(t, bytesSource(attachmentType, []byte(record+"\n")), nil, nil)
			if len(sink.records) != 1 || len(sink.records[0].Events) != 0 || len(sink.records[0].Diagnostics) != 0 {
				t.Fatalf("record = %#v", sink.records)
			}
		})
	}
}

// TestNullToolResultOutputIsMalformed verifies that explicit null output is
// diagnosed rather than normalized as empty tool output.
func TestNullToolResultOutputIsMalformed(t *testing.T) {
	for _, field := range []string{"content", "output"} {
		t.Run(field, func(t *testing.T) {
			data := []byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","` + field + `":null}]}}` + "\n")
			sink, _ := importSource(t, bytesSource("null-tool-result-"+field, data), nil, nil)
			record := sink.records[0]
			if len(record.Events) != 0 || len(record.Diagnostics) != 1 {
				t.Fatalf("record = %#v", record)
			}
			if got := record.Diagnostics[0]; got.Code != "claude.content_block.tool_result.invalid.0" ||
				got.InterpretationReason != model.InterpretationStructurallyInvalidKnownRecord {
				t.Fatalf("diagnostic = %#v", got)
			}
		})
	}
}

// TestMalformedUnknownSidechainAndTimestampDiagnostics verifies that malformed
// evidence remains diagnosed while sidechain messages normalize normally.
func TestMalformedUnknownSidechainAndTimestampDiagnostics(t *testing.T) {
	sink, _ := importSource(t, fixtureSource(t, "malformed_unknown_sidechain.jsonl"), nil, nil)
	if len(sink.records) != 4 {
		t.Fatalf("records = %d", len(sink.records))
	}
	if len(sink.records[0].Diagnostics) != 1 || sink.records[0].Events[0].Timestamp != nil {
		t.Fatalf("invalid timestamp evidence = %#v", sink.records[0])
	}
	if len(sink.records[1].Events) != 0 || len(sink.records[1].Diagnostics) != 1 || string(sink.records[1].RawRecord.Content) != "{\"type\":\"assistant\",\"sessionId\":\"edge-session\",\"version\":\"3.0.0\",\"uuid\":\"broken\"\n" {
		t.Fatalf("malformed record = %#v", sink.records[1])
	}
	unknown := sink.records[2].Events[0].Data.(model.UnknownData)
	if unknown.OriginalKind != "future-record" || !bytes.Contains(sink.records[2].RawRecord.Content, []byte("secret_shape")) {
		t.Fatalf("unknown record = %#v", sink.records[2])
	}
	sidechain := sink.records[3].Events
	if len(sidechain) != 1 || sidechain[0].Kind != model.EventKindMessage ||
		sidechain[0].Data.(model.MessageData).Text != "branch work" ||
		!strings.Contains(sidechain[0].Summary, "sidechain") {
		t.Fatalf("sidechain = %#v", sidechain)
	}
}
