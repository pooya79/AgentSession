package storage

import (
	"context"

	"github.com/pooya79/AgentSession/internal/model"
)

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
