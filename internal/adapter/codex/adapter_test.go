package codex

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
	"time"

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
	for _, record := range sink.records {
		if len(record.Events) > 0 {
			value := record.Events[len(record.Events)-1].Sequence
			last = &value
		}
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

func TestProbeDetectsLegacyAndOrdinalCompositeVersions(t *testing.T) {
	tests := []struct {
		name, want string
	}{
		{"legacy.jsonl", "codex-rollout-jsonl-v1+cli-0.42.0"},
		{"ordinal.jsonl", "codex-rollout-jsonl-v2-ordinal+cli-0.133.0"},
		{"current_0.145.0.jsonl", "codex-rollout-jsonl-v2-ordinal+cli-0.145.0"},
		{"delayed_metadata_0.145.0.jsonl", "codex-rollout-jsonl-v2-ordinal+cli-0.145.0"},
		{"malformed_future_0.145.0.jsonl", "codex-rollout-jsonl-v2-ordinal+cli-0.145.0"},
		{"malformed_unknown.jsonl", "codex-rollout-jsonl-v1+cli-unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probe, err := New().Probe(context.Background(), fixtureSource(t, tt.name))
			if err != nil {
				t.Fatal(err)
			}
			if probe.Confidence != importer.ProbeCertain || string(probe.FormatVersion) != tt.want {
				t.Fatalf("Probe() = (%v, %q), want certain %q", probe.Confidence, probe.FormatVersion, tt.want)
			}
		})
	}
	empty, err := New().Probe(context.Background(), bytesSource("empty", nil))
	if err != nil || empty.Confidence != importer.ProbeUnsupported {
		t.Fatalf("empty Probe() = %#v, %v", empty, err)
	}
	possible, err := New().Probe(context.Background(), bytesSource("jsonl", []byte("{\"custom\":true}\n")))
	if err != nil || possible.Confidence != importer.ProbePossible {
		t.Fatalf("generic JSONL Probe() = %#v, %v", possible, err)
	}
}

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

