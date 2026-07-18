// Package importer defines source-neutral adapter and import orchestration
// contracts. Source-specific implementations live outside this package.
package importer

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/pooya79/AgentSession/internal/model"
)

// SourceOpener opens a new read-only view of a source. Probe and Import each
// receive the same capability and must open and close their own view.
type SourceOpener func(context.Context) (io.ReadCloser, error)

// Source is the source-neutral input consumed by adapters. Hint is advisory
// discovery metadata; shared import code must not interpret it as a format.
type Source struct {
	ID   model.SourceID
	Size int64
	Hint string
	Open SourceOpener
}

// Validate checks the source metadata without opening the source.
func (s Source) Validate() error {
	if strings.TrimSpace(string(s.ID)) == "" {
		return fmt.Errorf("source ID is required")
	}
	if s.Size < 0 {
		return fmt.Errorf("source size must not be negative")
	}
	if s.Open == nil {
		return fmt.Errorf("source opener is required")
	}
	return nil
}

// ProbeConfidence expresses how strongly an adapter recognizes a source.
// Import orchestration may compare these values without knowing source names.
type ProbeConfidence uint8

const (
	ProbeUnsupported ProbeConfidence = iota
	ProbePossible
	ProbeCertain
)

// ProbeResult describes source recognition without producing canonical data.
type ProbeResult struct {
	Confidence    ProbeConfidence
	FormatVersion model.Version
	Diagnostics   []model.Diagnostic
}

// Validate checks source-neutral probe result invariants.
func (r ProbeResult) Validate() error {
	if r.Confidence > ProbeCertain {
		return fmt.Errorf("unsupported probe confidence %d", r.Confidence)
	}
	if r.Confidence == ProbeUnsupported && strings.TrimSpace(string(r.FormatVersion)) != "" {
		return fmt.Errorf("unsupported source must not report a format version")
	}
	if r.Confidence != ProbeUnsupported && strings.TrimSpace(string(r.FormatVersion)) == "" {
		return fmt.Errorf("recognized source requires a format version")
	}
	for i, diagnostic := range r.Diagnostics {
		if err := diagnostic.Validate(); err != nil {
			return fmt.Errorf("probe diagnostic %d: %w", i, err)
		}
	}
	return nil
}

// Adapter probes and incrementally normalizes one source format. The importer,
// as the consumer, owns this interface.
type Adapter interface {
	Name() string
	Version() model.Version
	Probe(context.Context, Source) (ProbeResult, error)
	Import(context.Context, Source, RecordSink) error
}

// RecordSink consumes one source record at a time. Accept is synchronous:
// returning from it is the backpressure boundary for the adapter.
type RecordSink interface {
	Accept(context.Context, RecordEnvelope) error
}

// RecordEnvelope keeps a retained source record, its canonical interpretation,
// recoverable diagnostics, and post-record checkpoint progress together.
type RecordEnvelope struct {
	RawRecord   model.RawRecord
	Events      []model.Event
	Diagnostics []model.Diagnostic
	Checkpoint  ImportCheckpoint
}

// Validate checks that all evidence in an envelope belongs to its raw record
// and lies at or before its post-record checkpoint.
func (e RecordEnvelope) Validate() error {
	if err := e.RawRecord.Validate(); err != nil {
		return fmt.Errorf("validate raw record: %w", err)
	}
	if err := e.Checkpoint.Validate(); err != nil {
		return fmt.Errorf("validate checkpoint: %w", err)
	}
	if e.RawRecord.Ref.SourceID != e.Checkpoint.SourceID {
		return fmt.Errorf("raw record source %q does not match checkpoint source %q", e.RawRecord.Ref.SourceID, e.Checkpoint.SourceID)
	}
	if got := model.HashRecord(e.RawRecord.Content); got != e.RawRecord.Ref.ContentHash {
		return fmt.Errorf("raw record content hash %q does not match content hash %q", e.RawRecord.Ref.ContentHash, got)
	}
	if err := validateEnvelopeCheckpoint(e.RawRecord.Ref, e.Checkpoint); err != nil {
		return err
	}
	if len(e.Events) == 0 && len(e.Diagnostics) == 0 {
		return fmt.Errorf("record envelope requires an event or diagnostic")
	}
	if err := model.ValidateEventOrder(e.Events); err != nil {
		return fmt.Errorf("validate event order: %w", err)
	}
	eventIDs := make(map[model.EventID]struct{}, len(e.Events))
	for i, event := range e.Events {
		if err := event.Validate(); err != nil {
			return fmt.Errorf("validate event %d: %w", i, err)
		}
		if !rawRecordRefEqual(event.RawRecord, e.RawRecord.Ref) {
			return fmt.Errorf("event %d raw record reference does not match retained record %q", i, e.RawRecord.Ref.ID)
		}
		eventIDs[event.ID] = struct{}{}
	}
	for i, diagnostic := range e.Diagnostics {
		if err := diagnostic.Validate(); err != nil {
			return fmt.Errorf("validate diagnostic %d: %w", i, err)
		}
		for _, id := range diagnostic.EventIDs {
			if _, ok := eventIDs[id]; !ok {
				return fmt.Errorf("diagnostic %d references event %q outside the record envelope", i, id)
			}
		}
		for _, id := range diagnostic.RawRecordIDs {
			if id != e.RawRecord.Ref.ID {
				return fmt.Errorf("diagnostic %d references raw record %q outside the record envelope", i, id)
			}
		}
	}
	return nil
}

func validateEnvelopeCheckpoint(ref model.RawRecordRef, checkpoint ImportCheckpoint) error {
	if ref.RecordSequence == nil && ref.ByteRange == nil {
		return fmt.Errorf("delivered raw record requires a record sequence or byte range")
	}
	if checkpoint.LastRecordHash != ref.ContentHash {
		return fmt.Errorf("checkpoint last record hash %q does not match delivered record hash %q", checkpoint.LastRecordHash, ref.ContentHash)
	}
	if sequence := ref.RecordSequence; sequence != nil && checkpoint.RecordSequence != *sequence {
		return fmt.Errorf("checkpoint sequence %d does not match delivered record sequence %d", checkpoint.RecordSequence, *sequence)
	}
	if byteRange := ref.ByteRange; byteRange != nil {
		end, err := byteRange.End()
		if err != nil {
			return fmt.Errorf("delivered raw byte range: %w", err)
		}
		if checkpoint.ByteOffset != end {
			return fmt.Errorf("checkpoint byte offset %d does not match delivered raw byte range end %d", checkpoint.ByteOffset, end)
		}
	}
	return nil
}
