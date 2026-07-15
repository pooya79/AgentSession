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
	// ErrCheckpointRegression means an ordinary import attempted to move a
	// verified source cursor behind its committed position.
	ErrCheckpointRegression = errors.New("import checkpoint would regress")
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
	if c.RecordSequence < 0 {
		return fmt.Errorf("checkpoint record sequence must not be negative")
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
	return nil
}

// ImportBatch is the authoritative session snapshot and source evidence that
// must become durable with Checkpoint in one transaction.
type ImportBatch struct {
	Session    model.Session
	Events     []model.Event
	Checkpoint ImportCheckpoint
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
		if sequence := event.RawRecord.RecordSequence; sequence != nil && *sequence > b.Checkpoint.RecordSequence {
			return fmt.Errorf("event %d raw record sequence %d exceeds checkpoint sequence %d", i, *sequence, b.Checkpoint.RecordSequence)
		}
		if byteRange := event.RawRecord.ByteRange; byteRange != nil {
			end := byteRange.Offset + byteRange.Length
			if end < byteRange.Offset || end > b.Checkpoint.ByteOffset {
				return fmt.Errorf("event %d raw byte range ends at %d beyond checkpoint offset %d", i, end, b.Checkpoint.ByteOffset)
			}
		}
	}
	return nil
}

// ImportStore is the persistence boundary consumed by import orchestration.
type ImportStore interface {
	CommitBatch(ctx context.Context, batch ImportBatch) error
	Checkpoint(ctx context.Context, sourceID model.SourceID) (ImportCheckpoint, bool, error)
}
