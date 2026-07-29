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
	"github.com/pooya79/AgentSession/internal/redaction"
	"github.com/pooya79/AgentSession/internal/storage"
)

const (
	// DefaultPageSize is used when an exploration request does not specify a
	// limit.
	DefaultPageSize = 50
	// MaximumPageSize bounds exploration reads and presentation memory use.
	MaximumPageSize = 200
	// DiagnosticSynopsisMax bounds the diagnostic evidence returned inline.
	DiagnosticSynopsisMax = 10
	// SessionPreviewMaxRunes bounds human-readable session previews without
	// splitting a UTF-8 code point.
	SessionPreviewMaxRunes = 240
	// maximumIdentifierSize bounds untrusted route and cursor identifiers.
	maximumIdentifierSize = 512
	// UnknownInspectionMaxBytes is the fixed decoded-content cap for explicit
	// retained Unknown-evidence inspection.
	UnknownInspectionMaxBytes = 64 * 1024
)

// ErrInvalidRequest marks exploration input rejected before a storage read.
var ErrInvalidRequest = errors.New("invalid exploration request")

// EvidenceState describes how completely retained canonical evidence supports a view.
type EvidenceState string

const (
	// EvidenceComplete means the requested canonical evidence is available without diagnostics.
	EvidenceComplete EvidenceState = "complete"
	// EvidencePartial means usable evidence is accompanied by retained diagnostics.
	EvidencePartial EvidenceState = "partial"
	// EvidenceUnavailable means a known session or event lacks the requested normalized evidence.
	EvidenceUnavailable EvidenceState = "unavailable"
	// EvidenceNotFound means the requested canonical identifier is not retained.
	EvidenceNotFound EvidenceState = "not_found"
)

// DiagnosticSynopsis is a bounded diagnostic sample with an exact total.
type DiagnosticSynopsis struct {
	Diagnostics []model.Diagnostic
	Total       int64
	Omitted     int64
}

// InterpretationCoverageState describes interpretation independently of
// canonical evidence integrity and session outcome.
type InterpretationCoverageState string

const (
	// InterpretationFullyInterpreted means every committed record was either
	// mapped to typed canonical evidence or intentionally ignored as metadata.
	InterpretationFullyInterpreted InterpretationCoverageState = "fully_interpreted"
	// InterpretationPartiallyInterpreted means committed evidence contains at
	// least one Unknown event or categorized malformed record.
	InterpretationPartiallyInterpreted InterpretationCoverageState = "partially_interpreted"
)

// InterpretationCoverage contains exact committed coverage counts. It is
// independent of EvidenceState and any derived session outcome.
type InterpretationCoverage struct {
	State            InterpretationCoverageState
	UnknownEvents    int64
	MalformedRecords int64
}

// SessionSummary is the presentation-safe canonical evidence summary for one session.
type SessionSummary struct {
	ID             model.SessionID
	Title          string
	Summary        string
	StartedAt      *time.Time
	EndedAt        *time.Time
	LastActivityAt *time.Time
	SourceID       model.SourceID
	AgentName      string
	Preview        string
	EventCount     int64
	State          EvidenceState
	Diagnostics    DiagnosticSynopsis
	Interpretation InterpretationCoverage
	// Projections reports derived-data readiness separately from State, which
	// describes canonical evidence only.
	Projections ProjectionSummary
}

// LibraryOverview contains exact counts over committed canonical evidence.
// IssueSessions counts affected sessions rather than individual diagnostics.
type LibraryOverview struct {
	Sessions      int64
	Events        int64
	Agents        int64
	IssueSessions int64
}

// ListSessionsRequest carries an opaque cursor and bounded page size.
type ListSessionsRequest struct {
	Cursor string
	Limit  int
}

// SessionPage contains activity-ordered summaries and opaque navigation cursors.
type SessionPage struct {
	State          EvidenceState
	Sessions       []SessionSummary
	PreviousCursor string
	NextCursor     string
}

// TimelineRequest selects an ordered page or a window focused on one event.
type TimelineRequest struct {
	SessionID    model.SessionID
	Cursor       string
	Limit        int
	FocusedEvent model.EventID
	// IncludePayloads opts this bounded page into normalized payload reads.
	// Retained raw records are never included.
	IncludePayloads bool
}

// TimelinePage contains bounded canonical event summaries and their evidence state.
type TimelinePage struct {
	State          EvidenceState
	Events         []model.EventSummary
	Payloads       map[model.EventID]model.NormalizedData
	NextCursor     string
	Diagnostics    DiagnosticSynopsis
	FocusedEvent   model.EventID
	Interpretation InterpretationCoverage
}

