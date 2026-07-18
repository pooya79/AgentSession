package storage

import (
	"bytes"
	"testing"
)

func TestEncodePayloadThresholdBoundaries(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name         string
		size         int
		wantEncoding string
	}{
		{name: "at threshold", size: InlinePayloadThresholdBytes, wantEncoding: EncodingIdentity},
		{name: "above threshold", size: InlinePayloadThresholdBytes + 1, wantEncoding: EncodingZlib},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			content := bytes.Repeat([]byte("x"), tt.size)
			encoded, err := EncodePayload(content)
			if err != nil {
				t.Fatalf("EncodePayload() error = %v", err)
			}
			if encoded.PolicyVersion != FullRetentionPolicyVersion || encoded.Encoding != tt.wantEncoding {
				t.Fatalf("EncodePayload() policy/encoding = (%d, %q), want (%d, %q)", encoded.PolicyVersion, encoded.Encoding, FullRetentionPolicyVersion, tt.wantEncoding)
			}
			decoded, err := DecodePayload(encoded)
			if err != nil || !bytes.Equal(decoded, content) {
				t.Fatalf("DecodePayload() round trip = (%d bytes, %v), want %d bytes", len(decoded), err, len(content))
			}
		})
	}
}

func TestDecodePayloadRejectsCorruptionAndWrongSize(t *testing.T) {
	t.Parallel()

	encoded, err := EncodePayload(bytes.Repeat([]byte("compress me"), InlinePayloadThresholdBytes))
	if err != nil {
		t.Fatalf("EncodePayload() error = %v", err)
	}
	corrupt := encoded
	corrupt.Content = []byte("not zlib")
	if _, err := DecodePayload(corrupt); err == nil {
		t.Fatal("DecodePayload(corrupt) error = nil, want corruption error")
	}
	wrongSize := encoded
	wrongSize.OriginalSize++
	if _, err := DecodePayload(wrongSize); err == nil {
		t.Fatal("DecodePayload(wrong size) error = nil, want size error")
	}
}
