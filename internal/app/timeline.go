package app

import (
	"context"
	"fmt"

	"github.com/pooya79/AgentSession/internal/model"
)

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
