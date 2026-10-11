package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/storage"
)

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

// decodeTimelineCursor ensures a cursor belongs to the requested session timeline.
func decodeTimelineCursor(value string, sessionID model.SessionID) (int64, error) {
	cursor, err := decodeCursor(value)
	if err != nil || cursor.Kind != "timeline" || cursor.SessionID != sessionID || cursor.Sequence < 0 {
		return 0, fmt.Errorf("%w: cursor does not belong to this timeline", ErrInvalidRequest)
	}
	return cursor.Sequence, nil
}

// formatCursorTime normalizes keyset timestamps to an exact UTC representation.
func formatCursorTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}
