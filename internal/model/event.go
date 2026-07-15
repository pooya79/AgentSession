package model

import (
	"fmt"
	"strings"
	"time"
)

// EventKind is a normalized category of session evidence.
type EventKind string

const (
	EventKindMessage      EventKind = "message"
	EventKindToolCall     EventKind = "tool_call"
	EventKindToolResult   EventKind = "tool_result"
	EventKindCommand      EventKind = "command"
	EventKindFileRead     EventKind = "file_read"
	EventKindFileMutation EventKind = "file_mutation"
	EventKindPatch        EventKind = "patch"
	EventKindUsage        EventKind = "usage"
	EventKindError        EventKind = "error"
	EventKindSummary      EventKind = "summary"
	EventKindUnknown      EventKind = "unknown"
)

// EventKinds returns all supported canonical event kinds in stable order.
func EventKinds() []EventKind {
	return []EventKind{
		EventKindMessage,
		EventKindToolCall,
		EventKindToolResult,
		EventKindCommand,
		EventKindFileRead,
		EventKindFileMutation,
		EventKindPatch,
		EventKindUsage,
		EventKindError,
		EventKindSummary,
		EventKindUnknown,
	}
}

func (k EventKind) valid() bool {
	for _, candidate := range EventKinds() {
		if k == candidate {
			return true
		}
	}
	return false
}

// Event is one ordered unit of canonical session evidence.
type Event struct {
	ID        EventID
	SessionID SessionID

	// Sequence is the authoritative source order within the session. Timestamp
	// is optional evidence and never participates in ordering.
	Sequence  int64
	Timestamp *time.Time

	Kind EventKind

	// Summary is concise display text. SearchableText contains the normalized
	// text selected for indexing and must not include retained raw records.
	Summary        string
	SearchableText string

	// Data is a source-neutral payload whose concrete type must match Kind.
	Data NormalizedData

	// RawRecord points to the authoritative original record used to produce
	// this event; it does not embed the record contents.
	RawRecord RawRecordRef
}

// EventSummary is the lightweight form used by timeline listings.
type EventSummary struct {
	ID        EventID
	SessionID SessionID
	Sequence  int64
	Timestamp *time.Time
	Kind      EventKind
	Summary   string
}

// Validate checks an event without interpreting source-specific content.
func (e Event) Validate() error {
	if strings.TrimSpace(string(e.ID)) == "" {
		return fmt.Errorf("event ID is required")
	}
	if strings.TrimSpace(string(e.SessionID)) == "" {
		return fmt.Errorf("event session ID is required")
	}
	if e.Sequence < 0 {
		return fmt.Errorf("event sequence must not be negative")
	}
	if !e.Kind.valid() {
		return fmt.Errorf("unsupported event kind %q", e.Kind)
	}
	if strings.TrimSpace(e.Summary) == "" {
		return fmt.Errorf("event summary is required")
	}
	if e.Data == nil {
		return fmt.Errorf("normalized data is required")
	}
	if got := e.Data.eventKind(); got != e.Kind {
		return fmt.Errorf("event kind %q does not match normalized data kind %q", e.Kind, got)
	}
	if err := validateNormalizedData(e.Data); err != nil {
		return fmt.Errorf("event %q normalized data: %w", e.ID, err)
	}
	if err := e.RawRecord.Validate(); err != nil {
		return fmt.Errorf("event %q raw record: %w", e.ID, err)
	}
	return nil
}

// ValidateEventOrder verifies a strictly ordered, duplicate-free timeline.
// Timestamp values are deliberately ignored.
func ValidateEventOrder(events []Event) error {
	if len(events) == 0 {
		return nil
	}

	sessionID := events[0].SessionID
	seen := make(map[EventID]struct{}, len(events))
	var previous int64
	for i, event := range events {
		if event.SessionID != sessionID {
			return fmt.Errorf("event %d belongs to session %q, want %q", i, event.SessionID, sessionID)
		}
		if event.Sequence < 0 {
			return fmt.Errorf("event %d sequence must not be negative", i)
		}
		if i > 0 && event.Sequence <= previous {
			return fmt.Errorf("event %d sequence %d is not greater than %d", i, event.Sequence, previous)
		}
		if _, exists := seen[event.ID]; exists {
			return fmt.Errorf("event %d repeats ID %q", i, event.ID)
		}
		seen[event.ID] = struct{}{}
		previous = event.Sequence
	}
	return nil
}
