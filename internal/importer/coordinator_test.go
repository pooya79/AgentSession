package importer

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/model"
)

type coordinatorFixture struct {
	mu                 sync.Mutex
	content            []byte
	openedAt           []int64
	verifyCalls        int
	afterVerify        func()
	identity           string
	reconcileStopAfter int
}

func (f *coordinatorFixture) set(content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.content = []byte(content)
}

func (f *coordinatorFixture) replace(identity, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identity = identity
	f.content = []byte(content)
}

func (f *coordinatorFixture) source() Source {
	f.mu.Lock()
	if f.identity == "" {
		f.identity = "fixture-source-1"
	}
	size := int64(len(f.content))
	f.mu.Unlock()
	return Source{ID: "source-coordinator", Size: size, Hint: "fixture", OpenAt: func(_ context.Context, offset int64) (io.ReadCloser, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if offset < 0 || offset > int64(len(f.content)) {
			return nil, fmt.Errorf("offset %d out of range", offset)
		}
		f.openedAt = append(f.openedAt, offset)
		return io.NopCloser(bytes.NewReader(f.content[offset:])), nil
	}}
}

type streamingFixtureAdapter struct{ fixture *coordinatorFixture }

func (*streamingFixtureAdapter) Name() string           { return "stream-fixture" }
func (*streamingFixtureAdapter) Version() model.Version { return "1" }
func (*streamingFixtureAdapter) Probe(context.Context, Source) (ProbeResult, error) {
	return ProbeResult{Confidence: ProbeCertain, FormatVersion: "1"}, nil
}
func (a *streamingFixtureAdapter) Prepare(ctx context.Context, source Source) (PreparedSource, error) {
	stream, err := source.OpenFrom(ctx, 0)
	if err != nil {
		return nil, err
	}
	content, err := io.ReadAll(stream)
	closeErr := stream.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	a.fixture.mu.Lock()
	identity := a.fixture.identity
	a.fixture.mu.Unlock()
	return &streamingFixtureView{adapter: a, source: source, content: content, identity: identity}, nil
}

type streamingFixtureView struct {
	adapter  *streamingFixtureAdapter
	source   Source
	content  []byte
	identity string
}

func (v *streamingFixtureView) Verify(_ context.Context, state SourceState) (SourceChange, error) {
	checkpoint := state.Checkpoint
	if checkpoint.StateVersion != "fixture-stream-v1" {
		return SourceReplaced, nil
	}
	offset := checkpointByteOffset(checkpoint)
	identity, hash, ok := bytes.Cut(checkpoint.Fingerprint, []byte{0})
	if !ok || string(identity) != v.identity {
		return SourceReplaced, nil
	}
	if offset > int64(len(v.content)) {
		return SourceTruncated, nil
	}
	change := SourceUnchanged
	if string(hash) != model.HashRecord(v.content[:offset]) {
		change = SourceMutated
	} else if offset < int64(len(v.content)) {
		change = SourceAppend
	}
	v.adapter.fixture.mu.Lock()
	v.adapter.fixture.verifyCalls++
	afterVerify := v.adapter.fixture.afterVerify
	v.adapter.fixture.mu.Unlock()
	if afterVerify != nil {
		afterVerify()
	}
	return change, nil
}

func (v *streamingFixtureView) Import(ctx context.Context, resume *ImportCheckpoint, sink ImportSink) error {
	return v.stream(ctx, resume, sink, false)
}

func (v *streamingFixtureView) Reconcile(ctx context.Context, sink ImportSink) error {
	return v.stream(ctx, nil, sink, true)
}

func (*streamingFixtureView) Close() error { return nil }

