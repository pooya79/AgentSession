package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/pooya79/AgentSession/internal/model"
)

type batchCommitter interface {
	CommitBatch(context.Context, ImportBatch) error
}

type stagedCommitter struct{ Reconciliation }

func (s stagedCommitter) CommitBatch(ctx context.Context, batch ImportBatch) error {
	return s.StageBatch(ctx, batch)
}

type batchSink struct {
	store               batchCommitter
	options             Options
	source              Source
	state               SourceState
	hasState            bool
	result              *ImportResult
	progress            *progressTracker
	workPhase           ProgressPhase
	deferCommitProgress bool

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
		s.lastCheckpoint = cloneCheckpoint(s.state.Checkpoint)
		if s.state.LastEventSequence != nil {
			sequence := *s.state.LastEventSequence
			s.lastEventSequence = &sequence
		}
	}
	s.initial, s.current, s.begun = cloneSession(session), cloneSession(session), true
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
	s.batch.Checkpoint = cloneCheckpoint(envelope.Checkpoint)
	s.batchBytes += size
	s.lastCheckpoint = cloneCheckpoint(envelope.Checkpoint)
	if len(envelope.Events) > 0 {
		sequence := envelope.Events[len(envelope.Events)-1].Sequence
		s.lastEventSequence = &sequence
	}
	s.batchRecords++
	s.deliveredRecords++
	s.progress.processed(envelope, s.workPhase)
	return nil
}

func (s *batchSink) Complete(ctx context.Context, session model.Session, checkpoint ImportCheckpoint) error {
	if !s.begun || s.completed {
		return fmt.Errorf("import sink complete outside active lifecycle")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	session = cloneSession(session)
	if err := ValidateSessionTransition(s.initial, session); err != nil {
		return err
	}
	if err := checkpoint.Validate(); err != nil {
		return fmt.Errorf("validate completion checkpoint: %w", err)
	}
	if checkpoint.SourceID != s.source.ID {
		return fmt.Errorf("completion checkpoint does not describe source %q", s.source.ID)
	}
	if s.deliveredRecords > 0 {
		if !CheckpointEqual(checkpoint, s.lastCheckpoint) {
			return fmt.Errorf("%w: completion checkpoint differs from last delivered record", ErrInvalidProgress)
		}
	} else if s.hasState {
		if !CheckpointEqual(checkpoint, s.state.Checkpoint) {
			return fmt.Errorf("%w: completion checkpoint changed without a delivered record", ErrInvalidProgress)
		}
	}
	if len(s.batch.RawRecords) == 0 && s.hasState && CheckpointEqual(checkpoint, s.state.Checkpoint) && reflect.DeepEqual(session, s.state.Session) {
		s.current = session
		s.completed = true
		s.result.Checkpoint = cloneCheckpoint(checkpoint)
		s.progress.publish(s.workPhase, session.Diagnostics)
		return nil
	}
	s.batch.Checkpoint = cloneCheckpoint(checkpoint)
	s.lastCheckpoint = cloneCheckpoint(checkpoint)
	s.current = session
	if err := s.flush(ctx, session); err != nil {
		return err
	}
	s.completed = true
	s.result.Checkpoint = cloneCheckpoint(checkpoint)
	s.progress.publish(s.workPhase, session.Diagnostics)
	return nil
}

func (s *batchSink) validateProgress(envelope RecordEnvelope) error {
	checkpoint := envelope.Checkpoint
	if s.hasState || s.deliveredRecords > 0 {
		if checkpoint.RecordSequence <= s.lastCheckpoint.RecordSequence {
			return fmt.Errorf("%w: checkpoint sequence %d does not advance beyond %d", ErrInvalidProgress, checkpoint.RecordSequence, s.lastCheckpoint.RecordSequence)
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
	s.progress.publish(PhaseCommitting, nil)
	if err := s.store.CommitBatch(ctx, s.batch); err != nil {
		return fmt.Errorf("commit canonical batch: %w", err)
	}
	committedRecords := s.batchRecords
	s.result.BatchesCommitted++
	s.result.RecordsCommitted += s.batchRecords
	if !s.deferCommitProgress {
		s.progress.committed(committedRecords, 1, s.workPhase)
	}
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