func TestDelayedMetadataUsesOneProbePrepareInspection(t *testing.T) {
	source := fixtureSource(t, "delayed_metadata_0.145.0.jsonl")
	probe, err := New().Probe(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	view, err := New().Prepare(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	prepared := view.(*prepared)
	defer prepared.Close()
	if prepared.format != probe.FormatVersion ||
		prepared.sessionID != "019c1000-0000-7000-8000-000000000145" ||
		prepared.cliVersion != "0.145.0" || !prepared.ordinal {
		t.Fatalf("prepared inspection = format %q session %q cli %q ordinal %t", prepared.format, prepared.sessionID, prepared.cliVersion, prepared.ordinal)
	}
	wantStarted := time.Date(2026, 7, 21, 10, 0, 5, 0, time.UTC)
	if prepared.startedAt == nil || !prepared.startedAt.Equal(wantStarted) {
		t.Fatalf("started at = %v, want %v", prepared.startedAt, wantStarted)
	}
	sink := &captureSink{}
	if err := prepared.Import(context.Background(), nil, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.records) != 9 || !bytes.Equal(sink.records[0].RawRecord.Content, []byte("{\"timestamp\":\"broken\"\n")) {
		t.Fatalf("replayed records = %d", len(sink.records))
	}
}

func TestMalformedKnownAndFutureVariantsStayDistinct(t *testing.T) {
	sink, _ := importSource(t, fixtureSource(t, "malformed_future_0.145.0.jsonl"), nil, nil)
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

func mustReadFixtureLine(t *testing.T, name string, index int) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	if index >= len(lines) {
		t.Fatalf("fixture %q line %d is missing", name, index)
	}
	return lines[index]
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

func TestMalformedAndUnknownRecordsRetainExactBytes(t *testing.T) {
	source := fixtureSource(t, "malformed_unknown.jsonl")
	sink, _ := importSource(t, source, nil, nil)
	if len(sink.records) != 3 {
		t.Fatalf("records = %d", len(sink.records))
	}
	if len(sink.records[1].Diagnostics) != 1 || len(sink.records[1].Events) != 0 {
		t.Fatalf("malformed envelope = %#v", sink.records[1])
	}
	if string(sink.records[1].RawRecord.Content) != "{\"timestamp\":\"broken\"\n" {
		t.Fatalf("malformed bytes = %q", sink.records[1].RawRecord.Content)
	}
	unknown := sink.records[2]
	if len(unknown.Events) != 1 || unknown.Events[0].Kind != model.EventKindUnknown ||
		unknown.Events[0].Data.(model.UnknownData).OriginalKind != "future_rollout_item" ||
		unknown.Events[0].Data.(model.UnknownData).Reason != model.UnknownUnsupportedRecordKind {
		t.Fatalf("unknown envelope = %#v", unknown)
	}
}

func TestPartialTailIsDeferredThenImportedAfterAppend(t *testing.T) {
	complete := []byte("{\"timestamp\":\"2025-01-01T00:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"partial-session\"}}\n")
	partial := []byte("{\"timestamp\":\"2025-01-01T00:00:01Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"message\":\"later\"")
	firstData := append(append([]byte(nil), complete...), partial...)
	first, _ := importSource(t, bytesSource("partial", firstData), nil, nil)
	if len(first.records) != 1 || first.checkpoint.RecordSequence != 0 {
		t.Fatalf("first import records/checkpoint = %d/%d", len(first.records), first.checkpoint.RecordSequence)
	}
	state := sourceState(first)
	appended := append(append([]byte(nil), firstData...), []byte("}}\n")...)
	second, change := importSource(t, bytesSource("partial", appended), &state.Checkpoint, &state)
	if change != importer.SourceAppend || len(second.records) != 1 || len(second.records[0].Events) != 1 {
		t.Fatalf("append change/records = %q/%d", change, len(second.records))
	}
	if second.records[0].RawRecord.Ref.ByteRange.Offset != int64(len(complete)) {
		t.Fatalf("appended record offset = %d", second.records[0].RawRecord.Ref.ByteRange.Offset)
	}
}

func TestPartialFirstRecordResumesFromZeroOffsetAfterAppend(t *testing.T) {
	partial := []byte("{\"timestamp\":\"2025-01-01T00:00:01Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"message\":\"later\"")
	first, _ := importSource(t, bytesSource("partial-first", partial), nil, nil)
	if len(first.records) != 0 || first.checkpoint.RecordSequence != -1 {
		t.Fatalf("first import records/checkpoint = %d/%d, want 0/-1", len(first.records), first.checkpoint.RecordSequence)
	}

	state := sourceState(first)
	appended := append(append([]byte(nil), partial...), []byte("}}\n")...)
	second, change := importSource(t, bytesSource("partial-first", appended), &state.Checkpoint, &state)
	if change != importer.SourceAppend || len(second.records) != 1 || len(second.records[0].Events) != 1 {
		t.Fatalf("append change/records/events = %q/%d/%d", change, len(second.records), len(events(second.records)))
	}
	if second.records[0].RawRecord.Ref.ByteRange.Offset != 0 {
		t.Fatalf("completed first record offset = %d, want 0", second.records[0].RawRecord.Ref.ByteRange.Offset)
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

func TestVerificationClassifiesSourceChanges(t *testing.T) {
	baseSource := fixtureSource(t, "legacy.jsonl")
	baseData, _ := os.ReadFile(filepath.Join("testdata", "legacy.jsonl"))
	initial, _ := importSource(t, baseSource, nil, nil)
	state := sourceState(initial)
	tests := []struct {
		name string
		data []byte
		want importer.SourceChange
	}{
		{"unchanged", baseData, importer.SourceUnchanged},
		{"append", append(append([]byte(nil), baseData...), []byte("{\"timestamp\":\"2025-01-02T03:04:10Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"error\",\"message\":\"late\"}}\n")...), importer.SourceAppend},
		{"truncated", baseData[:len(baseData)-20], importer.SourceTruncated},
		{"mutated", bytes.Replace(baseData, []byte("Please inspect the fixture."), []byte("Please inspect the fixturE."), 1), importer.SourceMutated},
		{"replaced", bytes.Replace(baseData, []byte("01940000-0000-7000-8000-000000000001"), []byte("01940000-0000-7000-8000-000000000009"), 1), importer.SourceReplaced},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view, err := New().Prepare(context.Background(), bytesSource(string(baseSource.ID), tt.data))
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			got, err := view.Verify(context.Background(), state)
			if err != nil || got != tt.want {
				t.Fatalf("Verify() = %q, %v, want %q", got, err, tt.want)
			}
		})
	}
}

func TestReconcileStreamsCompleteReplacementAfterMutation(t *testing.T) {
	baseData, _ := os.ReadFile(filepath.Join("testdata", "legacy.jsonl"))
	initial, _ := importSource(t, bytesSource("reconcile", baseData), nil, nil)
	state := sourceState(initial)
	mutated := bytes.Replace(baseData, []byte("Please inspect the fixture."), []byte("Please inspect the changed fixture."), 1)
	view, err := New().Prepare(context.Background(), bytesSource("reconcile", mutated))
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	change, err := view.Verify(context.Background(), state)
	if err != nil || change != importer.SourceMutated {
		t.Fatalf("Verify() = %q, %v", change, err)
	}
	sink := &captureSink{}
	if err := view.Reconcile(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.records) != 6 || !bytes.Contains(sink.records[1].RawRecord.Content, []byte("changed fixture")) {
		t.Fatalf("reconciled records = %d", len(sink.records))
	}
}

func TestStableRepeatedImportAndFallbackSessionID(t *testing.T) {
	data := []byte("{\"timestamp\":\"bad\",\"type\":\"future\",\"payload\":{}}\n")
	first, _ := importSource(t, bytesSource("fallback", data), nil, nil)
	second, _ := importSource(t, bytesSource("fallback", data), nil, nil)
	if first.session.ID != second.session.ID || !reflect.DeepEqual(events(first.records), events(second.records)) {
		t.Fatal("fallback session or event identities are not stable")
	}
	if len(first.records[0].Diagnostics) != 1 || first.records[0].Events[0].Timestamp != nil {
		t.Fatalf("malformed timestamp evidence = %#v", first.records[0])
	}
}

func TestCancellationAndSinkBackpressureStopStreaming(t *testing.T) {
	source := fixtureSource(t, "legacy.jsonl")
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
}

func TestLargeRecordExceedsScannerLimitAndIsDelivered(t *testing.T) {
	meta := "{\"timestamp\":\"2025-01-01T00:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"large\"}}\n"
	text := strings.Repeat("x", 300<<10)
	large := "{\"timestamp\":\"2025-01-01T00:00:01Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"agent_message\",\"message\":\"" + text + "\"}}\n"
	sink, _ := importSource(t, bytesSource("large", []byte(meta+large)), nil, nil)
	if len(sink.records) != 2 || len(sink.records[1].RawRecord.Content) <= 256<<10 || len(events(sink.records)) != 1 {
		t.Fatalf("large record delivery = records %d, bytes %d", len(sink.records), len(sink.records[1].RawRecord.Content))
	}
}

func TestOversizedInitialRecordUsesBoundedInspectionAndStillReplays(t *testing.T) {
	large := "{\"timestamp\":\"2026-07-24T00:00:00Z\",\"ordinal\":0,\"type\":\"future_initial\",\"payload\":{\"value\":\"" +
		strings.Repeat("x", inspectionPrefix+(32<<10)) + "\"}}\n"
	meta := "{\"timestamp\":\"2026-07-24T00:00:01Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"after-large\",\"cli_version\":\"0.145.0\"}}\n"
	source := bytesSource("oversized-initial", []byte(large+meta))
	probe, err := New().Probe(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Confidence != importer.ProbeCertain ||
		probe.FormatVersion != "codex-rollout-jsonl-v2-ordinal+cli-0.145.0" {
		t.Fatalf("Probe() = %#v", probe)
	}
	sink, _ := importSource(t, source, nil, nil)
	if sink.session.ID != "after-large" || len(sink.records) != 2 ||
		len(sink.records[0].RawRecord.Content) != len(large) ||
		len(sink.records[0].Events) != 1 || sink.records[0].Events[0].Kind != model.EventKindUnknown {
		t.Fatalf("oversized initial replay = session %q records %d raw %d", sink.session.ID, len(sink.records), len(sink.records[0].RawRecord.Content))
	}
}

func TestOversizedSessionMetadataUsesOnlyBoundedFields(t *testing.T) {
	meta := "{\"timestamp\":\"2026-07-24T01:00:00Z\",\"ordinal\":0,\"type\":\"session_meta\",\"payload\":{\"id\":\"large-meta\",\"session_id\":\"large-meta\",\"timestamp\":\"2026-07-24T01:00:00Z\",\"cli_version\":\"0.145.0\",\"base_instructions\":\"" +
		strings.Repeat("x", inspectionPrefix+(32<<10)) + "\"}}\n"
	message := "{\"timestamp\":\"2026-07-24T01:00:01Z\",\"ordinal\":1,\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"after_large_meta\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ready\"}]}}\n"
	source := bytesSource("oversized-meta", []byte(meta+message))
	probe, err := New().Probe(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	sink, _ := importSource(t, source, nil, nil)
	wantStarted := time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
	if probe.FormatVersion != "codex-rollout-jsonl-v2-ordinal+cli-0.145.0" ||
		sink.session.ID != "large-meta" || sink.session.StartedAt == nil ||
		!sink.session.StartedAt.Equal(wantStarted) || len(events(sink.records)) != 1 {
		t.Fatalf("oversized metadata = probe %#v session %#v events %d", probe, sink.session, len(events(sink.records)))
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

func TestPrepareStopsAtWindowAndReplaysEveryByte(t *testing.T) {
	var data strings.Builder
	for index := 0; index < initialRecords+1; index++ {
		fmt.Fprintf(&data, "{\"timestamp\":\"2026-07-23T10:00:%02dZ\",\"ordinal\":%d,\"type\":\"turn_context\",\"payload\":{\"index\":%d}}\n", index, index, index)
	}
	bytesValue := []byte(data.String())
	windowEnd := 0
	for count := 0; count < initialRecords; count++ {
		windowEnd += bytes.IndexByte(bytesValue[windowEnd:], '\n') + 1
	}
	tracker := &trackingReadCloser{reader: bytes.NewReader(bytesValue)}
	source := importer.Source{
		ID: "bounded-window", Size: int64(len(bytesValue)),
		Open: func(context.Context) (io.ReadCloser, error) { return tracker, nil },
	}
	view, err := New().Prepare(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	prepared := view.(*prepared)
	replayName := prepared.replay.Name()
	if tracker.readBytes != windowEnd {
		t.Fatalf("Prepare() read %d bytes, want exact eight-record window %d", tracker.readBytes, windowEnd)
	}
	sink := &captureSink{}
	if err := prepared.Import(context.Background(), nil, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.records) != initialRecords+1 {
		t.Fatalf("replayed records = %d, want %d", len(sink.records), initialRecords+1)
	}
	var replayed []byte
	for _, record := range sink.records {
		replayed = append(replayed, record.RawRecord.Content...)
	}
	if !bytes.Equal(replayed, bytesValue) {
		t.Fatal("prepared import did not replay every byte exactly once")
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(replayName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replay file still exists after Close(): %v", err)
	}
}

func TestPrepareHonorsCancellationDuringWindow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New().Prepare(ctx, bytesSource("cancel-window", []byte("{}\n")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Prepare() error = %v, want context cancellation", err)
	}
}

func TestRecordDiagnosticBoundIncludesTruncationMarker(t *testing.T) {
	diagnostics := make([]model.Diagnostic, maxRecordDiagnostics+3)
	for index := range diagnostics {
		diagnostics[index] = model.Diagnostic{
			Code: fmt.Sprintf("codex.synthetic.%d", index), Severity: model.SeverityWarning,
			Message:      "Synthetic bounded diagnostic.",
			RawRecordIDs: []model.RawRecordID{"raw-sanitized"},
		}
	}
	got := boundDiagnostics(diagnostics)
	if len(got) != maxRecordDiagnostics ||
		got[len(got)-1].Code != "codex.record.diagnostics.truncated" ||
		len(got[len(got)-1].RawRecordIDs) != 1 {
		t.Fatalf("bounded diagnostics = %#v", got)
	}
}

func TestLargeRecordIsDeliveredBeforeRemainderIsConsumed(t *testing.T) {
	meta := "{\"timestamp\":\"2025-01-01T00:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"streaming\"}}\n"
	for i := 1; i < initialRecords; i++ {
		meta += fmt.Sprintf("{\"timestamp\":\"2025-01-01T00:00:%02dZ\",\"type\":\"turn_context\",\"payload\":{}}\n", i)
	}
	large := "{\"timestamp\":\"2025-01-01T00:00:01Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"agent_message\",\"message\":\"" + strings.Repeat("x", 300<<10) + "\"}}\n"
	tail := "{\"timestamp\":\"2025-01-01T00:00:02Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"agent_message\",\"message\":\"" + strings.Repeat("y", 128<<10) + "\"}}\n"
	data := []byte(meta + large + tail)
	tracker := &trackingReadCloser{reader: bytes.NewReader(data)}
	source := importer.Source{ID: "streaming", Size: int64(len(data)), Open: func(context.Context) (io.ReadCloser, error) { return tracker, nil }}
	view, err := New().Prepare(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	accepted := 0
	sink := &captureSink{onAccept: func(importer.RecordEnvelope) {
		accepted++
		if accepted == initialRecords+1 && tracker.readBytes >= len(data) {
			t.Fatal("large record was not delivered until after the entire source was consumed")
		}
	}}
	if err := view.Import(context.Background(), nil, sink); err != nil {
		t.Fatal(err)
	}
}

func TestReadFailureStopsImport(t *testing.T) {
	line := []byte("{\"timestamp\":\"2025-01-01T00:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"read-failure\"}}\n")
	want := errors.New("injected read failure")
	reader := &trackingReadCloser{reader: bytes.NewReader(line), failAfter: len(line), fail: want}
	source := importer.Source{ID: "read-failure", Size: int64(len(line) + 1), Open: func(context.Context) (io.ReadCloser, error) { return reader, nil }}
	if _, err := New().Prepare(context.Background(), source); !errors.Is(err, want) {
		t.Fatalf("Prepare() error = %v, want injected failure", err)
	}
}