func (v *streamingFixtureView) stream(ctx context.Context, resume *ImportCheckpoint, sink ImportSink, reconciling bool) error {
	a := v.adapter
	requestSource := v.source
	session := model.Session{ID: "session-coordinator", Import: model.ImportMetadata{
		SourceID: requestSource.ID, AdapterName: a.Name(), AdapterVersion: a.Version(),
		FormatVersion: "1", ModelVersion: "1", NormalizationVersion: "1",
	}}
	if err := sink.Begin(ctx, session); err != nil {
		return err
	}
	offset, sequence := int64(0), int64(0)
	completion := fixtureCheckpoint(requestSource.ID, v.identity, NoRecordSequence, 0, nil)
	if resume != nil {
		offset, sequence = checkpointByteOffset(*resume), resume.RecordSequence+1
		completion = cloneCheckpoint(*resume)
	}
	if offset > int64(len(v.content)) {
		return ErrSourceChanged
	}
	prefix := append([]byte(nil), v.content[:offset]...)
	reader := bufio.NewReader(bytes.NewReader(v.content[offset:]))
	delivered := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		record, readErr := reader.ReadBytes('\n')
		if len(record) > 0 && record[len(record)-1] == '\n' {
			prefix = append(prefix, record...)
			envelope := testEnvelopeForSource(requestSource, sequence, offset, record)
			envelope.Checkpoint = fixtureCheckpoint(requestSource.ID, v.identity, sequence, offset+int64(len(record)), prefix)
			line := string(bytes.TrimSpace(record))
			switch line {
			case "bad":
				envelope.Diagnostics = []model.Diagnostic{{
					Code: "record.malformed", Severity: model.SeverityWarning, Message: "malformed fixture record",
					RawRecordIDs: []model.RawRecordID{envelope.RawRecord.Ref.ID},
				}}
			default:
				event := testUnknownEventForSource(envelope.RawRecord.Ref, sequence)
				event.SessionID = session.ID
				envelope.Events = []model.Event{event}
			}
			if err := sink.Accept(ctx, envelope); err != nil {
				return err
			}
			delivered++
			if reconciling && v.adapter.fixture.reconcileStopAfter > 0 && delivered == v.adapter.fixture.reconcileStopAfter {
				return context.Canceled
			}
			completion = envelope.Checkpoint
			offset += int64(len(record))
			sequence++
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return sink.Complete(ctx, session, completion)
			}
			return readErr
		}
	}
}

func fixtureCheckpoint(sourceID model.SourceID, identity string, sequence, offset int64, prefix []byte) ImportCheckpoint {
	return ImportCheckpoint{
		SourceID: sourceID, RecordSequence: sequence, StateVersion: "fixture-stream-v1",
		Cursor:      []byte(fmt.Sprintf("offset:%d", offset)),
		Fingerprint: append(append([]byte(identity), 0), []byte(model.HashRecord(prefix))...),
	}
}

type memoryImportStore struct {
	state        SourceState
	hasState     bool
	session      model.Session
	raw          map[model.RawRecordID]model.RawRecord
	events       map[model.EventID]model.Event
	diagnostics  []model.RecordDiagnostic
	commits      int
	maxRecords   int
	beforeCommit func(int, context.Context) error
}

func newMemoryImportStore() *memoryImportStore {
	return &memoryImportStore{raw: map[model.RawRecordID]model.RawRecord{}, events: map[model.EventID]model.Event{}}
}
func (s *memoryImportStore) CommitBatch(ctx context.Context, batch ImportBatch) error {
	if err := batch.Validate(); err != nil {
		return err
	}
	call := s.commits + 1
	if s.beforeCommit != nil {
		if err := s.beforeCommit(call, ctx); err != nil {
			return err
		}
	}
	s.commits++
	if len(batch.RawRecords) > s.maxRecords {
		s.maxRecords = len(batch.RawRecords)
	}
	s.session = batch.Session
	for _, raw := range batch.RawRecords {
		if old, ok := s.raw[raw.Ref.ID]; ok && !reflect.DeepEqual(old, raw) {
			return ErrRawRecordConflict
		}
		s.raw[raw.Ref.ID] = raw
	}
	for _, event := range batch.Events {
		if old, ok := s.events[event.ID]; ok && !reflect.DeepEqual(old, event) {
			return ErrEventConflict
		}
		s.events[event.ID] = event
	}
	s.diagnostics = append(s.diagnostics, batch.RecordDiagnostics...)
	s.state = SourceState{SessionID: batch.Session.ID, Import: batch.Session.Import, Session: batch.Session, Checkpoint: cloneCheckpoint(batch.Checkpoint)}
	if len(s.events) > 0 {
		last := int64(-1)
		for _, event := range s.events {
			if event.Sequence > last {
				last = event.Sequence
			}
		}
		s.state.LastEventSequence = &last
	}
	s.hasState = true
	return nil
}
func (s *memoryImportStore) BeginReconciliation(_ context.Context, sourceID model.SourceID, expected ImportCheckpoint) (Reconciliation, error) {
	if !s.hasState || sourceID != s.state.Checkpoint.SourceID || !CheckpointEqual(expected, s.state.Checkpoint) {
		return nil, ErrCheckpointConflict
	}
	return &memoryReconciliation{store: s, sourceID: sourceID}, nil
}

