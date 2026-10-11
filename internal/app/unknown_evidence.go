package app

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/redaction"
)

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
