package model

import (
	"math"
	"strings"
	"testing"
)

func TestHashRecordUsesExactBytes(t *testing.T) {
	const want = "sha256:59772b9c70d6cc244274937445f7c5b56ec6fe0a11292c4ed68848655515a1e6"
	if got := HashRecord([]byte("record\n")); got != want {
		t.Fatalf("HashRecord() = %q, want %q", got, want)
	}
	if HashRecord([]byte("record")) == want {
		t.Fatal("HashRecord() ignored the framing newline")
	}
}

func TestNewRawRecordIDIsDeterministicAndPrefersSequence(t *testing.T) {
	sequence := int64(7)
	input := RawRecordIDInput{
		SourceID:       "source-1",
		RecordSequence: &sequence,
		ByteRange:      &ByteRange{Offset: 20, Length: 8},
		ContentHash:    HashRecord([]byte("record\n")),
	}
	got, err := NewRawRecordID(input)
	if err != nil {
		t.Fatalf("NewRawRecordID() error = %v", err)
	}
	const golden = RawRecordID("raw_34c506573a863d9a451274daa9b1bfb6a2b08c76343a4a571d266f469125bbdd")
	if got != golden {
		t.Fatalf("NewRawRecordID() = %q, want stable fixture %q", got, golden)
	}
	withoutRange := input
	withoutRange.ByteRange = nil
	want, err := NewRawRecordID(withoutRange)
	if err != nil {
		t.Fatalf("sequence-only NewRawRecordID() error = %v", err)
	}
	if got != want {
		t.Fatalf("NewRawRecordID() = %q, want sequence-priority ID %q", got, want)
	}
	if !strings.HasPrefix(string(got), "raw_") || len(got) != len("raw_")+64 {
		t.Fatalf("NewRawRecordID() = %q, want raw_ plus SHA-256 hex", got)
	}
}

func TestNewRawRecordIDByteRangeFallbackAndValidation(t *testing.T) {
	contentHash := HashRecord([]byte("record"))
	input := RawRecordIDInput{
		SourceID:    "source-1",
		ByteRange:   &ByteRange{Offset: 0, Length: 6},
		ContentHash: contentHash,
	}
	if _, err := NewRawRecordID(input); err != nil {
		t.Fatalf("NewRawRecordID() error = %v", err)
	}

	tests := []RawRecordIDInput{
		{},
		{SourceID: "source-1", ContentHash: contentHash},
		{ByteRange: &ByteRange{Offset: 0, Length: 1}, ContentHash: contentHash},
		{SourceID: "source-1", ByteRange: &ByteRange{Offset: -1, Length: 1}, ContentHash: contentHash},
	}
	for i, candidate := range tests {
		if _, err := NewRawRecordID(candidate); err == nil {
			t.Errorf("case %d NewRawRecordID() error = nil, want validation error", i)
		}
	}
}

func TestByteRangeUsesHalfOpenOverflowSafeBounds(t *testing.T) {
	rangeValue := ByteRange{Offset: 4, Length: 6}
	end, err := rangeValue.End()
	if err != nil || end != 10 {
		t.Fatalf("End() = (%d, %v), want (10, nil)", end, err)
	}
	if err := (ByteRange{Offset: math.MaxInt64, Length: 1}).Validate(); err == nil {
		t.Fatal("Validate() error = nil for overflowing byte range")
	}
	if _, err := (ByteRange{Offset: 0, Length: 0}).End(); err == nil {
		t.Fatal("End() error = nil for empty byte range")
	}
}
