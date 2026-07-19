package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/pooya79/AgentSession/internal/model"
)

const (
	DefaultBatchRecords   = 256
	DefaultBatchBytes     = 8 << 20
	DefaultMaxRecordBytes = 64 << 20
)

var (
	ErrUnsupportedSource = errors.New("no adapter supports import source")
	ErrAmbiguousSource   = errors.New("multiple adapters equally support import source")
	ErrRecordTooLarge    = errors.New("import record exceeds configured limit")
	ErrInvalidProgress   = errors.New("adapter produced invalid import progress")
)

// Options bounds the evidence retained by the coordinator before a commit.
type Options struct {
	BatchRecords   int
	BatchBytes     int64
	MaxRecordBytes int64
}

func (o Options) withDefaults() Options {
	if o.BatchRecords == 0 {
		o.BatchRecords = DefaultBatchRecords
	}
	if o.BatchBytes == 0 {
		o.BatchBytes = DefaultBatchBytes
	}
	if o.MaxRecordBytes == 0 {
		o.MaxRecordBytes = DefaultMaxRecordBytes
	}
	return o
}

func (o Options) validate() error {
	if o.BatchRecords <= 0 || o.BatchBytes <= 0 || o.MaxRecordBytes <= 0 {
		return fmt.Errorf("import limits must be positive")
	}
	return nil
}

// ProjectionRequest identifies authoritative state that is already durable.
type ProjectionRequest struct {
	SourceID   model.SourceID
	SessionID  model.SessionID
	Checkpoint ImportCheckpoint
}

// Projector performs rebuildable work after authoritative importing succeeds.
type Projector interface {
	Project(context.Context, ProjectionRequest) error
}

// ImportResult distinguishes canonical import success from projection health.
type ImportResult struct {
	SourceID         model.SourceID
	SessionID        model.SessionID
	Checkpoint       ImportCheckpoint
	RecordsCommitted int64
	BatchesCommitted int64
	CanonicalChanged bool
	ProjectionError  error
}

// Coordinator selects adapters and turns their synchronous stream into atomic
// canonical batches. It is safe to construct once, but callers must serialize
// imports for the same source at the application-work layer.
type Coordinator struct {
	store     ImportStore
	adapters  []Adapter
	projector Projector
	options   Options
}

func NewCoordinator(store ImportStore, adapters []Adapter, projector Projector, options Options) (*Coordinator, error) {
	if store == nil {
		return nil, fmt.Errorf("import coordinator: store is required")
	}
	options = options.withDefaults()
	if err := options.validate(); err != nil {
		return nil, fmt.Errorf("import coordinator: options: %w", err)
	}
	if len(adapters) == 0 {
		return nil, fmt.Errorf("import coordinator: at least one adapter is required")
	}
	seen := make(map[string]struct{}, len(adapters))
	for i, adapter := range adapters {
		if adapter == nil || strings.TrimSpace(adapter.Name()) == "" || strings.TrimSpace(string(adapter.Version())) == "" {
			return nil, fmt.Errorf("import coordinator: adapter %d has incomplete identity", i)
		}
		identity := adapter.Name() + "\x00" + string(adapter.Version())
		if _, exists := seen[identity]; exists {
			return nil, fmt.Errorf("import coordinator: duplicate adapter %q version %q", adapter.Name(), adapter.Version())
		}
		seen[identity] = struct{}{}
	}
	return &Coordinator{store: store, adapters: append([]Adapter(nil), adapters...), projector: projector, options: options}, nil
}

