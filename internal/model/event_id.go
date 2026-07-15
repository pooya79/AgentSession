package model

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"strconv"
	"strings"
)

type NativeEventIDScope string

const (
	NativeEventIDGlobal  NativeEventIDScope = "global"
	NativeEventIDSession NativeEventIDScope = "session"
)

// NativeEventIdentity describes an adapter-supplied native identity without
// exposing any source-specific record structure to the model.
type NativeEventIdentity struct {
	Scope     NativeEventIDScope
	SessionID string
	EventID   string
}

// EventIDInput contains the strongest identity atoms an adapter can provide.
// NewEventID prefers native identity, then record sequence, then byte range.
type EventIDInput struct {
	Native         *NativeEventIdentity
	SourceID       SourceID
	RecordSequence *int64
	ByteRange      *ByteRange
	RecordHash     string
}

// NewEventID returns a deterministic ID for canonical imported evidence.
func NewEventID(input EventIDInput) (EventID, error) {
	if input.Native != nil {
		return eventIDFromNative(*input.Native)
	}

	sourceID := strings.TrimSpace(string(input.SourceID))
	recordHash := strings.TrimSpace(input.RecordHash)
	if input.RecordSequence != nil {
		if sourceID == "" || recordHash == "" {
			return "", fmt.Errorf("source ID and record hash are required with record sequence")
		}
		if *input.RecordSequence < 0 {
			return "", fmt.Errorf("record sequence must not be negative")
		}
		return hashEventID("source-sequence", sourceID, strconv.FormatInt(*input.RecordSequence, 10), recordHash), nil
	}
	if input.ByteRange != nil {
		if sourceID == "" || recordHash == "" {
			return "", fmt.Errorf("source ID and record hash are required with byte range")
		}
		if err := input.ByteRange.validate(); err != nil {
			return "", fmt.Errorf("event identity byte range: %w", err)
		}
		return hashEventID(
			"source-byte-range",
			sourceID,
			strconv.FormatInt(input.ByteRange.Offset, 10),
			strconv.FormatInt(input.ByteRange.Length, 10),
			recordHash,
		), nil
	}
	return "", fmt.Errorf("event identity requires native identity, record sequence, or byte range")
}

func eventIDFromNative(native NativeEventIdentity) (EventID, error) {
	eventID := strings.TrimSpace(native.EventID)
	if eventID == "" {
		return "", fmt.Errorf("native event ID is required")
	}
	switch native.Scope {
	case NativeEventIDGlobal:
		if strings.TrimSpace(native.SessionID) != "" {
			return "", fmt.Errorf("global native event identity must not include a session ID")
		}
		return hashEventID("native-global", eventID), nil
	case NativeEventIDSession:
		sessionID := strings.TrimSpace(native.SessionID)
		if sessionID == "" {
			return "", fmt.Errorf("session-scoped native event identity requires a session ID")
		}
		return hashEventID("native-session", sessionID, eventID), nil
	default:
		return "", fmt.Errorf("unsupported native event ID scope %q", native.Scope)
	}
}

func hashEventID(tier string, fields ...string) EventID {
	digest := sha256.New()
	writeHashField(digest, "agentsession:event-id:v1")
	writeHashField(digest, tier)
	for _, field := range fields {
		writeHashField(digest, field)
	}
	return EventID("evt_" + hex.EncodeToString(digest.Sum(nil)))
}

func writeHashField(digest hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(value))
}