// EventLocation identifies the retained session and ordering position of an event.
type EventLocation struct {
	EventID   model.EventID
	SessionID model.SessionID
	Sequence  int64
}

// EventDetailRequest selects one event and whether its normalized payload is needed.
type EventDetailRequest struct {
	SessionID      model.SessionID
	EventID        model.EventID
	IncludePayload bool
}

// EventDetail combines a lightweight envelope, optional payload, and bounded diagnostics.
type EventDetail struct {
	State          EvidenceState
	Event          model.EventSummary
	RawRecord      model.RawRecordRef
	Payload        model.NormalizedData
	Diagnostics    DiagnosticSynopsis
	Interpretation InterpretationCoverage
}

// UnknownEvidenceInspection is returned only by an explicit Unknown-event
// action and never by ordinary exploration reads.
type UnknownEvidenceInspection struct {
	State          EvidenceState
	SessionID      model.SessionID
	EventID        model.EventID
	Text           string
	OriginalSize   int64
	ReturnedSize   int64
	Truncated      bool
	RedactionCount int
}

// Explorer exposes bounded, validated reads over retained canonical evidence.
type Explorer interface {
	// LibraryOverview returns exact committed library counts.
	LibraryOverview(context.Context) (LibraryOverview, error)
	// ListSessions returns one activity-ordered page.
	ListSessions(context.Context, ListSessionsRequest) (SessionPage, error)
	// Timeline returns ordered summaries or a window containing a focused event.
	Timeline(context.Context, TimelineRequest) (TimelinePage, error)
	// EventDetail returns an event envelope and optionally its normalized payload.
	EventDetail(context.Context, EventDetailRequest) (EventDetail, error)
	// EventLocations resolves a bounded set of event IDs without loading payloads.
	EventLocations(context.Context, []model.EventID) (map[model.EventID]EventLocation, error)
	// InspectUnknownEvidence returns a bounded, redacted view of the retained
	// record for one session-owned Unknown event.
	InspectUnknownEvidence(context.Context, model.SessionID, model.EventID) (UnknownEvidenceInspection, error)
}

// explorationService validates presentation requests before delegating to storage.
type explorationService struct{ reader storage.ExplorationReader }

// NewExplorer creates the shared read service used by both presentation layers.
func NewExplorer(reader storage.ExplorationReader) (Explorer, error) {
	if reader == nil {
		return nil, errors.New("application explorer: reader is required")
	}
	return &explorationService{reader: reader}, nil
}

// LibraryOverview returns exact counts from committed canonical storage.
func (s *explorationService) LibraryOverview(ctx context.Context) (LibraryOverview, error) {
	if err := ctx.Err(); err != nil {
		return LibraryOverview{}, err
	}
	overview, err := s.reader.LibraryOverview(ctx)
	if err != nil {
		return LibraryOverview{}, fmt.Errorf("read library overview: %w", err)
	}
	return LibraryOverview{
		Sessions: overview.Sessions, Events: overview.Events,
		Agents: overview.Agents, IssueSessions: overview.IssueSessions,
	}, nil
}

// ListSessions returns a bounded keyset page ordered by canonical last activity.
func (s *explorationService) ListSessions(ctx context.Context, request ListSessionsRequest) (SessionPage, error) {
	if err := ctx.Err(); err != nil {
		return SessionPage{}, err
	}
	limit, err := pageLimit(request.Limit)
	if err != nil {
		return SessionPage{}, err
	}
	var after *storage.SessionCursor
	backward := false
	if request.Cursor != "" {
		cursor, err := decodeSessionCursor(request.Cursor)
		if err != nil {
			return SessionPage{}, err
		}
		after = &cursor
		backward = cursor.Before
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
		coverage, err := s.interpretationCoverage(ctx, row.ID)
		if err != nil {
			return SessionPage{}, fmt.Errorf("list imported sessions: interpretation coverage for %q: %w", row.ID, err)
		}
		state := EvidenceComplete
		if synopsis.Total > 0 {
			state = EvidencePartial
			page.State = EvidencePartial
		}
		page.Sessions = append(page.Sessions, SessionSummary{
			ID: row.ID, Title: row.Title, Summary: row.Summary, StartedAt: row.StartedAt, EndedAt: row.EndedAt,
			LastActivityAt: row.LastActivityAt, SourceID: row.SourceID, AgentName: row.AgentName,
			Preview:    sessionPreview(row.Summary, row.FirstUserMessage),
			EventCount: row.EventCount, State: state, Diagnostics: synopsis, Interpretation: coverage,
		})
	}
	if len(rows) > 0 && request.Cursor != "" && (!backward || more) {
		first := rows[0]
		page.PreviousCursor, err = encodeSessionCursor(first, true)
		if err != nil {
			return SessionPage{}, err
		}
	}
	if len(rows) > 0 && (more || backward) {
		last := rows[len(rows)-1]
		page.NextCursor, err = encodeSessionCursor(last, false)
		if err != nil {
			return SessionPage{}, err
		}
	}
	return page, nil
}