type memoryReconciliation struct {
	store    *memoryImportStore
	sourceID model.SourceID
	batches  []ImportBatch
	aborted  bool
}

func (r *memoryReconciliation) StageBatch(_ context.Context, batch ImportBatch) error {
	if r.aborted {
		return errors.New("reconciliation aborted")
	}
	r.batches = append(r.batches, batch)
	return nil
}

func (r *memoryReconciliation) Finalize(ctx context.Context) error {
	if r.aborted || len(r.batches) == 0 {
		return errors.New("reconciliation is incomplete")
	}
	r.store.raw = map[model.RawRecordID]model.RawRecord{}
	r.store.events = map[model.EventID]model.Event{}
	r.store.diagnostics = nil
	r.store.hasState = false
	for _, batch := range r.batches {
		if err := r.store.CommitBatch(ctx, batch); err != nil {
			return err
		}
	}
	return nil
}

func (r *memoryReconciliation) Abort(context.Context) error {
	r.aborted = true
	r.batches = nil
	return nil
}
func (s *memoryImportStore) Checkpoint(context.Context, model.SourceID) (ImportCheckpoint, bool, error) {
	return cloneCheckpoint(s.state.Checkpoint), s.hasState, nil
}
func (s *memoryImportStore) SourceState(context.Context, model.SourceID) (SourceState, bool, error) {
	return s.state, s.hasState, nil
}

type projectorFunc func(context.Context, ProjectionRequest) error

func (f projectorFunc) Project(ctx context.Context, request ProjectionRequest) error {
	return f(ctx, request)
}

func TestCoordinatorFirstImportAppendUnchangedMalformedUnknownAndPartial(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("one\nbad\nfuture\npartial")
	store := newMemoryImportStore()
	projectionCalls := 0
	coordinator, err := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, projectorFunc(func(context.Context, ProjectionRequest) error {
		projectionCalls++
		return nil
	}), Options{BatchRecords: 2})
	if err != nil {
		t.Fatal(err)
	}

	first, err := coordinator.Import(context.Background(), fixture.source())
	if err != nil {
		t.Fatalf("first Import() error = %v", err)
	}
	if first.RecordsCommitted != 3 || len(store.raw) != 3 || len(store.events) != 2 || len(store.diagnostics) != 1 {
		t.Fatalf("first import result/state = %#v, raw %d events %d diagnostics %d", first, len(store.raw), len(store.events), len(store.diagnostics))
	}
	wantOffset := int64(len("one\nbad\nfuture\n"))
	if got := checkpointByteOffset(first.Checkpoint); got != wantOffset {
		t.Fatalf("checkpoint offset = %d, want %d", got, wantOffset)
	}

	fixture.set("one\nbad\nfuture\npartial\n")
	second, err := coordinator.Import(context.Background(), fixture.source())
	if err != nil {
		t.Fatalf("append Import() error = %v", err)
	}
	if second.RecordsCommitted != 1 || len(store.raw) != 4 || len(store.events) != 3 {
		t.Fatalf("append result/state = %#v, raw %d events %d", second, len(store.raw), len(store.events))
	}
	fixture.mu.Lock()
	lastOpen := fixture.openedAt[len(fixture.openedAt)-1]
	fixture.mu.Unlock()
	if lastOpen != 0 {
		t.Fatalf("append snapshot opened original source at %d, want 0", lastOpen)
	}

	checkpoint := store.state.Checkpoint
	third, err := coordinator.Import(context.Background(), fixture.source())
	if err != nil {
		t.Fatalf("unchanged Import() error = %v", err)
	}
	if third.RecordsCommitted != 0 || !CheckpointEqual(store.state.Checkpoint, checkpoint) || len(store.raw) != 4 || len(store.events) != 3 {
		t.Fatalf("unchanged result/state changed: %#v", third)
	}
	if projectionCalls != 3 {
		t.Fatalf("projection calls = %d, want 3 successful imports", projectionCalls)
	}
}

