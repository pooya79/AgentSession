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

// SourceOffsetOpener opens a read-only view positioned at offset. Discovery
// should provide this capability for seekable local sources so verified
// appends do not have to scan the committed prefix a second time.
type SourceOffsetOpener func(context.Context, int64) (io.ReadCloser, error)

// Source is the source-neutral input consumed by adapters. Hint is advisory
// discovery metadata; shared import code must not interpret it as a format.
type Source struct {
	ID   model.SourceID
	Size int64
	Hint string
	Open SourceOpener
	// OpenAt is optional for compatibility with non-seekable sources. When it
	// is absent, OpenFrom falls back to opening at zero and discarding bytes.
	OpenAt SourceOffsetOpener
}

// Validate checks the source metadata without opening the source.
func (s Source) Validate() error {
	if strings.TrimSpace(string(s.ID)) == "" {
		return fmt.Errorf("source ID is required")
	}
	if s.Size < 0 {
		return fmt.Errorf("source size must not be negative")
	}
	if s.Open == nil && s.OpenAt == nil {
		return fmt.Errorf("source opener or offset opener is required")
	}
	return nil
}

// OpenFrom opens a source at an absolute byte offset. An offset opener avoids
// re-reading an already verified prefix; the fallback remains streaming.
func (s Source) OpenFrom(ctx context.Context, offset int64) (io.ReadCloser, error) {
	if offset < 0 {
		return nil, fmt.Errorf("source offset must not be negative")
	}
	if s.OpenAt != nil {
		return s.OpenAt(ctx, offset)
	}
	if s.Open == nil {
		return nil, fmt.Errorf("source opener is required")
	}
	stream, err := s.Open(ctx)
	if err != nil {
		return nil, err
	}
	if offset == 0 {
		return stream, nil
	}
	if _, err := io.CopyN(io.Discard, stream, offset); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("position source at offset %d: %w", offset, err)
	}
	return stream, nil
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
	Verify(context.Context, Source, ImportCheckpoint) (CheckpointVerification, error)
	Import(context.Context, ImportRequest, ImportSink) error
}

// CheckpointVerification is the adapter-owned verdict for the committed
// source prefix. Shared orchestration never interprets source fingerprints.
type CheckpointVerification uint8

const (
	CheckpointChanged CheckpointVerification = iota
	CheckpointVerified
)

// ImportRequest supplies an adapter with the current source and, for an
// incremental append, the last checkpoint that import orchestration verified
// against that source. Resume must be nil for a first import or whenever the
// source was truncated, replaced, or changed before the committed cursor.
//
// A non-nil Resume lets an adapter preserve absolute byte offsets and record
// sequences while starting after already committed records. It is not itself
// proof that the source is unchanged: callers must perform the adapter-aware
// fingerprint verification before setting it.
type ImportRequest struct {
	Source Source
	Resume *ImportCheckpoint
}

// Validate checks source-neutral request invariants. It catches structurally
// impossible resume attempts, but cannot replace adapter-aware fingerprint
// verification of the source content.
func (r ImportRequest) Validate() error {
	if err := r.Source.Validate(); err != nil {
		return fmt.Errorf("validate source: %w", err)
	}
	if r.Resume == nil {
		return nil
	}
	if err := r.Resume.Validate(); err != nil {
		return fmt.Errorf("validate resume checkpoint: %w", err)
	}
	if r.Resume.SourceID != r.Source.ID {
		return fmt.Errorf("resume checkpoint source %q does not match import source %q", r.Resume.SourceID, r.Source.ID)
	}
	if r.Resume.SourceSize > r.Source.Size {
		return fmt.Errorf("resume checkpoint source size %d exceeds current source size %d", r.Resume.SourceSize, r.Source.Size)
	}
	return nil
}

// ImportSink consumes one canonical session lifecycle. Begin establishes the
// session before any records are delivered. Accept is synchronous, making its
// return the backpressure boundary. Complete publishes the authoritative
// enriched session snapshot after a successful import, including imports that
// retained malformed records but produced no events.
//
// An adapter must stop immediately on a sink error or context cancellation and
// must not call Complete after either condition.
type ImportSink interface {
	Begin(context.Context, model.Session) error
	Accept(context.Context, RecordEnvelope) error
	Complete(context.Context, model.Session, ImportCheckpoint) error
}

