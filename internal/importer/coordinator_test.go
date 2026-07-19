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

	"github.com/pooya79/AgentSession/internal/model"
)

type coordinatorFixture struct {
	mu          sync.Mutex
	content     []byte
	openedAt    []int64
	verifyCalls int
}

func (f *coordinatorFixture) set(content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.content = []byte(content)
}

func (f *coordinatorFixture) source() Source {
	f.mu.Lock()
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
func (a *streamingFixtureAdapter) Verify(_ context.Context, _ Source, checkpoint ImportCheckpoint) (CheckpointVerification, error) {
	a.fixture.mu.Lock()
	defer a.fixture.mu.Unlock()
	a.fixture.verifyCalls++
	if checkpoint.ByteOffset > int64(len(a.fixture.content)) {
		return CheckpointChanged, nil
	}
	if checkpoint.PrefixHash != model.HashRecord(a.fixture.content[:checkpoint.ByteOffset]) {
		return CheckpointChanged, nil
	}
	return CheckpointVerified, nil
}
func (a *streamingFixtureAdapter) Import(ctx context.Context, request ImportRequest, sink ImportSink) error {
	session := model.Session{ID: "session-coordinator", Import: model.ImportMetadata{
		SourceID: request.Source.ID, AdapterName: a.Name(), AdapterVersion: a.Version(),
		FormatVersion: "1", ModelVersion: "1", NormalizationVersion: "1",
	}}
	if err := sink.Begin(ctx, session); err != nil {
		return err
	}
	offset, sequence := int64(0), int64(0)
	completion := ImportCheckpoint{SourceID: request.Source.ID, RecordSequence: NoRecordSequence, PrefixHash: model.HashRecord(nil), LastRecordHash: NoRecordHash, SourceSize: request.Source.Size}
	if request.Resume != nil {
		offset, sequence = request.Resume.ByteOffset, request.Resume.RecordSequence+1
		completion = *request.Resume
		completion.SourceSize = request.Source.Size
	}
	stream, err := request.Source.OpenFrom(ctx, offset)
	if err != nil {
		return err
	}
	defer stream.Close()
	reader := bufio.NewReader(stream)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		record, readErr := reader.ReadBytes('\n')
		if len(record) > 0 && record[len(record)-1] == '\n' {
			a.fixture.mu.Lock()
			prefix := append([]byte(nil), a.fixture.content[:offset+int64(len(record))]...)
			a.fixture.mu.Unlock()
			envelope := testEnvelopeForSource(request.Source, sequence, offset, record)
			envelope.Checkpoint.PrefixHash = model.HashRecord(prefix)
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
	s.state = SourceState{SessionID: batch.Session.ID, Import: batch.Session.Import, Session: batch.Session, Checkpoint: batch.Checkpoint}
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
func (s *memoryImportStore) ReconcileSource(context.Context, ImportBatch) error {
	return errors.New("unexpected reconcile")
}
func (s *memoryImportStore) Checkpoint(context.Context, model.SourceID) (ImportCheckpoint, bool, error) {
	return s.state.Checkpoint, s.hasState, nil
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
	if first.Checkpoint.ByteOffset != wantOffset {
		t.Fatalf("checkpoint offset = %d, want %d", first.Checkpoint.ByteOffset, wantOffset)
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
	if lastOpen != wantOffset {
		t.Fatalf("append opened at %d, want committed offset %d", lastOpen, wantOffset)
	}

	checkpoint := store.state.Checkpoint
	third, err := coordinator.Import(context.Background(), fixture.source())
	if err != nil {
		t.Fatalf("unchanged Import() error = %v", err)
	}
	if third.RecordsCommitted != 0 || store.state.Checkpoint != checkpoint || len(store.raw) != 4 || len(store.events) != 3 {
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
	if result.Checkpoint.RecordSequence != NoRecordSequence || result.Checkpoint.ByteOffset != 0 || result.Checkpoint.LastRecordHash != NoRecordHash {
		t.Fatalf("empty checkpoint = %#v", result.Checkpoint)
	}
	if len(store.raw) != 0 || store.commits != 1 {
		t.Fatalf("empty state raw=%d commits=%d", len(store.raw), store.commits)
	}
}

func TestCoordinatorChangedSourceStopsWithoutMutation(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("one\n")
	store := newMemoryImportStore()
	coordinator, _ := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, nil, Options{})
	if _, err := coordinator.Import(context.Background(), fixture.source()); err != nil {
		t.Fatal(err)
	}
	commits, checkpoint := store.commits, store.state.Checkpoint
	fixture.set("changed\n")
	_, err := coordinator.Import(context.Background(), fixture.source())
	if !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("Import() error = %v, want ErrSourceChanged", err)
	}
	if store.commits != commits || store.state.Checkpoint != checkpoint {
		t.Fatal("changed source mutated durable state")
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
