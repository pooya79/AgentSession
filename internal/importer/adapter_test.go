package importer

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/pooya79/AgentSession/internal/model"
)

type fakeAdapter struct {
	probeResult ProbeResult
	probeErr    error
	probeFn     func(context.Context, Source) (ProbeResult, error)
	importFn    func(context.Context, Source, RecordSink) error
	probeCalls  int
	importCalls int
}

func (*fakeAdapter) Name() string           { return "fixture" }
func (*fakeAdapter) Version() model.Version { return "1" }

func (a *fakeAdapter) Probe(ctx context.Context, source Source) (ProbeResult, error) {
	a.probeCalls++
	if a.probeFn != nil {
		return a.probeFn(ctx, source)
	}
	return a.probeResult, a.probeErr
}

func (a *fakeAdapter) Import(ctx context.Context, source Source, sink RecordSink) error {
	a.importCalls++
	if a.importFn == nil {
		return nil
	}
	return a.importFn(ctx, source, sink)
}

type sinkFunc func(context.Context, RecordEnvelope) error

func (f sinkFunc) Accept(ctx context.Context, envelope RecordEnvelope) error {
	return f(ctx, envelope)
}

func TestProbeIsIndependentFromCanonicalImport(t *testing.T) {
	adapter := &fakeAdapter{probeResult: ProbeResult{Confidence: ProbeCertain, FormatVersion: "1"}}
	source := testSource([]byte("fixture"))

	result, err := adapter.Probe(context.Background(), source)
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("ProbeResult.Validate() error = %v", err)
	}
	if adapter.probeCalls != 1 || adapter.importCalls != 0 {
		t.Fatalf("calls after Probe() = probe %d, import %d; want 1, 0", adapter.probeCalls, adapter.importCalls)
	}
}

func TestSourceAndProbeResultValidation(t *testing.T) {
	validSource := testSource(nil)
	if err := validSource.Validate(); err != nil {
		t.Fatalf("Source.Validate() error = %v", err)
	}

	sources := []Source{
		{},
		{ID: "source-1", Size: -1, Open: validSource.Open},
		{ID: "source-1"},
	}
	for i, source := range sources {
		if err := source.Validate(); err == nil {
			t.Errorf("source case %d Validate() error = nil", i)
		}
	}

	results := []ProbeResult{
		{Confidence: ProbePossible},
		{Confidence: ProbeUnsupported, FormatVersion: "unexpected"},
		{Confidence: ProbeConfidence(99)},
		{Diagnostics: []model.Diagnostic{{Severity: model.SeverityWarning, Message: "missing code"}}},
	}
	for i, result := range results {
		if err := result.Validate(); err == nil {
			t.Errorf("probe result case %d Validate() error = nil", i)
		}
	}
	unsupported := ProbeResult{Confidence: ProbeUnsupported}
	if err := unsupported.Validate(); err != nil {
		t.Fatalf("unsupported ProbeResult.Validate() error = %v", err)
	}
}