// Timeline returns bounded summaries and never treats missing or partial evidence as success.
func (s *explorationService) Timeline(ctx context.Context, request TimelineRequest) (TimelinePage, error) {
	if err := ctx.Err(); err != nil {
		return TimelinePage{}, err
	}
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
	if request.FocusedEvent != "" && request.Cursor != "" {
		return TimelinePage{}, fmt.Errorf("%w: focused event and cursor are mutually exclusive", ErrInvalidRequest)
	}
	var after *int64
	if request.Cursor != "" {
		sequence, err := decodeTimelineCursor(request.Cursor, request.SessionID)
		if err != nil {
			return TimelinePage{}, err
		}
		after = &sequence
	}
	var rows []model.EventSummary
	var more bool
	if request.FocusedEvent != "" {
		if err := validateEventID(request.FocusedEvent); err != nil {
			return TimelinePage{}, err
		}
		locations, locateErr := s.reader.EventLocations(ctx, []model.EventID{request.FocusedEvent})
		if locateErr != nil {
			return TimelinePage{}, fmt.Errorf("locate focused event %q: %w", request.FocusedEvent, locateErr)
		}
		location, found := locations[request.FocusedEvent]
		if !found || location.SessionID != request.SessionID {
			return TimelinePage{State: EvidenceNotFound}, nil
		}
		rows, more, err = s.reader.EventSummaryWindow(ctx, request.SessionID, location.Sequence, limit)
	} else {
		rows, more, err = s.reader.EventSummaryPage(ctx, request.SessionID, after, limit)
	}
	if err != nil {
		return TimelinePage{}, fmt.Errorf("read timeline for %q: %w", request.SessionID, err)
	}
	diagnostics, err := s.reader.Diagnostics(ctx, request.SessionID, nil, DiagnosticSynopsisMax)
	if err != nil {
		return TimelinePage{}, fmt.Errorf("read timeline diagnostics for %q: %w", request.SessionID, err)
	}
	synopsis := diagnosticSynopsis(diagnostics)
	coverage, err := s.interpretationCoverage(ctx, request.SessionID)
	if err != nil {
		return TimelinePage{}, fmt.Errorf("read timeline interpretation coverage for %q: %w", request.SessionID, err)
	}
	state := EvidenceComplete
	if synopsis.Total > 0 {
		state = EvidencePartial
		if len(rows) == 0 && request.Cursor == "" {
			state = EvidenceUnavailable
		}
	}
	page := TimelinePage{State: state, Events: rows, Diagnostics: synopsis, FocusedEvent: request.FocusedEvent, Interpretation: coverage}
	if request.IncludePayloads && len(rows) > 0 {
		eventIDs := make([]model.EventID, len(rows))
		for index, event := range rows {
			eventIDs[index] = event.ID
		}
		page.Payloads, err = s.reader.EventPayloads(ctx, request.SessionID, eventIDs)
		if err != nil {
			return TimelinePage{}, fmt.Errorf("read timeline payloads for %q: %w", request.SessionID, err)
		}
	}
	if more && len(rows) > 0 {
		page.NextCursor, err = encodeCursor(cursorEnvelope{Kind: "timeline", SessionID: request.SessionID, Sequence: rows[len(rows)-1].Sequence})
		if err != nil {
			return TimelinePage{}, err
		}
	}
	return page, nil
}

// EventLocations validates and resolves at most MaximumPageSize event IDs.
func (s *explorationService) EventLocations(ctx context.Context, eventIDs []model.EventID) (map[model.EventID]EventLocation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(eventIDs) > MaximumPageSize {
		return nil, fmt.Errorf("%w: at most %d event IDs may be located", ErrInvalidRequest, MaximumPageSize)
	}
	for _, eventID := range eventIDs {
		if err := validateEventID(eventID); err != nil {
			return nil, err
		}
	}
	found, err := s.reader.EventLocations(ctx, eventIDs)
	if err != nil {
		return nil, fmt.Errorf("locate events: %w", err)
	}
	result := make(map[model.EventID]EventLocation, len(found))
	for id, location := range found {
		result[id] = EventLocation{EventID: id, SessionID: location.SessionID, Sequence: location.Sequence}
	}
	return result, nil
}