func (c *Coordinator) Import(ctx context.Context, source Source) (ImportResult, error) {
	result := ImportResult{SourceID: source.ID}
	if err := source.Validate(); err != nil {
		return result, fmt.Errorf("import source: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	adapter, probe, err := c.selectAdapter(ctx, source)
	if err != nil {
		return result, err
	}
	state, found, err := c.store.SourceState(ctx, source.ID)
	if err != nil {
		return result, fmt.Errorf("import source %q: load durable state: %w", source.ID, err)
	}
	var resume *ImportCheckpoint
	if found {
		if source.Size < state.Checkpoint.SourceSize {
			return result, fmt.Errorf("%w: source %q size %d is behind committed size %d", ErrSourceChanged, source.ID, source.Size, state.Checkpoint.SourceSize)
		}
		if state.Import.AdapterName != adapter.Name() || state.Import.AdapterVersion != adapter.Version() || state.Import.FormatVersion != probe.FormatVersion {
			return result, fmt.Errorf("%w: source %q was imported by %s/%s format %s", ErrIncompatibleImport, source.ID, state.Import.AdapterName, state.Import.AdapterVersion, state.Import.FormatVersion)
		}
		verification, verifyErr := adapter.Verify(ctx, source, state.Checkpoint)
		if verifyErr != nil {
			return result, fmt.Errorf("import source %q: verify checkpoint: %w", source.ID, verifyErr)
		}
		if verification != CheckpointVerified {
			return result, fmt.Errorf("%w: source %q", ErrSourceChanged, source.ID)
		}
		checkpoint := state.Checkpoint
		resume = &checkpoint
	}

	sink := &batchSink{
		store: c.store, options: c.options, source: source,
		state: state, hasState: found, result: &result,
	}
	request := ImportRequest{Source: source, Resume: resume}
	if err := request.Validate(); err != nil {
		return result, fmt.Errorf("import source %q: validate request: %w", source.ID, err)
	}
	if err := adapter.Import(ctx, request, sink); err != nil {
		return result, fmt.Errorf("import source %q with adapter %q: %w", source.ID, adapter.Name(), err)
	}
	if !sink.completed {
		return result, fmt.Errorf("import source %q with adapter %q: adapter returned without completion", source.ID, adapter.Name())
	}
	result.CanonicalChanged = result.BatchesCommitted > 0
	if c.projector != nil {
		result.ProjectionError = c.projector.Project(ctx, ProjectionRequest{
			SourceID: source.ID, SessionID: result.SessionID, Checkpoint: result.Checkpoint,
		})
	}
	return result, nil
}

func (c *Coordinator) selectAdapter(ctx context.Context, source Source) (Adapter, ProbeResult, error) {
	var selected Adapter
	var selectedProbe ProbeResult
	ambiguous := false
	for _, adapter := range c.adapters {
		probe, err := adapter.Probe(ctx, source)
		if err != nil {
			return nil, ProbeResult{}, fmt.Errorf("probe source %q with adapter %q: %w", source.ID, adapter.Name(), err)
		}
		if err := probe.Validate(); err != nil {
			return nil, ProbeResult{}, fmt.Errorf("probe source %q with adapter %q returned invalid result: %w", source.ID, adapter.Name(), err)
		}
		if probe.Confidence == ProbeUnsupported || probe.Confidence < selectedProbe.Confidence {
			continue
		}
		if selected != nil && probe.Confidence == selectedProbe.Confidence {
			ambiguous = true
			continue
		}
		selected, selectedProbe = adapter, probe
		ambiguous = false
	}
	if selected == nil {
		return nil, ProbeResult{}, fmt.Errorf("%w: %q", ErrUnsupportedSource, source.ID)
	}
	if ambiguous {
		return nil, ProbeResult{}, fmt.Errorf("%w: source %q has multiple matches at confidence %d", ErrAmbiguousSource, source.ID, selectedProbe.Confidence)
	}
	return selected, selectedProbe, nil
}

type batchSink struct {
	store    ImportStore
	options  Options
	source   Source
	state    SourceState
	hasState bool
	result   *ImportResult

	initial           model.Session
	current           model.Session
	begun             bool
	completed         bool
	batch             ImportBatch
	batchBytes        int64
	batchRecords      int64
	deliveredRecords  int64
	lastCheckpoint    ImportCheckpoint
	lastEventSequence *int64
}

func (s *batchSink) Begin(ctx context.Context, session model.Session) error {
	if s.begun {
		return fmt.Errorf("import sink begin called more than once")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := session.Validate(); err != nil {
		return fmt.Errorf("validate initial session: %w", err)
	}
	if session.Import.SourceID != s.source.ID {
		return fmt.Errorf("initial session source %q does not match %q", session.Import.SourceID, s.source.ID)
	}
	if s.hasState {
		if session.ID != s.state.SessionID || session.Import != s.state.Import {
			return fmt.Errorf("%w: source %q session or normalization identity changed", ErrIncompatibleImport, s.source.ID)
		}
		s.lastCheckpoint = s.state.Checkpoint
		if s.state.LastEventSequence != nil {
			sequence := *s.state.LastEventSequence
			s.lastEventSequence = &sequence
		}
	}
	s.initial, s.current, s.begun = session, session, true
	s.result.SessionID = session.ID
	return nil
}

func (s *batchSink) Accept(ctx context.Context, envelope RecordEnvelope) error {
	if !s.begun || s.completed {
		return fmt.Errorf("import sink accept outside active lifecycle")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := envelope.ValidateForSession(s.initial); err != nil {
		return fmt.Errorf("validate record envelope: %w", err)
	}
	if err := s.validateProgress(envelope); err != nil {
		return err
	}
	size, err := recordEnvelopeSize(envelope)
	if err != nil {
		return fmt.Errorf("measure record envelope %q: %w", envelope.RawRecord.Ref.ID, err)
	}
	if size > s.options.MaxRecordBytes {
		return fmt.Errorf("%w: record %q is %d bytes, limit %d", ErrRecordTooLarge, envelope.RawRecord.Ref.ID, size, s.options.MaxRecordBytes)
	}
	if len(s.batch.RawRecords) > 0 && (len(s.batch.RawRecords) >= s.options.BatchRecords || s.batchBytes+size > s.options.BatchBytes) {
		if err := s.flush(ctx, s.current); err != nil {
			return err
		}
	}
	owned := cloneRecordEnvelope(envelope)
	s.batch.RawRecords = append(s.batch.RawRecords, owned.RawRecord)
	s.batch.Events = append(s.batch.Events, owned.Events...)
	s.batch.RecordDiagnostics = append(s.batch.RecordDiagnostics, owned.RecordDiagnostics()...)
	s.batch.Checkpoint = envelope.Checkpoint
	s.batchBytes += size
	s.lastCheckpoint = envelope.Checkpoint
	if len(envelope.Events) > 0 {
		sequence := envelope.Events[len(envelope.Events)-1].Sequence
		s.lastEventSequence = &sequence
	}
	s.batchRecords++
	s.deliveredRecords++
	return nil
}

func (s *batchSink) Complete(ctx context.Context, session model.Session, checkpoint ImportCheckpoint) error {
	if !s.begun || s.completed {
		return fmt.Errorf("import sink complete outside active lifecycle")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateSessionTransition(s.initial, session); err != nil {
		return err
	}
	if err := checkpoint.Validate(); err != nil {
		return fmt.Errorf("validate completion checkpoint: %w", err)
	}
	if checkpoint.SourceID != s.source.ID || checkpoint.SourceSize != s.source.Size {
		return fmt.Errorf("completion checkpoint does not describe source %q size %d", s.source.ID, s.source.Size)
	}
	if s.deliveredRecords > 0 {
		if checkpoint != s.lastCheckpoint {
			return fmt.Errorf("%w: completion checkpoint differs from last delivered record", ErrInvalidProgress)
		}
	} else if s.hasState {
		previous := s.state.Checkpoint
		if checkpoint.RecordSequence != previous.RecordSequence || checkpoint.ByteOffset != previous.ByteOffset ||
			checkpoint.PrefixHash != previous.PrefixHash || checkpoint.LastRecordHash != previous.LastRecordHash ||
			checkpoint.SourceSize < previous.SourceSize {
			return fmt.Errorf("%w: completion checkpoint changed without a delivered record", ErrInvalidProgress)
		}
	}
	if len(s.batch.RawRecords) == 0 && s.hasState && checkpoint == s.state.Checkpoint && reflect.DeepEqual(session, s.state.Session) {
		s.current = session
		s.completed = true
		s.result.Checkpoint = checkpoint
		return nil
	}
	s.batch.Checkpoint = checkpoint
	s.lastCheckpoint = checkpoint
	s.current = session
	if err := s.flush(ctx, session); err != nil {
		return err
	}
	s.completed = true
	s.result.Checkpoint = checkpoint
	return nil
}

func (s *batchSink) validateProgress(envelope RecordEnvelope) error {
	checkpoint := envelope.Checkpoint
	if checkpoint.SourceSize != s.source.Size {
		return fmt.Errorf("%w: record checkpoint source size %d does not match %d", ErrInvalidProgress, checkpoint.SourceSize, s.source.Size)
	}
	if s.hasState || s.deliveredRecords > 0 {
		if checkpoint.RecordSequence <= s.lastCheckpoint.RecordSequence || checkpoint.ByteOffset < s.lastCheckpoint.ByteOffset {
			return fmt.Errorf("%w: checkpoint (%d,%d) does not advance beyond (%d,%d)", ErrInvalidProgress, checkpoint.ByteOffset, checkpoint.RecordSequence, s.lastCheckpoint.ByteOffset, s.lastCheckpoint.RecordSequence)
		}
	}
	for _, event := range envelope.Events {
		if s.lastEventSequence != nil && event.Sequence <= *s.lastEventSequence {
			return fmt.Errorf("%w: event sequence %d does not advance beyond %d", ErrInvalidProgress, event.Sequence, *s.lastEventSequence)
		}
		sequence := event.Sequence
		s.lastEventSequence = &sequence
	}
	return nil
}

func (s *batchSink) flush(ctx context.Context, session model.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.batch.Checkpoint.Validate(); err != nil {
		return fmt.Errorf("import sink has no valid checkpoint to commit: %w", err)
	}
	s.batch.Session = session
	if err := s.store.CommitBatch(ctx, s.batch); err != nil {
		return fmt.Errorf("commit canonical batch: %w", err)
	}
	s.result.BatchesCommitted++
	s.result.RecordsCommitted += s.batchRecords
	s.batch = ImportBatch{}
	s.batchBytes = 0
	s.batchRecords = 0
	return nil
}

func recordEnvelopeSize(envelope RecordEnvelope) (int64, error) {
	size := int64(len(envelope.RawRecord.Content))
	for _, event := range envelope.Events {
		encoded, err := json.Marshal(event.Data)
		if err != nil {
			return 0, err
		}
		size += int64(len(encoded) + len(event.Summary) + len(event.SearchableText))
	}
	for _, diagnostic := range envelope.Diagnostics {
		size += int64(len(diagnostic.Code) + len(diagnostic.Message))
		for _, id := range diagnostic.EventIDs {
			size += int64(len(id))
		}
		for _, id := range diagnostic.RawRecordIDs {
			size += int64(len(id))
		}
	}
	return size, nil
}

func cloneRecordEnvelope(envelope RecordEnvelope) RecordEnvelope {
	clone := envelope
	clone.RawRecord.Content = append([]byte(nil), envelope.RawRecord.Content...)
	clone.RawRecord.Ref = cloneRawRecordRef(envelope.RawRecord.Ref)
	clone.Events = make([]model.Event, len(envelope.Events))
	for i, event := range envelope.Events {
		clone.Events[i] = event
		clone.Events[i].RawRecord = cloneRawRecordRef(event.RawRecord)
		clone.Events[i].Data = cloneNormalizedData(event.Data)
	}
	clone.Diagnostics = make([]model.Diagnostic, len(envelope.Diagnostics))
	for i, diagnostic := range envelope.Diagnostics {
		clone.Diagnostics[i] = diagnostic
		clone.Diagnostics[i].EventIDs = append([]model.EventID(nil), diagnostic.EventIDs...)
		clone.Diagnostics[i].RawRecordIDs = append([]model.RawRecordID(nil), diagnostic.RawRecordIDs...)
	}
	return clone
}

func cloneRawRecordRef(ref model.RawRecordRef) model.RawRecordRef {
	clone := ref
	if ref.RecordSequence != nil {
		sequence := *ref.RecordSequence
		clone.RecordSequence = &sequence
	}
	if ref.ByteRange != nil {
		byteRange := *ref.ByteRange
		clone.ByteRange = &byteRange
	}
	return clone
}

func cloneNormalizedData(data model.NormalizedData) model.NormalizedData {
	switch value := data.(type) {
	case model.ToolResultData:
		if value.IsError != nil {
			copied := *value.IsError
			value.IsError = &copied
		}
		return value
	case model.CommandData:
		if value.ExitCode != nil {
			copied := *value.ExitCode
			value.ExitCode = &copied
		}
		return value
	case model.FileReadData:
		if value.StartLine != nil {
			copied := *value.StartLine
			value.StartLine = &copied
		}
		if value.EndLine != nil {
			copied := *value.EndLine
			value.EndLine = &copied
		}
		return value
	case model.PatchData:
		value.Paths = append([]string(nil), value.Paths...)
		return value
	case model.UsageData:
		value.InputTokens = cloneInt64(value.InputTokens)
		value.OutputTokens = cloneInt64(value.OutputTokens)
		value.CacheReadTokens = cloneInt64(value.CacheReadTokens)
		value.CacheWriteTokens = cloneInt64(value.CacheWriteTokens)
		return value
	default:
		return data
	}
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
