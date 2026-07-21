package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	if sink.session.ID != "window-session" || sink.session.Import.AdapterName != "claude" || sink.session.Import.AdapterVersion != "1" || sink.session.Import.NormalizationVersion != "1" {
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
	if got[6].Data.(model.SummaryData).Text != "The sanitized fixture was inspected." {
		t.Fatalf("summary = %#v", got[6].Data)
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
	if len(sidechain) != 1 || sidechain[0].Kind != model.EventKindUnknown || sidechain[0].Data.(model.UnknownData).OriginalKind != "sidechain:assistant" {
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
	failing := importer.Source{ID: "read-failure", Size: int64(len(line) + 1), Open: func(context.Context) (io.ReadCloser, error) { return reader, nil }}
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
	source := importer.Source{ID: "streaming", Size: int64(len(data)), Open: func(context.Context) (io.ReadCloser, error) { return tracker, nil }}
	view, err := New().Prepare(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
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
