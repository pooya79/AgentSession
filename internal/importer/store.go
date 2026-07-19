// Package importer defines import orchestration contracts and state.
package importer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pooya79/AgentSession/internal/model"
)

var (
	// ErrEventConflict means a stable event ID is already associated with
	// different canonical evidence.
	ErrEventConflict = errors.New("import event conflicts with committed evidence")
	// ErrRawRecordConflict means a retained raw-record ID is already associated
	// with different source evidence.
	ErrRawRecordConflict = errors.New("import raw record conflicts with committed evidence")
	// ErrDiagnosticConflict means a stable record-diagnostic position is
	// already associated with different diagnostic evidence.
	ErrDiagnosticConflict = errors.New("import record diagnostic conflicts with committed evidence")
	// ErrCheckpointRegression means an ordinary import attempted to move a
	// verified source cursor behind its committed position.
	ErrCheckpointRegression = errors.New("import checkpoint would regress")
	// ErrSourceChanged means the committed prefix could not be verified.
	ErrSourceChanged = errors.New("import source changed before committed checkpoint")
	// ErrIncompatibleImport means persisted canonical data was produced by a
	// different adapter, format, model, or normalization version.
	ErrIncompatibleImport = errors.New("import metadata is incompatible with committed data")
)

const (
	// NoRecordSequence represents a checkpoint before the first complete
	// record. It is also used for empty and deferred-partial sources.
	NoRecordSequence int64 = -1
	// NoRecordHash is the last-record sentinel paired with NoRecordSequence.
	NoRecordHash = "none"
)

// ImportCheckpoint identifies verified source progress. The hashes make the
// cursor meaningful only for the source content that was inspected.
type ImportCheckpoint struct {
	SourceID       model.SourceID
	ByteOffset     int64
	RecordSequence int64
	PrefixHash     string
	LastRecordHash string
	SourceSize     int64
}

// Validate checks the source-independent checkpoint invariants.
func (c ImportCheckpoint) Validate() error {
	if strings.TrimSpace(string(c.SourceID)) == "" {
		return fmt.Errorf("checkpoint source ID is required")
	}
	if c.ByteOffset < 0 {
		return fmt.Errorf("checkpoint byte offset must not be negative")
	}
	if c.RecordSequence < NoRecordSequence {
		return fmt.Errorf("checkpoint record sequence must not be less than %d", NoRecordSequence)
	}
	if c.SourceSize < 0 {
		return fmt.Errorf("checkpoint source size must not be negative")
	}
	if c.ByteOffset > c.SourceSize {
		return fmt.Errorf("checkpoint byte offset %d exceeds source size %d", c.ByteOffset, c.SourceSize)
	}
	if strings.TrimSpace(c.PrefixHash) == "" {
		return fmt.Errorf("checkpoint prefix hash is required")
	}
	if strings.TrimSpace(c.LastRecordHash) == "" {
		return fmt.Errorf("checkpoint last record hash is required")
	}
	if c.RecordSequence == NoRecordSequence {
		if c.ByteOffset != 0 {
			return fmt.Errorf("zero-record checkpoint byte offset must be zero")
		}
		if c.LastRecordHash != NoRecordHash {
			return fmt.Errorf("zero-record checkpoint last record hash must be %q", NoRecordHash)
		}
	} else if c.LastRecordHash == NoRecordHash {
		return fmt.Errorf("record checkpoint must not use the no-record hash")
	}
	return nil
}

// SourceState is the durable import identity used to decide whether a source
// may safely resume without mixing normalization versions.
type SourceState struct {
	SessionID         model.SessionID
	Import            model.ImportMetadata
	Session           model.Session
	Checkpoint        ImportCheckpoint
	LastEventSequence *int64
}

// ImportBatch is the authoritative session snapshot and source evidence that
// must become durable with Checkpoint in one transaction.
type ImportBatch struct {
	Session           model.Session
	RawRecords        []model.RawRecord
	Events            []model.Event
	RecordDiagnostics []model.RecordDiagnostic
	Checkpoint        ImportCheckpoint
}

