package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/storage"
)

const (
	DefaultPageSize       = 50
	MaximumPageSize       = 200
	DiagnosticSynopsisMax = 10
	maximumIdentifierSize = 512
)

var ErrInvalidRequest = errors.New("invalid exploration request")

type EvidenceState string

const (
	EvidenceComplete    EvidenceState = "complete"
	EvidencePartial     EvidenceState = "partial"
	EvidenceUnavailable EvidenceState = "unavailable"
	EvidenceNotFound    EvidenceState = "not_found"
)

type DiagnosticSynopsis struct {
	Diagnostics []model.Diagnostic
	Total       int64
	Omitted     int64
}

type SessionSummary struct {
	ID          model.SessionID
	Title       string
	Summary     string
	StartedAt   *time.Time
	EndedAt     *time.Time
	SourceID    model.SourceID
	EventCount  int64
	State       EvidenceState
	Diagnostics DiagnosticSynopsis
}

type ListSessionsRequest struct {
	Cursor string
	Limit  int
}

type SessionPage struct {
	State      EvidenceState
	Sessions   []SessionSummary
	NextCursor string
}

type TimelineRequest struct {
	SessionID model.SessionID
	Cursor    string
	Limit     int
}

type TimelinePage struct {
	State       EvidenceState
	Events      []model.EventSummary
	NextCursor  string
	Diagnostics DiagnosticSynopsis
}

type EventDetailRequest struct {
	SessionID      model.SessionID
	EventID        model.EventID
	IncludePayload bool
}

type EventDetail struct {
	State       EvidenceState
	Event       model.EventSummary
	RawRecord   model.RawRecordRef
	Payload     model.NormalizedData
	Diagnostics DiagnosticSynopsis
}

type Explorer interface {
	ListSessions(context.Context, ListSessionsRequest) (SessionPage, error)
	Timeline(context.Context, TimelineRequest) (TimelinePage, error)
	EventDetail(context.Context, EventDetailRequest) (EventDetail, error)
}

type explorationService struct{ reader storage.ExplorationReader }

func NewExplorer(reader storage.ExplorationReader) (Explorer, error) {
	if reader == nil {
		return nil, errors.New("application explorer: reader is required")
	}
	return &explorationService{reader: reader}, nil
}

func (s *explorationService) ListSessions(ctx context.Context, request ListSessionsRequest) (SessionPage, error) {
	if err := ctx.Err(); err != nil {
		return SessionPage{}, err
	}
	limit, err := pageLimit(request.Limit)
	if err != nil {
		return SessionPage{}, err
	}
	var after *storage.SessionCursor
	if request.Cursor != "" {
		cursor, err := decodeSessionCursor(request.Cursor)
		if err != nil {
			return SessionPage{}, err
		}
		after = &cursor
	}
	rows, more, err := s.reader.ListSessions(ctx, after, limit)
	if err != nil {
		return SessionPage{}, fmt.Errorf("list imported sessions: %w", err)
	}
	page := SessionPage{State: EvidenceComplete, Sessions: make([]SessionSummary, 0, len(rows))}
	for _, row := range rows {
		diagnostics, err := s.reader.Diagnostics(ctx, row.ID, nil, DiagnosticSynopsisMax)
		if err != nil {
			return SessionPage{}, fmt.Errorf("list imported sessions: diagnostics for %q: %w", row.ID, err)
		}
		synopsis := diagnosticSynopsis(diagnostics)
		state := EvidenceComplete
		if synopsis.Total > 0 {
			state = EvidencePartial
			page.State = EvidencePartial
		}
		page.Sessions = append(page.Sessions, SessionSummary{
			ID: row.ID, Title: row.Title, Summary: row.Summary, StartedAt: row.StartedAt, EndedAt: row.EndedAt,
			SourceID: row.SourceID, EventCount: row.EventCount, State: state, Diagnostics: synopsis,
		})
	}
	if more && len(rows) > 0 {
		last := rows[len(rows)-1]
		page.NextCursor, err = encodeCursor(cursorEnvelope{Kind: "sessions", SessionID: last.ID, StartedAt: formatCursorTime(last.StartedAt)})
		if err != nil {
			return SessionPage{}, err
		}
	}
	return page, nil
}

func (s *explorationService) Timeline(ctx context.Context, request TimelineRequest) (TimelinePage, error) {
	if err := validateIdentifier("session", string(request.SessionID)); err != nil {
		return TimelinePage{}, err
	}
	limit, err := pageLimit(request.Limit)
	if err != nil {
		return TimelinePage{}, err
	}
	exists, err := s.reader.SessionExists(ctx, request.SessionID)
	if err != nil {
		return TimelinePage{}, fmt.Errorf("read timeline for %q: %w", request.SessionID, err)
	}
	if !exists {
		return TimelinePage{State: EvidenceNotFound}, nil
	}
	var after *int64
	if request.Cursor != "" {
		sequence, err := decodeTimelineCursor(request.Cursor, request.SessionID)
		if err != nil {
			return TimelinePage{}, err
		}
		after = &sequence
	}
	rows, more, err := s.reader.EventSummaryPage(ctx, request.SessionID, after, limit)
	if err != nil {
		return TimelinePage{}, fmt.Errorf("read timeline for %q: %w", request.SessionID, err)
	}
	diagnostics, err := s.reader.Diagnostics(ctx, request.SessionID, nil, DiagnosticSynopsisMax)
	if err != nil {
		return TimelinePage{}, fmt.Errorf("read timeline diagnostics for %q: %w", request.SessionID, err)
	}
	synopsis := diagnosticSynopsis(diagnostics)
	state := EvidenceComplete
	if synopsis.Total > 0 {
		state = EvidencePartial
		if len(rows) == 0 && request.Cursor == "" {
			state = EvidenceUnavailable
		}
	}
	page := TimelinePage{State: state, Events: rows, Diagnostics: synopsis}
	if more && len(rows) > 0 {
		page.NextCursor, err = encodeCursor(cursorEnvelope{Kind: "timeline", SessionID: request.SessionID, Sequence: rows[len(rows)-1].Sequence})
		if err != nil {
			return TimelinePage{}, err
		}
	}
	return page, nil
}

