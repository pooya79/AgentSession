package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

type captureSink struct {
	session     model.Session
	records     []importer.RecordEnvelope
	checkpoint  importer.ImportCheckpoint
	acceptError error
	onAccept    func(importer.RecordEnvelope)
}

func (s *captureSink) Begin(_ context.Context, session model.Session) error {
	s.session = session
	return nil
}

func (s *captureSink) Accept(_ context.Context, envelope importer.RecordEnvelope) error {
	if s.acceptError != nil {
		return s.acceptError
	}
	if s.onAccept != nil {
		s.onAccept(envelope)
	}
	s.records = append(s.records, envelope)
	return nil
}

func (s *captureSink) Complete(_ context.Context, session model.Session, checkpoint importer.ImportCheckpoint) error {
	s.session = session
	s.checkpoint = checkpoint
	return nil
}

func fixtureSource(t *testing.T, name string) importer.Source {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return bytesSource("fixture:"+name, data)
}

func bytesSource(id string, data []byte) importer.Source {
	data = append([]byte(nil), data...)
	return importer.Source{ID: model.SourceID(id), Size: int64(len(data)), OpenAt: func(_ context.Context, offset int64) (io.ReadCloser, error) {
		if offset < 0 || offset > int64(len(data)) {
			return nil, errors.New("offset out of range")
		}
		return io.NopCloser(bytes.NewReader(data[offset:])), nil
	}}
}