// EventDetail reads normalized payload data only when explicitly requested.
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
	detail.Interpretation, err = s.interpretationCoverage(ctx, request.SessionID)
	if err != nil {
		return EventDetail{}, fmt.Errorf("read event %q interpretation coverage: %w", request.EventID, err)
	}
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

// interpretationCoverage maps exact storage counts to the source-neutral
// application state shared by both presentation layers.
func (s *explorationService) interpretationCoverage(ctx context.Context, sessionID model.SessionID) (InterpretationCoverage, error) {
	value, err := s.reader.InterpretationCoverage(ctx, sessionID)
	if err != nil {
		return InterpretationCoverage{}, err
	}
	state := InterpretationFullyInterpreted
	if value.UnknownEvents > 0 || value.MalformedRecords > 0 {
		state = InterpretationPartiallyInterpreted
	}
	return InterpretationCoverage{State: state, UnknownEvents: value.UnknownEvents, MalformedRecords: value.MalformedRecords}, nil
}

// InspectUnknownEvidence explicitly loads, redacts, and caps retained content
// for one Unknown event after verifying session ownership and event kind.
func (s *explorationService) InspectUnknownEvidence(ctx context.Context, sessionID model.SessionID, eventID model.EventID) (UnknownEvidenceInspection, error) {
	if err := validateIdentifier("session", string(sessionID)); err != nil {
		return UnknownEvidenceInspection{}, err
	}
	if err := validateEventID(eventID); err != nil {
		return UnknownEvidenceInspection{}, err
	}
	envelope, found, err := s.reader.EventEnvelope(ctx, sessionID, eventID)
	if err != nil {
		return UnknownEvidenceInspection{}, fmt.Errorf("inspect unknown event %q: %w", eventID, err)
	}
	if !found {
		return UnknownEvidenceInspection{State: EvidenceNotFound}, nil
	}
	if envelope.SessionID != sessionID {
		return UnknownEvidenceInspection{State: EvidenceNotFound}, nil
	}
	if envelope.Kind != model.EventKindUnknown {
		return UnknownEvidenceInspection{}, fmt.Errorf("%w: event %q is not Unknown evidence", ErrInvalidRequest, eventID)
	}
	prefix, found, err := s.reader.RawRecordPrefix(ctx, sessionID, envelope.RawRecord.ID, UnknownInspectionMaxBytes)
	if err != nil {
		return UnknownEvidenceInspection{}, fmt.Errorf("inspect unknown event %q retained record: %w", eventID, err)
	}
	if !found {
		return UnknownEvidenceInspection{State: EvidenceUnavailable, SessionID: sessionID, EventID: eventID}, nil
	}
	text := strings.ToValidUTF8(string(prefix.Content), "\uFFFD")
	truncatedInput := prefix.OriginalSize > int64(len(prefix.Content))
	var redactions int
	if truncatedInput {
		text, redactions = redaction.TruncatedText(text)
	} else {
		text, redactions = redaction.Text(text)
	}
	redactedSize := len(text)
	text = truncateUTF8Bytes(text, UnknownInspectionMaxBytes)
	return UnknownEvidenceInspection{
		State: EvidenceComplete, SessionID: sessionID, EventID: eventID, Text: text,
		OriginalSize: prefix.OriginalSize, ReturnedSize: int64(len(text)),
		Truncated:      truncatedInput || len(text) < redactedSize,
		RedactionCount: redactions,
	}, nil
}

// truncateUTF8Bytes returns at most limit bytes without splitting a UTF-8
// encoding. Callers provide a non-negative limit.
func truncateUTF8Bytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

// cursorEnvelope is the versioned, opaque wire representation for exploration cursors.
type cursorEnvelope struct {
	Version        int             `json:"v"`
	Kind           string          `json:"kind"`
	SessionID      model.SessionID `json:"session"`
	LastActivityAt *string         `json:"last_activity_at,omitempty"`
	Sequence       int64           `json:"sequence,omitempty"`
	Direction      string          `json:"direction,omitempty"`
}

const (
	// sessionCursorKind separates session cursors from timeline cursors.
	sessionCursorKind    = "sessions-last-activity"
	sessionCursorVersion = 1
)

