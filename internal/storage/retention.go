// Package storage defines source-neutral persistence contracts and policies.
package storage

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
)

const (
	// FullRetentionPolicyVersion identifies the v0.1 full-retention layout.
	FullRetentionPolicyVersion = 1
	// InlinePayloadThresholdBytes is the largest payload that may remain inline.
	InlinePayloadThresholdBytes = 256 * 1024

	EncodingIdentity = "identity"
	EncodingZlib     = "zlib"
)

// EncodedPayload is the durable representation of retained bytes.
type EncodedPayload struct {
	PolicyVersion int
	Encoding      string
	OriginalSize  int64
	Content       []byte
}

// EncodePayload applies the full-retention policy without truncating content.
func EncodePayload(content []byte) (EncodedPayload, error) {
	encoded := EncodedPayload{
		PolicyVersion: FullRetentionPolicyVersion,
		Encoding:      EncodingIdentity,
		OriginalSize:  int64(len(content)),
		Content:       content,
	}
	if len(content) <= InlinePayloadThresholdBytes {
		return encoded, nil
	}

	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(content); err != nil {
		return EncodedPayload{}, fmt.Errorf("compress retained payload: %w", err)
	}
	if err := writer.Close(); err != nil {
		return EncodedPayload{}, fmt.Errorf("finish compressing retained payload: %w", err)
	}
	encoded.Encoding = EncodingZlib
	encoded.Content = compressed.Bytes()
	return encoded, nil
}

// DecodePayload restores authoritative retained bytes and verifies their size.
func DecodePayload(payload EncodedPayload) ([]byte, error) {
	if payload.PolicyVersion != FullRetentionPolicyVersion {
		return nil, fmt.Errorf("unsupported retention policy version %d", payload.PolicyVersion)
	}
	if payload.OriginalSize < 0 {
		return nil, fmt.Errorf("negative retained payload size %d", payload.OriginalSize)
	}

	switch payload.Encoding {
	case EncodingIdentity:
		if int64(len(payload.Content)) != payload.OriginalSize {
			return nil, fmt.Errorf("payload size %d does not match recorded size %d", len(payload.Content), payload.OriginalSize)
		}
		return append([]byte(nil), payload.Content...), nil
	case EncodingZlib:
		reader, err := zlib.NewReader(bytes.NewReader(payload.Content))
		if err != nil {
			return nil, fmt.Errorf("open compressed retained payload: %w", err)
		}
		content, readErr := io.ReadAll(io.LimitReader(reader, payload.OriginalSize+1))
		closeErr := reader.Close()
		if readErr != nil {
			return nil, fmt.Errorf("decompress retained payload: %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close compressed retained payload: %w", closeErr)
		}
		if int64(len(content)) != payload.OriginalSize {
			return nil, fmt.Errorf("payload size %d does not match recorded size %d", len(content), payload.OriginalSize)
		}
		return content, nil
	default:
		return nil, fmt.Errorf("unsupported retained payload encoding %q", payload.Encoding)
	}
}