// Validate checks cross-record invariants before persistence starts.
func (b ImportBatch) Validate() error {
	if err := b.Session.Validate(); err != nil {
		return fmt.Errorf("validate session: %w", err)
	}
	if err := b.Checkpoint.Validate(); err != nil {
		return fmt.Errorf("validate checkpoint: %w", err)
	}
	if b.Session.Import.SourceID != b.Checkpoint.SourceID {
		return fmt.Errorf("session source %q does not match checkpoint source %q", b.Session.Import.SourceID, b.Checkpoint.SourceID)
	}
	if err := model.ValidateEventOrder(b.Events); err != nil {
		return fmt.Errorf("validate event order: %w", err)
	}
	rawRecords := make(map[model.RawRecordID]model.RawRecord, len(b.RawRecords))
	for i, rawRecord := range b.RawRecords {
		if err := rawRecord.Validate(); err != nil {
			return fmt.Errorf("validate raw record %d: %w", i, err)
		}
		if rawRecord.Ref.SourceID != b.Checkpoint.SourceID {
			return fmt.Errorf("raw record %d source %q does not match checkpoint source %q", i, rawRecord.Ref.SourceID, b.Checkpoint.SourceID)
		}
		if _, exists := rawRecords[rawRecord.Ref.ID]; exists {
			return fmt.Errorf("raw record %d repeats ID %q", i, rawRecord.Ref.ID)
		}
		if err := validateRawRecordPosition(rawRecord.Ref, b.Checkpoint); err != nil {
			return fmt.Errorf("raw record %d: %w", i, err)
		}
		rawRecords[rawRecord.Ref.ID] = rawRecord
	}
	events := make(map[model.EventID]model.Event, len(b.Events))
	for i, event := range b.Events {
		if err := event.Validate(); err != nil {
			return fmt.Errorf("validate event %d: %w", i, err)
		}
		if event.SessionID != b.Session.ID {
			return fmt.Errorf("event %d belongs to session %q, want %q", i, event.SessionID, b.Session.ID)
		}
		if event.RawRecord.SourceID != b.Checkpoint.SourceID {
			return fmt.Errorf("event %d raw source %q does not match checkpoint source %q", i, event.RawRecord.SourceID, b.Checkpoint.SourceID)
		}
		if err := validateRawRecordPosition(event.RawRecord, b.Checkpoint); err != nil {
			return fmt.Errorf("event %d: %w", i, err)
		}
		rawRecord, exists := rawRecords[event.RawRecord.ID]
		if !exists {
			return fmt.Errorf("event %d raw record %q is not retained in the batch", i, event.RawRecord.ID)
		}
		if !rawRecordRefEqual(rawRecord.Ref, event.RawRecord) {
			return fmt.Errorf("event %d raw record reference does not match retained record %q", i, event.RawRecord.ID)
		}
		events[event.ID] = event
	}
	diagnosticPositions := make(map[recordDiagnosticPosition]struct{}, len(b.RecordDiagnostics))
	for i, recordDiagnostic := range b.RecordDiagnostics {
		if err := recordDiagnostic.Validate(); err != nil {
			return fmt.Errorf("validate record diagnostic %d: %w", i, err)
		}
		if _, exists := rawRecords[recordDiagnostic.RawRecordID]; !exists {
			return fmt.Errorf("record diagnostic %d raw record %q is not retained in the batch", i, recordDiagnostic.RawRecordID)
		}
		position := recordDiagnosticPosition{rawRecordID: recordDiagnostic.RawRecordID, ordinal: recordDiagnostic.Ordinal}
		if _, exists := diagnosticPositions[position]; exists {
			return fmt.Errorf("record diagnostic %d repeats raw record %q ordinal %d", i, recordDiagnostic.RawRecordID, recordDiagnostic.Ordinal)
		}
		diagnosticPositions[position] = struct{}{}
		for _, eventID := range recordDiagnostic.Diagnostic.EventIDs {
			event, exists := events[eventID]
			if !exists {
				return fmt.Errorf("record diagnostic %d references event %q outside the batch", i, eventID)
			}
			if event.RawRecord.ID != recordDiagnostic.RawRecordID {
				return fmt.Errorf("record diagnostic %d references event %q from another raw record", i, eventID)
			}
		}
	}
	return nil
}

type recordDiagnosticPosition struct {
	rawRecordID model.RawRecordID
	ordinal     int64
}

func validateRawRecordPosition(ref model.RawRecordRef, checkpoint ImportCheckpoint) error {
	if sequence := ref.RecordSequence; sequence != nil && *sequence > checkpoint.RecordSequence {
		return fmt.Errorf("raw record sequence %d exceeds checkpoint sequence %d", *sequence, checkpoint.RecordSequence)
	}
	if byteRange := ref.ByteRange; byteRange != nil {
		end, err := byteRange.End()
		if err != nil {
			return fmt.Errorf("raw byte range: %w", err)
		}
		if end > checkpoint.ByteOffset {
			return fmt.Errorf("raw byte range ends at %d beyond checkpoint offset %d", end, checkpoint.ByteOffset)
		}
	}
	return nil
}

func rawRecordRefEqual(left, right model.RawRecordRef) bool {
	if left.ID != right.ID || left.SourceID != right.SourceID || left.ContentHash != right.ContentHash {
		return false
	}
	if (left.RecordSequence == nil) != (right.RecordSequence == nil) ||
		(left.RecordSequence != nil && *left.RecordSequence != *right.RecordSequence) {
		return false
	}
	if (left.ByteRange == nil) != (right.ByteRange == nil) {
		return false
	}
	return left.ByteRange == nil || *left.ByteRange == *right.ByteRange
}

// ImportStore is the persistence boundary consumed by import orchestration.
type ImportStore interface {
	CommitBatch(ctx context.Context, batch ImportBatch) error
	// ReconcileSource replaces all previously imported data for a source with
	// the supplied first batch. Callers must verify truncation, replacement, or
	// a required re-normalization before choosing this destructive index update.
	ReconcileSource(ctx context.Context, batch ImportBatch) error
	Checkpoint(ctx context.Context, sourceID model.SourceID) (ImportCheckpoint, bool, error)
	SourceState(ctx context.Context, sourceID model.SourceID) (SourceState, bool, error)
}