func (s *explorationService) EventDetail(ctx context.Context, request EventDetailRequest) (EventDetail, error) {
	if err := validateIdentifier("session", string(request.SessionID)); err != nil {
		return EventDetail{}, err
	}
	if err := validateEventID(request.EventID); err != nil {
		return EventDetail{}, err
	}
	envelope, found, err := s.reader.EventEnvelope(ctx, request.SessionID, request.EventID)
	if err != nil {
		return EventDetail{}, fmt.Errorf("read event %q: %w", request.EventID, err)
	}
	if !found {
		return EventDetail{State: EvidenceNotFound}, nil
	}
	diagnostics, err := s.reader.Diagnostics(ctx, request.SessionID, &request.EventID, DiagnosticSynopsisMax)
	if err != nil {
		return EventDetail{}, fmt.Errorf("read event %q diagnostics: %w", request.EventID, err)
	}
	detail := EventDetail{State: EvidenceComplete, Event: envelope.EventSummary, RawRecord: envelope.RawRecord, Diagnostics: diagnosticSynopsis(diagnostics)}
	if detail.Diagnostics.Total > 0 {
		detail.State = EvidencePartial
	}
	if request.IncludePayload {
		payload, payloadFound, err := s.reader.EventPayload(ctx, request.SessionID, request.EventID)
		if err != nil {
			return EventDetail{}, fmt.Errorf("read event %q normalized payload: %w", request.EventID, err)
		}
		if !payloadFound {
			detail.State = EvidenceUnavailable
			return detail, nil
		}
		detail.Payload = payload
	}
	return detail, nil
}

type cursorEnvelope struct {
	Version   int             `json:"v"`
	Kind      string          `json:"kind"`
	SessionID model.SessionID `json:"session"`
	StartedAt *string         `json:"started_at,omitempty"`
	Sequence  int64           `json:"sequence,omitempty"`
}

func pageLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultPageSize, nil
	}
	if limit < 0 || limit > MaximumPageSize {
		return 0, fmt.Errorf("%w: page limit must be between 1 and %d", ErrInvalidRequest, MaximumPageSize)
	}
	return limit, nil
}

func encodeCursor(cursor cursorEnvelope) (string, error) {
	cursor.Version = 1
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode exploration cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeCursor(value string) (cursorEnvelope, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return cursorEnvelope{}, fmt.Errorf("%w: malformed cursor", ErrInvalidRequest)
	}
	var cursor cursorEnvelope
	if err := json.Unmarshal(decoded, &cursor); err != nil || cursor.Version != 1 {
		return cursorEnvelope{}, fmt.Errorf("%w: unsupported cursor", ErrInvalidRequest)
	}
	return cursor, nil
}

func decodeSessionCursor(value string) (storage.SessionCursor, error) {
	cursor, err := decodeCursor(value)
	if err != nil || cursor.Kind != "sessions" {
		return storage.SessionCursor{}, fmt.Errorf("%w: cursor does not belong to a session list", ErrInvalidRequest)
	}
	if err := validateIdentifier("session cursor", string(cursor.SessionID)); err != nil {
		return storage.SessionCursor{}, err
	}
	result := storage.SessionCursor{ID: cursor.SessionID}
	if cursor.StartedAt != nil {
		parsed, err := time.Parse(time.RFC3339Nano, *cursor.StartedAt)
		if err != nil {
			return storage.SessionCursor{}, fmt.Errorf("%w: malformed session cursor timestamp", ErrInvalidRequest)
		}
		result.StartedAt = &parsed
	}
	return result, nil
}

func decodeTimelineCursor(value string, sessionID model.SessionID) (int64, error) {
	cursor, err := decodeCursor(value)
	if err != nil || cursor.Kind != "timeline" || cursor.SessionID != sessionID || cursor.Sequence < 0 {
		return 0, fmt.Errorf("%w: cursor does not belong to this timeline", ErrInvalidRequest)
	}
	return cursor.Sequence, nil
}

func validateIdentifier(kind, value string) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > maximumIdentifierSize || !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s ID is malformed", ErrInvalidRequest, kind)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: %s ID is malformed", ErrInvalidRequest, kind)
		}
	}
	return nil
}

func validateEventID(id model.EventID) error {
	value := string(id)
	if len(value) != 68 || !strings.HasPrefix(value, "evt_") {
		return fmt.Errorf("%w: event ID is malformed", ErrInvalidRequest)
	}
	for _, r := range value[4:] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return fmt.Errorf("%w: event ID is malformed", ErrInvalidRequest)
		}
	}
	return nil
}

func diagnosticSynopsis(page storage.DiagnosticPage) DiagnosticSynopsis {
	omitted := page.Total - int64(len(page.Diagnostics))
	if omitted < 0 {
		omitted = 0
	}
	return DiagnosticSynopsis{Diagnostics: page.Diagnostics, Total: page.Total, Omitted: omitted}
}

func formatCursorTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}