func TestCoordinatorEmptySourceUsesZeroRecordCheckpoint(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("")
	store := newMemoryImportStore()
	coordinator, err := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Import(context.Background(), fixture.source())
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if result.Checkpoint.RecordSequence != NoRecordSequence || checkpointByteOffset(result.Checkpoint) != 0 {
		t.Fatalf("empty checkpoint = %#v", result.Checkpoint)
	}
	if len(store.raw) != 0 || store.commits != 1 {
		t.Fatalf("empty state raw=%d commits=%d", len(store.raw), store.commits)
	}
}

func TestCoordinatorChangedSourceReconciles(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("one\n")
	store := newMemoryImportStore()
	coordinator, _ := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, nil, Options{})
	if _, err := coordinator.Import(context.Background(), fixture.source()); err != nil {
		t.Fatal(err)
	}
	fixture.set("changed\n")
	result, err := coordinator.Import(context.Background(), fixture.source())
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if result.Change != SourceMutated || !result.Reconciled || len(store.raw) != 1 || len(store.events) != 1 {
		t.Fatalf("reconciled result=%#v raw=%d events=%d", result, len(store.raw), len(store.events))
	}
}

func TestCoordinatorClassifiesEverySourceChangeMode(t *testing.T) {
	tests := []struct {
		name       string
		change     SourceChange
		mutate     func(*coordinatorFixture)
		reconciled bool
		wantRaw    int
		wantEvents int
	}{
		{name: "unchanged", change: SourceUnchanged, mutate: func(*coordinatorFixture) {}, wantRaw: 2, wantEvents: 2},
		{name: "append", change: SourceAppend, mutate: func(f *coordinatorFixture) { f.set("one\ntwo\nthree\n") }, wantRaw: 3, wantEvents: 3},
		{name: "truncation", change: SourceTruncated, mutate: func(f *coordinatorFixture) { f.set("one\n") }, reconciled: true, wantRaw: 1, wantEvents: 1},
		{name: "mutation", change: SourceMutated, mutate: func(f *coordinatorFixture) { f.set("one\nbad\n") }, reconciled: true, wantRaw: 2, wantEvents: 1},
		{name: "replacement", change: SourceReplaced, mutate: func(f *coordinatorFixture) { f.replace("fixture-source-2", "new\n") }, reconciled: true, wantRaw: 1, wantEvents: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := &coordinatorFixture{}
			fixture.set("one\ntwo\n")
			store := newMemoryImportStore()
			coordinator, err := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, nil, Options{BatchRecords: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := coordinator.Import(context.Background(), fixture.source()); err != nil {
				t.Fatal(err)
			}
			tt.mutate(fixture)
			result, err := coordinator.Import(context.Background(), fixture.source())
			if err != nil {
				t.Fatalf("Import() error = %v", err)
			}
			if result.Change != tt.change || result.Reconciled != tt.reconciled {
				t.Fatalf("classification = (%q, reconciled %v), want (%q, %v)", result.Change, result.Reconciled, tt.change, tt.reconciled)
			}
			if len(store.raw) != tt.wantRaw || len(store.events) != tt.wantEvents {
				t.Fatalf("canonical evidence raw=%d events=%d, want %d and %d", len(store.raw), len(store.events), tt.wantRaw, tt.wantEvents)
			}
		})
	}
}

