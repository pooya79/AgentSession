// Package importer defines import orchestration contracts and state.
package importer

import (
	"bytes"
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
	// ErrCheckpointConflict means reconciliation's expected live generation
	// changed before its staged replacement could be promoted.
	ErrCheckpointConflict = errors.New("import checkpoint changed during reconciliation")
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
)

// ImportCheckpoint identifies verified source progress. Cursor and Fingerprint
// are opaque, versioned adapter state; shared import code never interprets
// their source-specific encoding.
type ImportCheckpoint struct {
	SourceID       model.SourceID
	RecordSequence int64
	StateVersion   model.Version
	Cursor         []byte
	Fingerprint    []byte
}

// Validate checks the source-independent checkpoint invariants.
func (c ImportCheckpoint) Validate() error {
	if strings.TrimSpace(string(c.SourceID)) == "" {
		return fmt.Errorf("checkpoint source ID is required")
	}
	if c.RecordSequence < NoRecordSequence {
		return fmt.Errorf("checkpoint record sequence must not be less than %d", NoRecordSequence)
	}
	if strings.TrimSpace(string(c.StateVersion)) == "" {
		return fmt.Errorf("checkpoint state version is required")
	}
	if len(c.Cursor) == 0 {
		return fmt.Errorf("checkpoint adapter cursor is required")
	}
	if len(c.Fingerprint) == 0 {
		return fmt.Errorf("checkpoint adapter fingerprint is required")
	}
	return nil
}

func cloneCheckpoint(checkpoint ImportCheckpoint) ImportCheckpoint {
	clone := checkpoint
	clone.Cursor = append([]byte(nil), checkpoint.Cursor...)
	clone.Fingerprint = append([]byte(nil), checkpoint.Fingerprint...)
	return clone
}

// CheckpointEqual compares adapter-owned checkpoint bytes by value.
func CheckpointEqual(left, right ImportCheckpoint) bool {
	return left.SourceID == right.SourceID && left.RecordSequence == right.RecordSequence &&
		left.StateVersion == right.StateVersion && bytes.Equal(left.Cursor, right.Cursor) &&
		bytes.Equal(left.Fingerprint, right.Fingerprint)
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
	BeginReconciliation(ctx context.Context, sourceID model.SourceID, expected ImportCheckpoint) (Reconciliation, error)
	Checkpoint(ctx context.Context, sourceID model.SourceID) (ImportCheckpoint, bool, error)
	SourceState(ctx context.Context, sourceID model.SourceID) (SourceState, bool, error)
}

// ContainerMembershipStore atomically publishes the successful logical-source
// inventory for a physical container and removes canonical data for members
// that disappeared. Implementations only modify AgentSession-owned data.
type ContainerMembershipStore interface {
	SyncContainerMembers(context.Context, model.SourceID, []model.SourceID) error
}

// Reconciliation stages a complete replacement generation outside the live
// canonical tables and promotes it atomically after adapter completion.
type Reconciliation interface {
	StageBatch(context.Context, ImportBatch) error
	Finalize(context.Context) error
	Abort(context.Context) error
}
