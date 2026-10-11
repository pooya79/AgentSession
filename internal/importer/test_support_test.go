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
	finalizeErr  error
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
	if r.store.finalizeErr != nil {
		return r.store.finalizeErr
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