func TestProbePreservesOpenErrorsAndCancellation(t *testing.T) {
	openErr := errors.New("read denied")
	adapter := &fakeAdapter{probeFn: func(ctx context.Context, source Source) (ProbeResult, error) {
		if err := ctx.Err(); err != nil {
			return ProbeResult{}, err
		}
		stream, err := source.Open(ctx)
		if err != nil {
			return ProbeResult{}, fmt.Errorf("probe source %q: %w", source.ID, err)
		}
		defer stream.Close()
		return ProbeResult{Confidence: ProbePossible, FormatVersion: "1"}, nil
	}}
	source := Source{ID: "source-1", Open: func(context.Context) (io.ReadCloser, error) {
		return nil, openErr
	}}
	if _, err := adapter.Probe(context.Background(), source); !errors.Is(err, openErr) {
		t.Fatalf("Probe() error = %v, want wrapped open error", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.Probe(ctx, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Probe() error = %v, want context cancellation", err)
	}
}

func TestRecordEnvelopePreservesUnknownAndMalformedRecords(t *testing.T) {
	unknown := testEnvelope(t, 0, []byte("{\"type\":\"future\"}\n"))
	unknown.Events = []model.Event{testUnknownEvent(t, unknown.RawRecord.Ref, 0)}
	if err := unknown.Validate(); err != nil {
		t.Fatalf("unknown envelope Validate() error = %v", err)
	}

	malformed := testEnvelope(t, 1, []byte("{malformed}\n"))
	malformed.Diagnostics = []model.Diagnostic{{
		Code:         "record.malformed",
		Severity:     model.SeverityWarning,
		Message:      "record could not be normalized",
		RawRecordIDs: []model.RawRecordID{malformed.RawRecord.Ref.ID},
	}}
	if err := malformed.Validate(); err != nil {
		t.Fatalf("malformed envelope Validate() error = %v", err)
	}
	if !bytes.Equal(malformed.RawRecord.Content, []byte("{malformed}\n")) {
		t.Fatal("malformed envelope did not preserve exact source bytes")
	}
}

func TestRecordEnvelopeRejectsUnrelatedOrChangedEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RecordEnvelope)
	}{
		{name: "silent record", mutate: func(e *RecordEnvelope) {}},
		{name: "changed content", mutate: func(e *RecordEnvelope) {
			e.Diagnostics = []model.Diagnostic{{Code: "record.bad", Severity: model.SeverityWarning, Message: "bad"}}
			e.RawRecord.Content = []byte("changed")
		}},
		{name: "unrelated diagnostic", mutate: func(e *RecordEnvelope) {
			e.Diagnostics = []model.Diagnostic{{Code: "record.bad", Severity: model.SeverityWarning, Message: "bad", RawRecordIDs: []model.RawRecordID{"raw-other"}}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envelope := testEnvelope(t, 0, []byte("record\n"))
			tt.mutate(&envelope)
			if err := envelope.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want envelope validation error")
			}
		})
	}
}

func TestRecordEnvelopeCheckpointIsTiedToDeliveredRecord(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RecordEnvelope)
	}{
		{name: "last record hash", mutate: func(e *RecordEnvelope) {
			e.Checkpoint.LastRecordHash = model.HashRecord([]byte("other record"))
		}},
		{name: "record sequence", mutate: func(e *RecordEnvelope) {
			e.Checkpoint.RecordSequence++
		}},
		{name: "byte offset", mutate: func(e *RecordEnvelope) {
			e.Checkpoint.ByteOffset--
		}},
		{name: "missing record position", mutate: func(e *RecordEnvelope) {
			e.RawRecord.Ref.RecordSequence = nil
			e.RawRecord.Ref.ByteRange = nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envelope := testEnvelope(t, 3, []byte("record\n"))
			envelope.Diagnostics = []model.Diagnostic{{
				Code: "record.test", Severity: model.SeverityInfo, Message: "test record",
				RawRecordIDs: []model.RawRecordID{envelope.RawRecord.Ref.ID},
			}}
			tt.mutate(&envelope)
			if err := envelope.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want record-to-checkpoint mismatch")
			}
		})
	}
}

func TestStreamingImportStopsWithoutReadingVeryLargeSource(t *testing.T) {
	const records = int64(10_000_000)
	reader := &syntheticRecordReader{remaining: records}
	source := Source{
		ID:   "source-large",
		Size: records * int64(len(syntheticLine)),
		Hint: "fixture-format",
		Open: func(context.Context) (io.ReadCloser, error) {
			return reader, nil
		},
	}
	adapter := &fakeAdapter{importFn: streamSyntheticRecords}
	stop := errors.New("stop after prefix")
	accepted := 0
	sink := sinkFunc(func(_ context.Context, envelope RecordEnvelope) error {
		if err := envelope.Validate(); err != nil {
			return fmt.Errorf("validate delivered envelope: %w", err)
		}
		accepted++
		if accepted == 10 {
			return stop
		}
		return nil
	})

	err := adapter.Import(context.Background(), source, sink)
	if !errors.Is(err, stop) {
		t.Fatalf("Import() error = %v, want sink error", err)
	}
	if accepted != 10 {
		t.Fatalf("accepted records = %d, want 10", accepted)
	}
	if reader.eof {
		t.Fatal("streaming import read the entire synthetic source before delivery")
	}
	if reader.bytesRead >= source.Size/1000 {
		t.Fatalf("bytes read = %d of %d, want only a bounded prefix", reader.bytesRead, source.Size)
	}
}

func TestStreamingImportHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	adapter := &fakeAdapter{importFn: streamSyntheticRecords}
	source := Source{ID: "source-cancelled", Size: int64(len(syntheticLine)), Open: func(context.Context) (io.ReadCloser, error) {
		return &syntheticRecordReader{remaining: 1}, nil
	}}
	err := adapter.Import(ctx, source, sinkFunc(func(context.Context, RecordEnvelope) error {
		t.Fatal("sink called after cancellation")
		return nil
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Import() error = %v, want context cancellation", err)
	}
}

func streamSyntheticRecords(ctx context.Context, source Source, sink RecordSink) error {
	if err := source.Validate(); err != nil {
		return err
	}
	stream, err := source.Open(ctx)
	if err != nil {
		return fmt.Errorf("open source %q: %w", source.ID, err)
	}
	defer stream.Close()
	reader := bufio.NewReader(stream)
	var sequence int64
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		record, err := reader.ReadBytes('\n')
		if len(record) > 0 {
			envelope := testEnvelopeForSource(source, sequence, offset, record)
			envelope.Events = []model.Event{testUnknownEventForSource(envelope.RawRecord.Ref, sequence)}
			if sinkErr := sink.Accept(ctx, envelope); sinkErr != nil {
				return fmt.Errorf("deliver source %q record %d: %w", source.ID, sequence, sinkErr)
			}
			offset += int64(len(record))
			sequence++
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read source %q: %w", source.ID, err)
		}
	}
}

func testSource(content []byte) Source {
	return Source{ID: "source-1", Size: int64(len(content)), Hint: "fixture-format", Open: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(content)), nil
	}}
}

func testEnvelope(t *testing.T, sequence int64, content []byte) RecordEnvelope {
	t.Helper()
	source := Source{ID: "source-1", Size: int64(len(content))}
	return testEnvelopeForSource(source, sequence, 0, content)
}

func testEnvelopeForSource(source Source, sequence, offset int64, content []byte) RecordEnvelope {
	contentHash := model.HashRecord(content)
	byteRange := model.ByteRange{Offset: offset, Length: int64(len(content))}
	rawID, err := model.NewRawRecordID(model.RawRecordIDInput{
		SourceID: source.ID, RecordSequence: &sequence, ByteRange: &byteRange, ContentHash: contentHash,
	})
	if err != nil {
		panic(err)
	}
	ref := model.RawRecordRef{
		ID: rawID, SourceID: source.ID, RecordSequence: &sequence, ByteRange: &byteRange, ContentHash: contentHash,
	}
	return RecordEnvelope{
		RawRecord: model.RawRecord{Ref: ref, Content: append([]byte(nil), content...)},
		Checkpoint: ImportCheckpoint{
			SourceID: source.ID, ByteOffset: offset + int64(len(content)), RecordSequence: sequence,
			PrefixHash: fmt.Sprintf("sha256:prefix-%d", sequence), LastRecordHash: contentHash, SourceSize: source.Size,
		},
	}
}

func testUnknownEvent(t *testing.T, ref model.RawRecordRef, sequence int64) model.Event {
	t.Helper()
	return testUnknownEventForSource(ref, sequence)
}

func testUnknownEventForSource(ref model.RawRecordRef, sequence int64) model.Event {
	id, err := model.NewEventID(model.EventIDInput{
		SourceID: ref.SourceID, RecordSequence: ref.RecordSequence, ByteRange: ref.ByteRange, RecordHash: ref.ContentHash,
	})
	if err != nil {
		panic(err)
	}
	return model.Event{
		ID: id, SessionID: "session-fixture", Sequence: sequence, Kind: model.EventKindUnknown,
		Summary: "unknown source record", Data: model.UnknownData{OriginalKind: "fixture"}, RawRecord: ref,
	}
}

const syntheticLine = "{}\n"

type syntheticRecordReader struct {
	remaining int64
	position  int
	bytesRead int64
	eof       bool
}

func (r *syntheticRecordReader) Read(buffer []byte) (int, error) {
	if r.remaining == 0 {
		r.eof = true
		return 0, io.EOF
	}
	written := 0
	for written < len(buffer) && r.remaining > 0 {
		count := copy(buffer[written:], syntheticLine[r.position:])
		written += count
		r.position += count
		if r.position == len(syntheticLine) {
			r.position = 0
			r.remaining--
		}
	}
	r.bytesRead += int64(written)
	return written, nil
}

func (*syntheticRecordReader) Close() error { return nil }