func TestCoordinatorInterruptionDuringRecoveryKeepsOldGeneration(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("one\ntwo\nthree\n")
	store := newMemoryImportStore()
	coordinator, err := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, nil, Options{BatchRecords: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Import(context.Background(), fixture.source()); err != nil {
		t.Fatal(err)
	}
	checkpoint := cloneCheckpoint(store.state.Checkpoint)
	fixture.set("changed\ntwo\nthree\n")
	fixture.reconcileStopAfter = 2
	if _, err := coordinator.Import(context.Background(), fixture.source()); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted reconciliation error = %v, want context.Canceled", err)
	}
	if !CheckpointEqual(store.state.Checkpoint, checkpoint) || len(store.raw) != 3 || len(store.events) != 3 {
		t.Fatalf("interrupted reconciliation exposed partial state: checkpoint=%#v raw=%d events=%d", store.state.Checkpoint, len(store.raw), len(store.events))
	}
	fixture.reconcileStopAfter = 0
	result, err := coordinator.Import(context.Background(), fixture.source())
	if err != nil || !result.Reconciled || result.Change != SourceMutated {
		t.Fatalf("reconciliation retry = (%#v, %v)", result, err)
	}
}

func TestCoordinatorResumeUsesVerifiedSourceSnapshot(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("one\n")
	store := newMemoryImportStore()
	adapter := &streamingFixtureAdapter{fixture: fixture}
	coordinator, _ := NewCoordinator(store, []Adapter{adapter}, nil, Options{})
	if _, err := coordinator.Import(context.Background(), fixture.source()); err != nil {
		t.Fatal(err)
	}

	fixture.set("one\ntwo\n")
	source := fixture.source()
	fixture.afterVerify = func() {
		fixture.set("one\nbad\n")
		fixture.afterVerify = nil
	}
	result, err := coordinator.Import(context.Background(), source)
	if err != nil {
		t.Fatalf("resumed Import() error = %v", err)
	}
	if result.RecordsCommitted != 1 || len(store.events) != 2 || len(store.diagnostics) != 0 {
		t.Fatalf("resumed snapshot result=%#v events=%d diagnostics=%d", result, len(store.events), len(store.diagnostics))
	}
	wantRecordID := testEnvelopeForSource(source, 1, int64(len("one\n")), []byte("two\n")).RawRecord.Ref.ID
	if _, ok := store.raw[wantRecordID]; !ok {
		t.Fatalf("resumed import did not retain verified snapshot record %q", wantRecordID)
	}
}

func TestBatchSinkRetainsImmutableInitialSession(t *testing.T) {
	source := testSource(nil)
	session := testSession(source)
	session.Diagnostics = []model.Diagnostic{{
		Code: "source.partial", Severity: model.SeverityWarning, Message: "original",
		EventIDs: []model.EventID{"event-1"}, RawRecordIDs: []model.RawRecordID{"record-1"},
	}}
	sink := &batchSink{source: source, result: &ImportResult{}, options: Options{}.withDefaults()}
	if err := sink.Begin(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	session.Diagnostics[0].Message = "changed"
	session.Diagnostics[0].EventIDs[0] = "event-2"
	session.Diagnostics[0].RawRecordIDs[0] = "record-2"

	checkpoint := ImportCheckpoint{
		SourceID: source.ID, RecordSequence: NoRecordSequence, StateVersion: "fixture-stream-v1",
		Cursor: []byte("offset:0"), Fingerprint: []byte(model.HashRecord(nil)),
	}
	if err := sink.Complete(context.Background(), session, checkpoint); err == nil {
		t.Fatal("Complete() accepted an adapter mutation of the initial session")
	}
}

func TestCloneRecordEnvelopeOwnsEventTimestamp(t *testing.T) {
	source := testSource([]byte("one\n"))
	envelope := testEnvelopeForSource(source, 0, 0, []byte("one\n"))
	event := testUnknownEventForSource(envelope.RawRecord.Ref, 0)
	event.SessionID = testSession(source).ID
	timestamp := time.Unix(100, 0).UTC()
	event.Timestamp = &timestamp
	envelope.Events = []model.Event{event}

	clone := cloneRecordEnvelope(envelope)
	timestamp = time.Unix(200, 0).UTC()
	if got := clone.Events[0].Timestamp; got == nil || !got.Equal(time.Unix(100, 0).UTC()) {
		t.Fatalf("cloned timestamp = %v, want original value", got)
	}
}

func TestCoordinatorCancellationKeepsOnlyEarlierBatches(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("one\ntwo\nthree\n")
	store := newMemoryImportStore()
	ctx, cancel := context.WithCancel(context.Background())
	store.beforeCommit = func(call int, _ context.Context) error {
		if call == 2 {
			cancel()
			return context.Canceled
		}
		return nil
	}
	coordinator, _ := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, nil, Options{BatchRecords: 1})
	_, err := coordinator.Import(ctx, fixture.source())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Import() error = %v, want cancellation", err)
	}
	if len(store.raw) != 1 || store.state.Checkpoint.RecordSequence != 0 {
		t.Fatalf("durable records=%d checkpoint=%#v", len(store.raw), store.state.Checkpoint)
	}
}

