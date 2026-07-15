// Package model defines AgentSession's source-neutral domain types.
package model

import (
	"fmt"
	"strings"
	"time"
)

type (
	SessionID   string
	EventID     string
	RawRecordID string
	FindingID   string
	SourceID    string
	Version     string
)

// ImportMetadata identifies how a canonical session was produced.
type ImportMetadata struct {
	SourceID             SourceID
	AdapterName          string
	AdapterVersion       Version
	FormatVersion        Version
	ModelVersion         Version
	NormalizationVersion Version
}

// Session is the source-neutral representation of an imported agent session.
// Timestamps describe available evidence; they do not determine event order.
type Session struct {
	ID          SessionID
	Title       string
	Summary     string
	StartedAt   *time.Time
	EndedAt     *time.Time
	Import      ImportMetadata
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
	ID             RawRecordID
	SourceID       SourceID
	RecordSequence *int64
	ByteRange      *ByteRange
	ContentHash    string
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
