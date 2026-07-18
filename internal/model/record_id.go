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

// HashRecord returns the canonical digest label for the exact retained bytes
// of a source record.
func HashRecord(content []byte) string {
	digest := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// RawRecordIDInput identifies a retained record by its source, exact content,
// and strongest available source position. Sequence is preferred to byte range.
type RawRecordIDInput struct {
	SourceID       SourceID
	RecordSequence *int64
	ByteRange      *ByteRange
	ContentHash    string
}

// NewRawRecordID returns a deterministic identifier for retained raw evidence.
func NewRawRecordID(input RawRecordIDInput) (RawRecordID, error) {
	sourceID := strings.TrimSpace(string(input.SourceID))
	contentHash := strings.TrimSpace(input.ContentHash)
	if sourceID == "" || contentHash == "" {
		return "", fmt.Errorf("source ID and content hash are required")
	}
	if input.RecordSequence != nil {
		if *input.RecordSequence < 0 {
			return "", fmt.Errorf("record sequence must not be negative")
		}
		return hashRawRecordID("source-sequence", sourceID, strconv.FormatInt(*input.RecordSequence, 10), contentHash), nil
	}
	if input.ByteRange != nil {
		if err := input.ByteRange.Validate(); err != nil {
			return "", fmt.Errorf("raw record identity byte range: %w", err)
		}
		return hashRawRecordID(
			"source-byte-range",
			sourceID,
			strconv.FormatInt(input.ByteRange.Offset, 10),
			strconv.FormatInt(input.ByteRange.Length, 10),
			contentHash,
		), nil
	}
	return "", fmt.Errorf("raw record identity requires record sequence or byte range")
}

func hashRawRecordID(tier string, fields ...string) RawRecordID {
	digest := sha256.New()
	writeRawRecordHashField(digest, "agentsession:raw-record-id:v1")
	writeRawRecordHashField(digest, tier)
	for _, field := range fields {
		writeRawRecordHashField(digest, field)
	}
	return RawRecordID("raw_" + hex.EncodeToString(digest.Sum(nil)))
}

func writeRawRecordHashField(digest hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(value))
}
