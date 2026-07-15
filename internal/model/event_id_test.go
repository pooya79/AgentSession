package model

import (
	"strings"
	"testing"
)

func TestNewEventIDIsDeterministic(t *testing.T) {
	sequence := int64(0)
	input := EventIDInput{
		SourceID:       "source-1",
		RecordSequence: &sequence,
		RecordHash:     "sha256:record-1",
	}
	first, err := NewEventID(input)
	if err != nil {
		t.Fatalf("NewEventID() error = %v", err)
	}
	second, err := NewEventID(input)
	if err != nil {
		t.Fatalf("second NewEventID() error = %v", err)
	}
	if first != second {
		t.Fatalf("NewEventID() = %q then %q, want stable result", first, second)
	}
	const golden = EventID("evt_c5d2050cfb37725fdb3abfa7386b698047366a74dbb47c63ba5af0f31a884aba")
	if first != golden {
		t.Fatalf("NewEventID() = %q, want stable fixture %q", first, golden)
	}
	if !strings.HasPrefix(string(first), "evt_") || len(first) != len("evt_")+64 {
		t.Fatalf("NewEventID() = %q, want evt_ plus SHA-256 hex", first)
	}

	changed := input
	changed.RecordHash = "sha256:record-2"
	different, err := NewEventID(changed)
	if err != nil {
		t.Fatalf("changed NewEventID() error = %v", err)
	}
	if different == first {
		t.Fatal("NewEventID() did not change when record hash changed")
	}
}

func TestNewEventIDIdentityPriority(t *testing.T) {
	sequence := int64(3)
	input := EventIDInput{
		Native: &NativeEventIdentity{
			Scope:   NativeEventIDGlobal,
			EventID: "native-event",
		},
		SourceID:       "source-1",
		RecordSequence: &sequence,
		ByteRange:      &ByteRange{Offset: 10, Length: 20},
		RecordHash:     "record-hash",
	}
	got, err := NewEventID(input)
	if err != nil {
		t.Fatalf("NewEventID() error = %v", err)
	}
	want, err := NewEventID(EventIDInput{Native: input.Native})
	if err != nil {
		t.Fatalf("native NewEventID() error = %v", err)
	}
	if got != want {
		t.Fatalf("NewEventID() = %q, want native-priority ID %q", got, want)
	}

	input.Native = nil
	got, err = NewEventID(input)
	if err != nil {
		t.Fatalf("fallback NewEventID() error = %v", err)
	}
	want, err = NewEventID(EventIDInput{
		SourceID:       input.SourceID,
		RecordSequence: input.RecordSequence,
		RecordHash:     input.RecordHash,
	})
	if err != nil {
		t.Fatalf("sequence NewEventID() error = %v", err)
	}
	if got != want {
		t.Fatalf("NewEventID() = %q, want sequence-priority ID %q", got, want)
	}
}

func TestNewEventIDNativeScopesAndCanonicalEncoding(t *testing.T) {
	global, err := NewEventID(EventIDInput{Native: &NativeEventIdentity{
		Scope: NativeEventIDGlobal, EventID: "event-1",
	}})
	if err != nil {
		t.Fatalf("global NewEventID() error = %v", err)
	}
	scoped, err := NewEventID(EventIDInput{Native: &NativeEventIdentity{
		Scope: NativeEventIDSession, SessionID: "session-1", EventID: "event-1",
	}})
	if err != nil {
		t.Fatalf("scoped NewEventID() error = %v", err)
	}
	if global == scoped {
		t.Fatal("global and session-scoped native identities produced the same ID")
	}

	left, err := NewEventID(EventIDInput{Native: &NativeEventIdentity{
		Scope: NativeEventIDSession, SessionID: "a:b", EventID: "c",
	}})
	if err != nil {
		t.Fatalf("left NewEventID() error = %v", err)
	}
	right, err := NewEventID(EventIDInput{Native: &NativeEventIdentity{
		Scope: NativeEventIDSession, SessionID: "a", EventID: "b:c",
	}})
	if err != nil {
		t.Fatalf("right NewEventID() error = %v", err)
	}
	if left == right {
		t.Fatal("length-prefixed identity fields collided")
	}
}

func TestNewEventIDByteRangeFallback(t *testing.T) {
	input := EventIDInput{
		SourceID:   "source-1",
		ByteRange:  &ByteRange{Offset: 0, Length: 12},
		RecordHash: "record-hash",
	}
	if _, err := NewEventID(input); err != nil {
		t.Fatalf("NewEventID() error = %v for zero-offset byte range", err)
	}

	input.ByteRange = &ByteRange{Offset: -1, Length: 12}
	if _, err := NewEventID(input); err == nil {
		t.Fatal("NewEventID() error = nil for negative byte offset")
	}
}

func TestNewEventIDRejectsIncompleteInputs(t *testing.T) {
	sequence := int64(1)
	tests := []EventIDInput{
		{},
		{Native: &NativeEventIdentity{Scope: NativeEventIDGlobal}},
		{Native: &NativeEventIdentity{Scope: NativeEventIDGlobal, SessionID: "unexpected", EventID: "event"}},
		{Native: &NativeEventIdentity{Scope: NativeEventIDSession, EventID: "event"}},
		{Native: &NativeEventIdentity{Scope: "repository", EventID: "event"}},
		{RecordSequence: &sequence, RecordHash: "record-hash"},
		{SourceID: "source-1", RecordSequence: &sequence},
		{SourceID: "source-1", ByteRange: &ByteRange{Offset: 0, Length: 1}},
	}
	for i, input := range tests {
		if _, err := NewEventID(input); err == nil {
			t.Errorf("case %d NewEventID() error = nil, want validation error", i)
		}
	}
}
