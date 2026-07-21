package importer

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/model"
)

type fakeAdapter struct {
	probeResult ProbeResult
	probeErr    error
	probeFn     func(context.Context, Source) (ProbeResult, error)
	importFn    func(context.Context, Source, *ImportCheckpoint, ImportSink) error
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

func (a *fakeAdapter) Prepare(_ context.Context, source Source) (PreparedSource, error) {
	return &fakePreparedSource{adapter: a, source: source}, nil
}

func (a *fakeAdapter) runImport(ctx context.Context, source Source, resume *ImportCheckpoint, sink ImportSink) error {
	a.importCalls++
	if a.importFn == nil {
		return nil
	}
	return a.importFn(ctx, source, resume, sink)
}

type fakePreparedSource struct {
	adapter *fakeAdapter
	source  Source
}

func (*fakePreparedSource) Verify(context.Context, SourceState) (SourceChange, error) {
	return SourceAppend, nil
}

func (p *fakePreparedSource) Import(ctx context.Context, resume *ImportCheckpoint, sink ImportSink) error {
	return p.adapter.runImport(ctx, p.source, resume, sink)
}

func (p *fakePreparedSource) Reconcile(ctx context.Context, sink ImportSink) error {
	return p.adapter.runImport(ctx, p.source, nil, sink)
}

func (*fakePreparedSource) Close() error { return nil }

type sinkFunc func(context.Context, RecordEnvelope) error

func (sinkFunc) Begin(context.Context, model.Session) error { return nil }

func (f sinkFunc) Accept(ctx context.Context, envelope RecordEnvelope) error {
	return f(ctx, envelope)
}

func (sinkFunc) Complete(context.Context, model.Session, ImportCheckpoint) error { return nil }

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

func TestRecordEnvelopePermitsRetainedMetadataWithoutTimelineEvidence(t *testing.T) {
	envelope := testEnvelope(t, 0, []byte("metadata\n"))
	if err := envelope.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRecordEnvelopeCheckpointIsTiedToDeliveredRecord(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RecordEnvelope)
	}{
		{name: "record sequence", mutate: func(e *RecordEnvelope) {
			e.Checkpoint.RecordSequence++
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

func TestImportSessionLifecycleSupportsMalformedOnlySource(t *testing.T) {
	source := testSource([]byte("{malformed}\n"))
	initial := testSession(source)
	envelope := testEnvelopeForSource(source, 0, 0, []byte("{malformed}\n"))
	envelope.Diagnostics = []model.Diagnostic{{
		Code: "record.malformed", Severity: model.SeverityWarning, Message: "record could not be normalized",
		RawRecordIDs: []model.RawRecordID{envelope.RawRecord.Ref.ID},
	}}
	startedAt := time.Date(2026, time.July, 18, 10, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(time.Minute)
	completed := initial
	completed.Title = "Recovered session"
	completed.StartedAt = &startedAt
	completed.EndedAt = &endedAt

	adapter := &fakeAdapter{importFn: func(ctx context.Context, _ Source, _ *ImportCheckpoint, sink ImportSink) error {
		if err := sink.Begin(ctx, initial); err != nil {
			return err
		}
		if err := sink.Accept(ctx, envelope); err != nil {
			return err
		}
		return sink.Complete(ctx, completed, envelope.Checkpoint)
	}}
	sink := &lifecycleSink{}
	if err := adapter.runImport(context.Background(), source, nil, sink); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if sink.calls != "begin,accept,complete" {
		t.Fatalf("lifecycle calls = %q, want begin,accept,complete", sink.calls)
	}
	if len(sink.envelopes) != 1 || len(sink.envelopes[0].Events) != 0 {
		t.Fatalf("accepted envelopes = %#v, want one malformed record without events", sink.envelopes)
	}
	if err := sink.envelopes[0].ValidateForSession(sink.initial); err != nil {
		t.Fatalf("ValidateForSession() error = %v", err)
	}
	if err := ValidateSessionTransition(sink.initial, sink.completed); err != nil {
		t.Fatalf("ValidateSessionTransition() error = %v", err)
	}
	if sink.completed.StartedAt == nil || sink.completed.EndedAt == nil || len(sink.completed.Diagnostics) != 0 {
		t.Fatalf("completed session lost timestamps or unexpectedly accumulated record diagnostics: %#v", sink.completed)
	}
	recordDiagnostics := sink.envelopes[0].RecordDiagnostics()
	if len(recordDiagnostics) != 1 || recordDiagnostics[0].RawRecordID != envelope.RawRecord.Ref.ID ||
		recordDiagnostics[0].Ordinal != 0 || !diagnosticEqual(recordDiagnostics[0].Diagnostic, envelope.Diagnostics[0]) {
		t.Fatalf("RecordDiagnostics() = %#v, want independently persistable envelope diagnostic", recordDiagnostics)
	}
}

func TestSessionLifecycleValidationRejectsChangedIdentityOrLostDiagnostics(t *testing.T) {
	initial := testSession(testSource(nil))
	initial.Diagnostics = []model.Diagnostic{{
		Code: "session.partial", Severity: model.SeverityWarning, Message: "session metadata is partial",
	}}
	tests := []struct {
		name   string
		mutate func(*model.Session)
	}{
		{name: "session ID", mutate: func(session *model.Session) { session.ID = "other-session" }},
		{name: "source ID", mutate: func(session *model.Session) { session.Import.SourceID = "other-source" }},
		{name: "normalization version", mutate: func(session *model.Session) { session.Import.NormalizationVersion = "2" }},
		{name: "lost diagnostics", mutate: func(session *model.Session) { session.Diagnostics = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			completed := initial
			completed.Diagnostics = append([]model.Diagnostic(nil), initial.Diagnostics...)
			tt.mutate(&completed)
			if err := ValidateSessionTransition(initial, completed); err == nil {
				t.Fatal("ValidateSessionTransition() error = nil, want lifecycle mismatch")
			}
		})
	}
}

func TestRecordEnvelopeRejectsDifferentLifecycleSession(t *testing.T) {
	envelope := testEnvelope(t, 0, []byte("record\n"))
	envelope.Events = []model.Event{testUnknownEvent(t, envelope.RawRecord.Ref, 0)}
	session := testSession(testSource(nil))
	session.ID = "another-session"
	if err := envelope.ValidateForSession(session); err == nil {
		t.Fatal("ValidateForSession() error = nil, want session mismatch")
	}
}

func TestRecordEnvelopeDiagnosticsDetachForBatching(t *testing.T) {
	envelope := testEnvelope(t, 0, []byte("record\n"))
	event := testUnknownEvent(t, envelope.RawRecord.Ref, 0)
	envelope.Events = []model.Event{event}
	envelope.Diagnostics = []model.Diagnostic{{
		Code: "record.partial", Severity: model.SeverityWarning, Message: "partial record",
		EventIDs: []model.EventID{event.ID}, RawRecordIDs: []model.RawRecordID{envelope.RawRecord.Ref.ID},
	}}

	diagnostics := envelope.RecordDiagnostics()
	envelope.Diagnostics[0].Message = "changed"
	envelope.Diagnostics[0].EventIDs[0] = "changed-event"
	envelope.Diagnostics[0].RawRecordIDs[0] = "changed-record"
	if diagnostics[0].Diagnostic.Message != "partial record" || diagnostics[0].Diagnostic.EventIDs[0] != event.ID ||
		diagnostics[0].Diagnostic.RawRecordIDs[0] != envelope.RawRecord.Ref.ID {
		t.Fatalf("RecordDiagnostics() retained mutable envelope storage: %#v", diagnostics)
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

	err := adapter.runImport(context.Background(), source, nil, sink)
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

func TestStreamingImportResumesWithAbsoluteRecordPositions(t *testing.T) {
	content := []byte("{}\n{}\n{}\n")
	source := testSource(content)
	checkpoint := testEnvelopeForSource(source, 0, 0, []byte("{}\n")).Checkpoint
	var envelopes []RecordEnvelope

	err := streamSyntheticRecords(context.Background(), source, &checkpoint, sinkFunc(func(_ context.Context, envelope RecordEnvelope) error {
		envelopes = append(envelopes, envelope)
		return nil
	}))
	if err != nil {
		t.Fatalf("streamSyntheticRecords() error = %v", err)
	}
	if len(envelopes) != 2 {
		t.Fatalf("accepted envelopes = %d, want only two records after checkpoint", len(envelopes))
	}
	first := envelopes[0].RawRecord.Ref
	if first.RecordSequence == nil || *first.RecordSequence != 1 {
		t.Fatalf("first resumed record sequence = %v, want 1", first.RecordSequence)
	}
	resumeOffset := checkpointByteOffset(checkpoint)
	if first.ByteRange == nil || first.ByteRange.Offset != resumeOffset {
		t.Fatalf("first resumed byte range = %#v, want offset %d", first.ByteRange, resumeOffset)
	}
}

func TestLogicalDatabaseFingerprintUsesRowsNotOffsets(t *testing.T) {
	original := []logicalFixtureRow{{ID: "row-1", Value: "one"}, {ID: "row-2", Value: "two"}}
	checkpoint := logicalFixtureCheckpoint("source-1", "database-1", original)

	tests := []struct {
		name     string
		identity string
		rows     []logicalFixtureRow
		want     SourceChange
	}{
		{name: "unchanged", identity: "database-1", rows: original, want: SourceUnchanged},
		{name: "append", identity: "database-1", rows: append(append([]logicalFixtureRow(nil), original...), logicalFixtureRow{ID: "row-3", Value: "three"}), want: SourceAppend},
		{name: "truncation", identity: "database-1", rows: original[:1], want: SourceTruncated},
		{name: "mutation", identity: "database-1", rows: []logicalFixtureRow{{ID: "row-1", Value: "changed"}, {ID: "row-2", Value: "two"}}, want: SourceMutated},
		{name: "replacement", identity: "database-2", rows: original, want: SourceReplaced},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := verifyLogicalFixture(tt.identity, tt.rows, checkpoint); got != tt.want {
				t.Fatalf("verifyLogicalFixture() = %q, want %q", got, tt.want)
			}
		})
	}
}

type logicalFixtureRow struct {
	ID    string
	Value string
}

func logicalFixtureCheckpoint(sourceID model.SourceID, identity string, rows []logicalFixtureRow) ImportCheckpoint {
	cursor := "start"
	sequence := NoRecordSequence
	if len(rows) > 0 {
		cursor = rows[len(rows)-1].ID
		sequence = int64(len(rows) - 1)
	}
	return ImportCheckpoint{
		SourceID: sourceID, RecordSequence: sequence, StateVersion: "logical-rows-v1",
		Cursor: []byte(cursor), Fingerprint: logicalFixtureFingerprint(identity, rows),
	}
}

func verifyLogicalFixture(identity string, rows []logicalFixtureRow, checkpoint ImportCheckpoint) SourceChange {
	storedIdentity, storedHash, ok := bytes.Cut(checkpoint.Fingerprint, []byte{0})
	if checkpoint.StateVersion != "logical-rows-v1" || !ok || string(storedIdentity) != identity {
		return SourceReplaced
	}
	count := 0
	if string(checkpoint.Cursor) != "start" {
		for count < len(rows) && rows[count].ID != string(checkpoint.Cursor) {
			count++
		}
		if count == len(rows) {
			return SourceTruncated
		}
		count++
	}
	if !bytes.Equal(storedHash, logicalFixtureFingerprintHash(rows[:count])) {
		return SourceMutated
	}
	if count < len(rows) {
		return SourceAppend
	}
	return SourceUnchanged
}

func logicalFixtureFingerprint(identity string, rows []logicalFixtureRow) []byte {
	return append(append([]byte(identity), 0), logicalFixtureFingerprintHash(rows)...)
}

func logicalFixtureFingerprintHash(rows []logicalFixtureRow) []byte {
	var canonical bytes.Buffer
	for _, row := range rows {
		fmt.Fprintf(&canonical, "%d:%s%d:%s", len(row.ID), row.ID, len(row.Value), row.Value)
	}
	return []byte(model.HashRecord(canonical.Bytes()))
}

func TestStreamingImportHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	adapter := &fakeAdapter{importFn: streamSyntheticRecords}
	source := Source{ID: "source-cancelled", Size: int64(len(syntheticLine)), Open: func(context.Context) (io.ReadCloser, error) {
		return &syntheticRecordReader{remaining: 1}, nil
	}}
	err := adapter.runImport(ctx, source, nil, sinkFunc(func(context.Context, RecordEnvelope) error {
		t.Fatal("sink called after cancellation")
		return nil
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Import() error = %v, want context cancellation", err)
	}
}

func TestStreamingImportStopsBeforeCompleteOnSinkError(t *testing.T) {
	source := testSource([]byte(syntheticLine))
	stop := errors.New("stop delivery")
	sink := &lifecycleSink{acceptErr: stop}
	err := streamSyntheticRecords(context.Background(), source, nil, sink)
	if !errors.Is(err, stop) {
		t.Fatalf("streamSyntheticRecords() error = %v, want sink error", err)
	}
	if sink.calls != "begin,accept" {
		t.Fatalf("lifecycle calls = %q, want begin,accept without complete", sink.calls)
	}
}

func streamSyntheticRecords(ctx context.Context, source Source, resume *ImportCheckpoint, sink ImportSink) error {
	if err := source.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	session := testSession(source)
	if err := sink.Begin(ctx, session); err != nil {
		return fmt.Errorf("begin source %q session: %w", source.ID, err)
	}
	resumeOffset := int64(0)
	if resume != nil {
		resumeOffset = checkpointByteOffset(*resume)
	}
	stream, err := source.OpenFrom(ctx, resumeOffset)
	if err != nil {
		return fmt.Errorf("open source %q: %w", source.ID, err)
	}
	defer stream.Close()
	var sequence int64
	var offset int64
	if resume != nil {
		sequence = resume.RecordSequence + 1
		offset = checkpointByteOffset(*resume)
	}
	reader := bufio.NewReader(stream)
	completion := ImportCheckpoint{
		SourceID: source.ID, RecordSequence: NoRecordSequence, StateVersion: "fixture-stream-v1",
		Cursor: []byte("offset:0"), Fingerprint: []byte(model.HashRecord(nil)),
	}
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
			completion = envelope.Checkpoint
			offset += int64(len(record))
			sequence++
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if completeErr := sink.Complete(ctx, session, completion); completeErr != nil {
					return fmt.Errorf("complete source %q session: %w", source.ID, completeErr)
				}
				return nil
			}
			return fmt.Errorf("read source %q: %w", source.ID, err)
		}
	}
}

type lifecycleSink struct {
	calls     string
	initial   model.Session
	completed model.Session
	envelopes []RecordEnvelope
	acceptErr error
}

func (s *lifecycleSink) recordCall(call string) {
	if s.calls != "" {
		s.calls += ","
	}
	s.calls += call
}

func (s *lifecycleSink) Begin(_ context.Context, session model.Session) error {
	s.recordCall("begin")
	s.initial = session
	return nil
}

func (s *lifecycleSink) Accept(_ context.Context, envelope RecordEnvelope) error {
	s.recordCall("accept")
	s.envelopes = append(s.envelopes, envelope)
	return s.acceptErr
}

func (s *lifecycleSink) Complete(_ context.Context, session model.Session, _ ImportCheckpoint) error {
	s.recordCall("complete")
	s.completed = session
	return nil
}

func testSource(content []byte) Source {
	return Source{ID: "source-1", Size: int64(len(content)), Hint: "fixture-format", Open: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(content)), nil
	}}
}

func testSession(source Source) model.Session {
	return model.Session{
		ID: "session-fixture",
		Import: model.ImportMetadata{
			SourceID: source.ID, AdapterName: "fixture", AdapterVersion: "1",
			FormatVersion: "1", ModelVersion: "1", NormalizationVersion: "1",
		},
	}
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
			SourceID: source.ID, RecordSequence: sequence, StateVersion: "fixture-stream-v1",
			Cursor:      []byte(fmt.Sprintf("offset:%d", offset+int64(len(content)))),
			Fingerprint: []byte(fmt.Sprintf("sha256:prefix-%d", sequence)),
		},
	}
}

func checkpointByteOffset(checkpoint ImportCheckpoint) int64 {
	var offset int64
	if _, err := fmt.Sscanf(string(checkpoint.Cursor), "offset:%d", &offset); err != nil {
		panic(err)
	}
	return offset
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
