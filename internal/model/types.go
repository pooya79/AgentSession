// Package model defines AgentSession's source-neutral domain types.
package model

import (
	"fmt"
	"strings"
	"time"
)

// SessionID identifies one canonical imported session.
type SessionID string

// EventID identifies one canonical event. Imported event IDs are generated
// deterministically by NewEventID.
type EventID string

// RawRecordID identifies an authoritative retained source record. It is an
// opaque domain identifier rather than a database key or file path.
type RawRecordID string

// FindingID identifies one versioned analysis finding.
type FindingID string

// SourceID identifies a discovered read-only session source independently of
// any particular record parsed from it.
type SourceID string

// Version is an opaque, producer-defined version label. It is deliberately
// not interpreted or ordered by the model package.
type Version string

// ImportMetadata identifies how a canonical session was produced.
type ImportMetadata struct {
	SourceID       SourceID
	AdapterName    string
	AdapterVersion Version

	// FormatVersion is the source format detected by the adapter. ModelVersion
	// identifies the canonical schema populated by normalization.
	FormatVersion Version
	ModelVersion  Version

	// NormalizationVersion changes when an adapter's mapping into the same
	// canonical model changes and existing records may need re-normalization.
	NormalizationVersion Version
}

// Session is the source-neutral representation of an imported agent session.
// Timestamps describe available evidence; they do not determine event order.
type Session struct {
	ID      SessionID
	Title   string
	Summary string

	// StartedAt and EndedAt are optional recorded evidence. They may be
	// inconsistent and must not be used to establish event order.
	StartedAt *time.Time
	EndedAt   *time.Time

	Import ImportMetadata

	// Diagnostics describe partial or unavailable evidence without making the
	// otherwise usable session invalid.
	Diagnostics []Diagnostic
}

// Validate checks the structural invariants of a session.
func (s Session) Validate() error {
	if strings.TrimSpace(string(s.ID)) == "" {
		return fmt.Errorf("session ID is required")
	}
	if err := s.Import.validate(); err != nil {
		return fmt.Errorf("session %q import metadata: %w", s.ID, err)
	}
	for i, diagnostic := range s.Diagnostics {
		if err := diagnostic.Validate(); err != nil {
			return fmt.Errorf("session %q diagnostic %d: %w", s.ID, i, err)
		}
	}
	return nil
}

func (m ImportMetadata) validate() error {
	fields := []struct {
		name  string
		value string
	}{
		{name: "source ID", value: string(m.SourceID)},
		{name: "adapter name", value: m.AdapterName},
		{name: "adapter version", value: string(m.AdapterVersion)},
		{name: "format version", value: string(m.FormatVersion)},
		{name: "model version", value: string(m.ModelVersion)},
		{name: "normalization version", value: string(m.NormalizationVersion)},
	}
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}
	return nil
}

// ByteRange identifies a raw record's byte extent within a source.
type ByteRange struct {
	// Offset is zero-based; Length must be positive.
	Offset int64
	Length int64
}

func (r ByteRange) validate() error {
	if r.Offset < 0 {
		return fmt.Errorf("byte offset must not be negative")
	}
	if r.Length <= 0 {
		return fmt.Errorf("byte length must be positive")
	}
	return nil
}

// RawRecordRef points to an authoritative raw record without embedding its
// potentially large or sensitive contents in a canonical event.
type RawRecordRef struct {
	ID       RawRecordID
	SourceID SourceID

	// RecordSequence and ByteRange describe the source location when known.
	// Either or both may be absent because not every format exposes both forms.
	RecordSequence *int64
	ByteRange      *ByteRange

	// ContentHash is the adapter-provided digest label for the original record.
	// The raw content itself is retained separately from this reference.
	ContentHash string
}

// RawRecord retains one original source record as authoritative import
// evidence. Content is untrusted input and must be sanitized at presentation
// and export boundaries.
type RawRecord struct {
	Ref     RawRecordRef
	Content []byte
}

// Validate checks the retained record metadata. Empty content is valid because
// an original source record may itself be empty.
func (r RawRecord) Validate() error {
	if err := r.Ref.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Ref.ContentHash) == "" {
		return fmt.Errorf("raw record content hash is required")
	}
	return nil
}

// Validate checks the structural invariants of a raw-record reference.
func (r RawRecordRef) Validate() error {
	if strings.TrimSpace(string(r.ID)) == "" {
		return fmt.Errorf("raw record ID is required")
	}
	if strings.TrimSpace(string(r.SourceID)) == "" {
		return fmt.Errorf("raw record source ID is required")
	}
	if r.RecordSequence != nil && *r.RecordSequence < 0 {
		return fmt.Errorf("raw record sequence must not be negative")
	}
	if r.ByteRange != nil {
		if err := r.ByteRange.validate(); err != nil {
			return fmt.Errorf("raw record range: %w", err)
		}
	}
	return nil
}