func TestCoordinatorBoundsLargeSourceAndRejectsOversizedRecord(t *testing.T) {
	fixture := &coordinatorFixture{}
	var source bytes.Buffer
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&source, "record-%04d\n", i)
	}
	fixture.set(source.String())
	store := newMemoryImportStore()
	coordinator, _ := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, nil, Options{BatchRecords: 10, BatchBytes: 1024, MaxRecordBytes: 1024})
	result, err := coordinator.Import(context.Background(), fixture.source())
	if err != nil {
		t.Fatalf("large Import() error = %v", err)
	}
	if result.RecordsCommitted != 1000 || store.maxRecords > 10 {
		t.Fatalf("result=%#v max batch records=%d", result, store.maxRecords)
	}

	fixture2 := &coordinatorFixture{}
	fixture2.set("this-record-is-too-large\n")
	store2 := newMemoryImportStore()
	coordinator2, _ := NewCoordinator(store2, []Adapter{&streamingFixtureAdapter{fixture2}}, nil, Options{MaxRecordBytes: 8})
	_, err = coordinator2.Import(context.Background(), fixture2.source())
	if !errors.Is(err, ErrRecordTooLarge) || store2.commits != 0 {
		t.Fatalf("oversized error=%v commits=%d", err, store2.commits)
	}
}

func TestCoordinatorProjectionFailureIsSeparateFromCanonicalSuccess(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("one\n")
	store := newMemoryImportStore()
	projectionErr := errors.New("projection unavailable")
	coordinator, _ := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, projectorFunc(func(context.Context, ProjectionRequest) error { return projectionErr }), Options{})
	result, err := coordinator.Import(context.Background(), fixture.source())
	if err != nil {
		t.Fatalf("Import() canonical error = %v", err)
	}
	if !errors.Is(result.ProjectionError, projectionErr) || len(store.raw) != 1 {
		t.Fatalf("result=%#v raw=%d", result, len(store.raw))
	}
}

func TestCoordinatorObservedProgressIncludesCountsDiagnosticsAndPhases(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("bad\none\n")
	store := newMemoryImportStore()
	coordinator, _ := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, nil, Options{BatchRecords: 1})
	var progress []Progress
	results, err := coordinator.ImportAllObserved(context.Background(), fixture.source(), func(update Progress) {
		progress = append(progress, update)
	})
	if err != nil || len(results) != 1 {
		t.Fatalf("ImportAllObserved() = (%#v, %v)", results, err)
	}
	if len(progress) == 0 {
		t.Fatal("ImportAllObserved() emitted no progress")
	}
	last := progress[len(progress)-1]
	if last.SourceID != fixture.source().ID || last.ActiveSourceID != fixture.source().ID || last.Phase != PhaseFinalizing {
		t.Fatalf("last progress identity/phase = %#v", last)
	}
	if last.RecordsProcessed != 2 || last.EventsProcessed != 1 || last.RecordsCommitted != 2 || last.BatchesCommitted != 2 || last.DiagnosticsObserved != 1 {
		t.Fatalf("last progress counts = %#v", last)
	}
	seenDiagnostic := false
	seenCommit := false
	for _, update := range progress {
		seenCommit = seenCommit || update.Phase == PhaseCommitting
		seenDiagnostic = seenDiagnostic || len(update.Diagnostics) == 1 && update.Diagnostics[0].Code == "record.malformed"
	}
	if !seenDiagnostic || !seenCommit {
		t.Fatalf("progress missing diagnostic or commit phase: %#v", progress)
	}
}