// RecordEnvelope keeps a retained source record, its canonical interpretation,
// recoverable diagnostics, and post-record checkpoint progress together.
type RecordEnvelope struct {
	RawRecord   model.RawRecord
	Events      []model.Event
	Diagnostics []model.Diagnostic
	Checkpoint  ImportCheckpoint
}

// RecordDiagnostics returns the envelope diagnostics with stable per-record
// ordinals for incremental batch persistence. The returned slice is detached
// from the envelope and may be retained by the sink until its current batch is
// committed.
func (e RecordEnvelope) RecordDiagnostics() []model.RecordDiagnostic {
	diagnostics := make([]model.RecordDiagnostic, len(e.Diagnostics))
	for i, diagnostic := range e.Diagnostics {
		diagnostic.EventIDs = append([]model.EventID(nil), diagnostic.EventIDs...)
		diagnostic.RawRecordIDs = append([]model.RawRecordID(nil), diagnostic.RawRecordIDs...)
		diagnostics[i] = model.RecordDiagnostic{
			RawRecordID: e.RawRecord.Ref.ID,
			Ordinal:     int64(i),
			Diagnostic:  diagnostic,
		}
	}
	return diagnostics
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

// ValidateForSession checks an envelope and verifies that its evidence belongs
// to the canonical session established for the import.
func (e RecordEnvelope) ValidateForSession(session model.Session) error {
	if err := session.Validate(); err != nil {
		return fmt.Errorf("validate session: %w", err)
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if e.RawRecord.Ref.SourceID != session.Import.SourceID {
		return fmt.Errorf("raw record source %q does not match session source %q", e.RawRecord.Ref.SourceID, session.Import.SourceID)
	}
	for i, event := range e.Events {
		if event.SessionID != session.ID {
			return fmt.Errorf("event %d belongs to session %q, want %q", i, event.SessionID, session.ID)
		}
	}
	return nil
}

// ValidateSessionTransition checks the immutable identity and normalization
// metadata shared by the initial and completed session snapshots. Completion
// may enrich display metadata, timestamps, and bounded session-level
// diagnostics, but cannot discard diagnostics that were already present at
// Begin. Record-level diagnostics travel through RecordEnvelope instead.
func ValidateSessionTransition(initial, completed model.Session) error {
	if err := initial.Validate(); err != nil {
		return fmt.Errorf("validate initial session: %w", err)
	}
	if err := completed.Validate(); err != nil {
		return fmt.Errorf("validate completed session: %w", err)
	}
	if completed.ID != initial.ID {
		return fmt.Errorf("completed session ID %q does not match initial session ID %q", completed.ID, initial.ID)
	}
	if completed.Import != initial.Import {
		return fmt.Errorf("completed session import metadata differs from initial session")
	}
	if len(completed.Diagnostics) < len(initial.Diagnostics) {
		return fmt.Errorf("completed session discarded initial diagnostics")
	}
	for i := range initial.Diagnostics {
		if !diagnosticEqual(initial.Diagnostics[i], completed.Diagnostics[i]) {
			return fmt.Errorf("completed session diagnostic %d differs from initial diagnostic", i)
		}
	}
	return nil
}

func diagnosticEqual(left, right model.Diagnostic) bool {
	if left.Code != right.Code || left.Severity != right.Severity || left.Message != right.Message {
		return false
	}
	if len(left.EventIDs) != len(right.EventIDs) || len(left.RawRecordIDs) != len(right.RawRecordIDs) {
		return false
	}
	for i := range left.EventIDs {
		if left.EventIDs[i] != right.EventIDs[i] {
			return false
		}
	}
	for i := range left.RawRecordIDs {
		if left.RawRecordIDs[i] != right.RawRecordIDs[i] {
			return false
		}
	}
	return true
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
