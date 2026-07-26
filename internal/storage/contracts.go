package storage

import (
	"context"
	"time"

	"github.com/pooya79/AgentSession/internal/model"
)

// SessionCursor is the storage-level keyset for the deterministic session list.
// Application cursors encode this value opaquely for presentation consumers.
type SessionCursor struct {
	LastActivityAt *time.Time
	ID             model.SessionID
	// Before reverses the keyset comparison while results remain in canonical display order.
	Before bool
}

// SessionSummary is lightweight imported-session metadata. It deliberately
// excludes producer version metadata, normalized payloads, and retained
// evidence.
type SessionSummary struct {
	ID             model.SessionID
	Title          string
	Summary        string
	StartedAt      *time.Time
	EndedAt        *time.Time
	LastActivityAt *time.Time
	SourceID       model.SourceID
	AgentName      string
	// FirstUserMessage supports presentation previews without loading event payloads.
	FirstUserMessage string
	EventCount       int64
}

// LibraryOverview contains exact aggregate counts over committed canonical
// evidence. IssueSessions counts a session once even when diagnostics exist at
// both the session and record level.
type LibraryOverview struct {
	Sessions      int64
	Events        int64
	Agents        int64
	IssueSessions int64
}

// EventEnvelope is event detail that is safe to fetch without normalized or
// retained payload content.
type EventEnvelope struct {
	model.EventSummary
	RawRecord model.RawRecordRef
}

// EventLocation is the smallest canonical destination for an event reference.
type EventLocation struct {
	EventID   model.EventID
	SessionID model.SessionID
	Sequence  int64
}

// DiagnosticPage is a bounded synopsis with an exact total.
type DiagnosticPage struct {
	Diagnostics []model.Diagnostic
	Total       int64
}

// ExplorationReader is the narrow authoritative read contract consumed by
// the shared application explorer.
type ExplorationReader interface {
	// LibraryOverview returns exact committed counts.
	LibraryOverview(context.Context) (LibraryOverview, error)
	// ListSessions returns a keyset page plus whether more rows exist in the requested direction.
	ListSessions(context.Context, *SessionCursor, int) ([]SessionSummary, bool, error)
	// SessionExists checks canonical metadata without loading events.
	SessionExists(context.Context, model.SessionID) (bool, error)
	// EventSummaryPage returns ordered summaries after an optional sequence.
	EventSummaryPage(context.Context, model.SessionID, *int64, int) ([]model.EventSummary, bool, error)
	// EventSummaryWindow returns an ordered page ending at the requested sequence.
	EventSummaryWindow(context.Context, model.SessionID, int64, int) ([]model.EventSummary, bool, error)
	// EventLocations resolves ownership and order for a bounded set of IDs.
	EventLocations(context.Context, []model.EventID) (map[model.EventID]EventLocation, error)
	// EventEnvelope returns lightweight detail without normalized payload content.
	EventEnvelope(context.Context, model.SessionID, model.EventID) (EventEnvelope, bool, error)
	// EventPayload returns normalized data only for an explicitly selected event.
	EventPayload(context.Context, model.SessionID, model.EventID) (model.NormalizedData, bool, error)
	// Diagnostics returns an exact total with at most the requested number of entries.
	Diagnostics(context.Context, model.SessionID, *model.EventID, int) (DiagnosticPage, error)
}

// SessionReader exposes lightweight timelines separately from full evidence.
type SessionReader interface {
	Session(context.Context, model.SessionID) (model.Session, bool, error)
	RecordDiagnostics(context.Context, model.SessionID) ([]model.RecordDiagnostic, error)
	EventSummaries(context.Context, model.SessionID) ([]model.EventSummary, error)
	Event(context.Context, model.EventID) (model.Event, bool, error)
	RawRecord(context.Context, model.RawRecordID) (model.RawRecord, bool, error)
}

// SessionDeleter removes only AgentSession-owned imported data.
type SessionDeleter interface {
	DeleteSession(context.Context, model.SessionID) (bool, error)
}