func importSource(t *testing.T, source importer.Source, resume *importer.ImportCheckpoint, state *importer.SourceState) (*captureSink, importer.SourceChange) {
	t.Helper()
	view, err := New().Prepare(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	change := importer.SourceNew
	if state != nil {
		change, err = view.Verify(context.Background(), *state)
		if err != nil {
			t.Fatal(err)
		}
	}
	sink := &captureSink{}
	if err := view.Import(context.Background(), resume, sink); err != nil {
		t.Fatal(err)
	}
	return sink, change
}

func sourceState(sink *captureSink) importer.SourceState {
	var last *int64
	all := events(sink.records)
	if len(all) > 0 {
		value := all[len(all)-1].Sequence
		last = &value
	}
	return importer.SourceState{SessionID: sink.session.ID, Import: sink.session.Import, Session: sink.session, Checkpoint: sink.checkpoint, LastEventSequence: last}
}

func events(records []importer.RecordEnvelope) []model.Event {
	var result []model.Event
	for _, record := range records {
		result = append(result, record.Events...)
	}
	return result
}

func TestProbeAndPrepareInspectEightCompleteRecords(t *testing.T) {
	var lines []string
	for i := 0; i < 7; i++ {
		lines = append(lines, `{"type":"file-history-snapshot"}`)
	}
	lines = append(lines, `{"type":"user","sessionId":"window-session","version":"9.8.7","message":{"role":"user","content":"eighth"}}`)
	data := []byte(strings.Join(lines, "\n") + "\n")
	source := bytesSource("window", data)
	probe, err := New().Probe(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Confidence != importer.ProbeCertain || probe.FormatVersion != "claude-code-jsonl-v1+cli-9.8.7" {
		t.Fatalf("Probe() = %#v", probe)
	}
	sink, _ := importSource(t, source, nil, nil)
	if sink.session.ID != "window-session" || sink.session.Import.AdapterName != "claude" || sink.session.Import.AdapterVersion != "1" || sink.session.Import.NormalizationVersion != NormalizationVersion {
		t.Fatalf("session metadata = %#v", sink.session)
	}

	unsupported, err := New().Probe(context.Background(), bytesSource("empty", nil))
	if err != nil || unsupported.Confidence != importer.ProbeUnsupported {
		t.Fatalf("empty probe = %#v, %v", unsupported, err)
	}
	possible, err := New().Probe(context.Background(), bytesSource("generic", []byte("{\"custom\":true}\n")))
	if err != nil || possible.Confidence != importer.ProbePossible || possible.FormatVersion != "claude-code-jsonl-v1+cli-unknown" {
		t.Fatalf("generic probe = %#v, %v", possible, err)
	}
}

func TestProbeTreatsClaudeStructuralRecordsAsCertain(t *testing.T) {
	tests := map[string]string{
		"system variant":          `{"type":"system"}`,
		"progress variant":        `{"type":"progress"}`,
		"queue operation variant": `{"type":"queue-operation"}`,
		"session ID":              `{"type":"future-record","sessionId":"claude-session"}`,
		"sidechain field":         `{"type":"future-record","isSidechain":false}`,
	}
	for name, record := range tests {
		t.Run(name, func(t *testing.T) {
			probe, err := New().Probe(context.Background(), bytesSource(name, []byte(record+"\n")))
			if err != nil || probe.Confidence != importer.ProbeCertain {
				t.Fatalf("Probe() = %#v, %v", probe, err)
			}
		})
	}
}

// TestProbeAndPrepareSkipMalformedAndSparseLeadingRecords verifies that header
// discovery continues until usable session metadata appears.
func TestProbeAndPrepareSkipMalformedAndSparseLeadingRecords(t *testing.T) {
	data := []byte("{\"type\":\"assistant\"\n" +
		"{\"custom\":true}\n" +
		"{\"type\":\"file-history-snapshot\"}\n" +
		"{\"type\":\"user\",\"sessionId\":\"delayed-session\",\"version\":\"2.1.179\",\"message\":{\"role\":\"user\",\"content\":\"late metadata\"}}\n")
	source := bytesSource("delayed-metadata", data)
	probe, err := New().Probe(context.Background(), source)
	if err != nil || probe.Confidence != importer.ProbeCertain ||
		probe.FormatVersion != "claude-code-jsonl-v1+cli-2.1.179" ||
		len(probe.Diagnostics) != 1 {
		t.Fatalf("Probe() = %#v, %v", probe, err)
	}
	sink, _ := importSource(t, source, nil, nil)
	if sink.session.ID != "delayed-session" ||
		sink.session.Import.FormatVersion != "claude-code-jsonl-v1+cli-2.1.179" ||
		len(sink.records) != 4 || len(events(sink.records)) != 1 {
		t.Fatalf("delayed import = session %#v records %#v", sink.session, sink.records)
	}
}

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

func TestAgentTaskLogProbesAsClaude(t *testing.T) {
	source := bytesSource("agent-task-log", []byte(`{"type":"started","agentId":"agent-one","key":"task"}`+"\n"))
	probe, err := New().Probe(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Confidence != importer.ProbeCertain {
		t.Fatalf("probe confidence = %d", probe.Confidence)
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

func TestIncompleteTailIsDeferredAndAppendResumesEventSequence(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "incomplete.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.TrimSuffix(data, []byte("\n"))
	first, _ := importSource(t, bytesSource("partial", data), nil, nil)
	if len(first.records) != 1 || first.checkpoint.RecordSequence != 0 || len(events(first.records)) != 1 {
		t.Fatalf("first import = records %d checkpoint %d events %d", len(first.records), first.checkpoint.RecordSequence, len(events(first.records)))
	}
	state := sourceState(first)
	appended := append(append([]byte(nil), data...), []byte("}}\n")...)
	second, change := importSource(t, bytesSource("partial", appended), &state.Checkpoint, &state)
	if change != importer.SourceAppend || len(second.records) != 1 || len(events(second.records)) != 1 {
		t.Fatalf("append = change %q records %d events %d", change, len(second.records), len(events(second.records)))
	}
	if second.records[0].RawRecord.Ref.RecordSequence == nil || *second.records[0].RawRecord.Ref.RecordSequence != 1 || second.records[0].Events[0].Sequence != 1 {
		t.Fatalf("resumed progress = %#v", second.records[0])
	}
}

func TestZeroRecordResumeAndFallbackIdentitiesAreStable(t *testing.T) {
	partial := []byte(`{"type":"user","message":{"role":"user","content":"later"}`)
	first, _ := importSource(t, bytesSource("fallback-partial", partial), nil, nil)
	if len(first.records) != 0 || first.checkpoint.RecordSequence != importer.NoRecordSequence || first.session.Import.FormatVersion != "claude-code-jsonl-v1+cli-unknown" {
		t.Fatalf("partial import = %#v", first)
	}
	state := sourceState(first)
	complete := append(append([]byte(nil), partial...), []byte("}\n")...)
	second, change := importSource(t, bytesSource("fallback-partial", complete), &state.Checkpoint, &state)
	third, _ := importSource(t, bytesSource("fallback-partial", complete), nil, nil)
	if change != importer.SourceAppend || second.session.ID != third.session.ID || !reflect.DeepEqual(events(second.records), events(third.records)) {
		t.Fatal("fallback session/event identities are not stable")
	}
}

func TestVerificationAndReconciliationClassifyChanges(t *testing.T) {
	baseData, err := os.ReadFile(filepath.Join("testdata", "main.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	initial, _ := importSource(t, bytesSource("verify", baseData), nil, nil)
	state := sourceState(initial)
	tests := []struct {
		name string
		data []byte
		want importer.SourceChange
	}{
		{"unchanged", baseData, importer.SourceUnchanged},
		{"append", append(append([]byte(nil), baseData...), []byte("{\"type\":\"summary\",\"sessionId\":\"claude-session-1\",\"version\":\"2.1.3\",\"summary\":\"late\"}\n")...), importer.SourceAppend},
		{"truncated", baseData[:len(baseData)-20], importer.SourceTruncated},
		{"mutated", bytes.Replace(baseData, []byte("sanitized fixture"), []byte("sanitized fixturE"), 1), importer.SourceMutated},
		{"replaced", bytes.Replace(baseData, []byte("claude-session-1"), []byte("claude-session-9"), 1), importer.SourceReplaced},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view, err := New().Prepare(context.Background(), bytesSource("verify", tt.data))
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			got, err := view.Verify(context.Background(), state)
			if err != nil || got != tt.want {
				t.Fatalf("Verify() = %q, %v, want %q", got, err, tt.want)
			}
			if got == importer.SourceMutated {
				sink := &captureSink{}
				if err := view.Reconcile(context.Background(), sink); err != nil {
					t.Fatal(err)
				}
				if len(sink.records) != 5 || !bytes.Contains(sink.records[1].RawRecord.Content, []byte("fixturE")) {
					t.Fatalf("reconciled records = %d", len(sink.records))
				}
			}
		})
	}
}

func TestCancellationSinkBackpressureAndReadFailure(t *testing.T) {
	source := fixtureSource(t, "main.jsonl")
	view, err := New().Prepare(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := view.Import(ctx, nil, &captureSink{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Import() error = %v", err)
	}
	view.Close()

	view, _ = New().Prepare(context.Background(), source)
	defer view.Close()
	want := errors.New("backpressure")
	if err := view.Import(context.Background(), nil, &captureSink{acceptError: want}); !errors.Is(err, want) {
		t.Fatalf("backpressure Import() error = %v", err)
	}

	line := []byte("{\"type\":\"user\",\"sessionId\":\"read-failure\",\"message\":{\"role\":\"user\",\"content\":\"ok\"}}\n")
	reader := &trackingReadCloser{reader: bytes.NewReader(line), failAfter: len(line), fail: errors.New("injected read failure")}
	failing := importer.Source{ID: "read-failure", Size: int64(len(line) + 1), OpenAt: func(context.Context, int64) (io.ReadCloser, error) { return reader, nil }}
	if _, err := New().Prepare(context.Background(), failing); !errors.Is(err, reader.fail) {
		t.Fatalf("Prepare() error = %v, want read failure", err)
	}
}

type trackingReadCloser struct {
	reader    *bytes.Reader
	readBytes int
	failAfter int
	fail      error
}

func (r *trackingReadCloser) Read(p []byte) (int, error) {
	if r.fail != nil && r.readBytes >= r.failAfter {
		return 0, r.fail
	}
	n, err := r.reader.Read(p)
	r.readBytes += n
	return n, err
}

func (*trackingReadCloser) Close() error { return nil }

func TestOversizedRecordIsDeliveredBeforeRemainingSourceIsConsumed(t *testing.T) {
	largeText := strings.Repeat("large-evidence-", 24<<10)
	large, _ := json.Marshal(map[string]any{"type": "assistant", "sessionId": "streaming", "version": "5.0.0", "uuid": "large", "message": map[string]any{"role": "assistant", "content": largeText}})
	var records [][]byte
	for i := 0; i < 7; i++ {
		records = append(records, []byte(`{"type":"file-history-snapshot","sessionId":"streaming","version":"5.0.0"}`))
	}
	records = append(records, large)
	for i := 0; i < 1024; i++ {
		records = append(records, []byte(`{"type":"summary","sessionId":"streaming","version":"5.0.0","summary":"tail"}`))
	}
	data := bytes.Join(records, []byte("\n"))
	data = append(data, '\n')
	tracker := &trackingReadCloser{reader: bytes.NewReader(data)}
	source := importer.Source{ID: "streaming", Size: int64(len(data)), OpenAt: func(context.Context, int64) (io.ReadCloser, error) { return tracker, nil }}
	view, err := New().Prepare(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	preparedView := view.(*prepared)
	if preparedView.replay == nil {
		t.Fatal("probe prefix was not moved to replay storage")
	}
	if preparedView.sessionID != "streaming" || preparedView.format != "claude-code-jsonl-v1+cli-5.0.0" {
		t.Fatalf("large probe metadata = session %q format %q", preparedView.sessionID, preparedView.format)
	}
	if info, statErr := preparedView.replay.Stat(); statErr != nil || info.Size() <= int64(len(large)) {
		t.Fatalf("probe replay size = %v, %v", info, statErr)
	}
	accepted := 0
	sink := &captureSink{onAccept: func(envelope importer.RecordEnvelope) {
		accepted++
		if accepted == 8 {
			if len(envelope.RawRecord.Content) <= 256<<10 || len(envelope.Events) != 1 || len(envelope.Events[0].Data.(model.MessageData).Text) != len(largeText) {
				t.Fatalf("large envelope = raw %d events %d", len(envelope.RawRecord.Content), len(envelope.Events))
			}
			if tracker.readBytes >= len(data) {
				t.Fatal("large record was delivered only after the remaining source was consumed")
			}
		}
	}}
	if err := view.Import(context.Background(), nil, sink); err != nil {
		t.Fatal(err)
	}
}
