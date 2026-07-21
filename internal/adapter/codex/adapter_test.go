package codex

import (
	"bytes"
	"context"
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
		if event.Sequence != []int64{2, 4, 5}[i] {
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
	if len(unknown.Events) != 1 || unknown.Events[0].Kind != model.EventKindUnknown || unknown.Events[0].Data.(model.UnknownData).OriginalKind != "future_rollout_item:future_nested" {
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

func TestLargeRecordIsDeliveredBeforeRemainderIsConsumed(t *testing.T) {
	meta := "{\"timestamp\":\"2025-01-01T00:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"streaming\"}}\n"
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
		if accepted == 2 && tracker.readBytes >= len(data) {
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
	view, err := New().Prepare(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if err := view.Import(context.Background(), nil, &captureSink{}); !errors.Is(err, want) {
		t.Fatalf("Import() error = %v, want injected failure", err)
	}
}
