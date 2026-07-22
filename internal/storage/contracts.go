package storage

import (
	"context"
	"time"

	"github.com/pooya79/AgentSession/internal/model"
)

// SessionCursor is the storage-level keyset for the deterministic session list.
// Application cursors encode this value opaquely for presentation consumers.
type SessionCursor struct {
	StartedAt *time.Time
	ID        model.SessionID
}

// SessionSummary is lightweight imported-session metadata. It deliberately
// excludes producer metadata and retained evidence.
type SessionSummary struct {
	ID         model.SessionID
	Title      string
	Summary    string
	StartedAt  *time.Time
	EndedAt    *time.Time
	SourceID   model.SourceID
	EventCount int64
}

// EventEnvelope is event detail that is safe to fetch without normalized or
// retained payload content.
type EventEnvelope struct {
	model.EventSummary
	RawRecord model.RawRecordRef
}

// DiagnosticPage is a bounded synopsis with an exact total.
type DiagnosticPage struct {
	Diagnostics []model.Diagnostic
	Total       int64
}

// ExplorationReader is the narrow authoritative read contract consumed by
// the shared application explorer.
type ExplorationReader interface {
	ListSessions(context.Context, *SessionCursor, int) ([]SessionSummary, bool, error)
	SessionExists(context.Context, model.SessionID) (bool, error)
	EventSummaryPage(context.Context, model.SessionID, *int64, int) ([]model.EventSummary, bool, error)
	EventEnvelope(context.Context, model.SessionID, model.EventID) (EventEnvelope, bool, error)
	EventPayload(context.Context, model.SessionID, model.EventID) (model.NormalizedData, bool, error)
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