// pageLimit applies the default and rejects unbounded exploration reads.
func pageLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultPageSize, nil
	}
	if limit < 0 || limit > MaximumPageSize {
		return 0, fmt.Errorf("%w: page limit must be between 1 and %d", ErrInvalidRequest, MaximumPageSize)
	}
	return limit, nil
}

// encodeCursor serializes a versioned cursor without exposing storage details.
func encodeCursor(cursor cursorEnvelope) (string, error) {
	cursor.Version = 1
	if cursor.Kind == sessionCursorKind {
		cursor.Version = sessionCursorVersion
	}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode exploration cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

// decodeCursor parses only supported cursor versions.
func decodeCursor(value string) (cursorEnvelope, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return cursorEnvelope{}, fmt.Errorf("%w: malformed cursor", ErrInvalidRequest)
	}
	var cursor cursorEnvelope
	if err := json.Unmarshal(decoded, &cursor); err != nil || (cursor.Version < 1 || cursor.Version > sessionCursorVersion) {
		return cursorEnvelope{}, fmt.Errorf("%w: unsupported cursor", ErrInvalidRequest)
	}
	return cursor, nil
}

// decodeSessionCursor validates cursor kind, version, direction, and activity time.
func decodeSessionCursor(value string) (storage.SessionCursor, error) {
	cursor, err := decodeCursor(value)
	if err != nil || cursor.Version != sessionCursorVersion || cursor.Kind != sessionCursorKind {
		return storage.SessionCursor{}, fmt.Errorf("%w: cursor does not belong to a session list", ErrInvalidRequest)
	}
	if err := validateIdentifier("session cursor", string(cursor.SessionID)); err != nil {
		return storage.SessionCursor{}, err
	}
	result := storage.SessionCursor{ID: cursor.SessionID}
	if cursor.Direction != "next" && cursor.Direction != "previous" {
		return storage.SessionCursor{}, fmt.Errorf("%w: malformed session cursor direction", ErrInvalidRequest)
	}
	result.Before = cursor.Direction == "previous"
	if cursor.LastActivityAt != nil {
		parsed, err := time.Parse(time.RFC3339Nano, *cursor.LastActivityAt)
		if err != nil {
			return storage.SessionCursor{}, fmt.Errorf("%w: malformed session cursor timestamp", ErrInvalidRequest)
		}
		result.LastActivityAt = &parsed
	}
	return result, nil
}

// encodeSessionCursor captures a keyset boundary and navigation direction.
func encodeSessionCursor(row storage.SessionSummary, before bool) (string, error) {
	direction := "next"
	if before {
		direction = "previous"
	}
	return encodeCursor(cursorEnvelope{
		Kind: sessionCursorKind, SessionID: row.ID,
		LastActivityAt: formatCursorTime(row.LastActivityAt), Direction: direction,
	})
}

// sessionPreview prefers an adapter-provided summary and falls back to the
// first user message. Whitespace normalization keeps previews compact and
// deterministic across terminal and web presentation layers.
func sessionPreview(summary, firstUserMessage string) string {
	preview := normalizeDisplayText(summary)
	if preview == "" {
		preview = normalizeDisplayText(firstUserMessage)
	}
	runes := []rune(preview)
	if len(runes) <= SessionPreviewMaxRunes {
		return preview
	}
	return string(runes[:SessionPreviewMaxRunes-1]) + "…"
}

// decodeTimelineCursor ensures a cursor belongs to the requested session timeline.
func decodeTimelineCursor(value string, sessionID model.SessionID) (int64, error) {
	cursor, err := decodeCursor(value)
	if err != nil || cursor.Kind != "timeline" || cursor.SessionID != sessionID || cursor.Sequence < 0 {
		return 0, fmt.Errorf("%w: cursor does not belong to this timeline", ErrInvalidRequest)
	}
	return cursor.Sequence, nil
}

// validateIdentifier rejects empty, oversized, malformed, or control-bearing identifiers.
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

// validateEventID enforces the stable hexadecimal canonical event ID shape.
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

// diagnosticSynopsis preserves exact totals while returning only the bounded sample.
func diagnosticSynopsis(page storage.DiagnosticPage) DiagnosticSynopsis {
	omitted := page.Total - int64(len(page.Diagnostics))
	if omitted < 0 {
		omitted = 0
	}
	return DiagnosticSynopsis{Diagnostics: page.Diagnostics, Total: page.Total, Omitted: omitted}
}

// formatCursorTime normalizes keyset timestamps to an exact UTC representation.
func formatCursorTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}
