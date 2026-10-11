package opencode

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash"

	"github.com/pooya79/AgentSession/internal/importer"
)

type cursorState struct {
	Version         string `json:"version"`
	Format          string `json:"format"`
	EventConvention string `json:"event_sequence_convention,omitempty"`
	Count           int64  `json:"count"`
	LastKey         string `json:"last_key"`
	MarkerState     string `json:"marker_state,omitempty"`
}

type fingerprintState struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

func (p *prepared) Verify(ctx context.Context, state importer.SourceState) (importer.SourceChange, error) {
	if state.Import.AdapterName != p.adapter.Name() || state.Import.AdapterVersion != AdapterVersion ||
		state.Import.FormatVersion != p.selected.format || state.Import.ModelVersion != ModelVersion ||
		state.Import.NormalizationVersion != NormalizationVersion {
		return importer.SourceReplaced, nil
	}
	var cursor cursorState
	var fingerprint fingerprintState
	if state.Checkpoint.StateVersion != CursorVersion || json.Unmarshal(state.Checkpoint.Cursor, &cursor) != nil ||
		json.Unmarshal(state.Checkpoint.Fingerprint, &fingerprint) != nil || cursor.Version != string(CursorVersion) ||
		cursor.Format != string(p.selected.format) || cursor.EventConvention != p.selected.eventConvention ||
		fingerprint.Version != fingerprintVersion || cursor.Count < 0 {
		return importer.SourceReplaced, nil
	}
	if cursor.MarkerState != p.selected.markerState {
		return importer.SourceMutated, nil
	}
	digest := sha256.New()
	var count, total int64
	var lastKey string
	prefix := hex.EncodeToString(digest.Sum(nil))
	err := p.eachRecord(ctx, func(record logicalRecord) error {
		total++
		if count < cursor.Count {
			writeFingerprint(digest, record.raw)
			count++
			lastKey = record.key
			if count == cursor.Count {
				prefix = hex.EncodeToString(digest.Sum(nil))
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if count < cursor.Count {
		return importer.SourceTruncated, nil
	}
	if lastKey != cursor.LastKey {
		return importer.SourceMutated, nil
	}
	if prefix != fingerprint.SHA256 {
		return importer.SourceMutated, nil
	}
	if total == cursor.Count {
		return importer.SourceUnchanged, nil
	}
	if p.selected.durableIncomplete {
		// Appending relocates a trailing gap diagnostic on a formerly final row.
		return importer.SourceMutated, nil
	}
	return importer.SourceAppend, nil
}

func (p *prepared) checkpoint(count int64, lastKey string, digest hash.Hash) (importer.ImportCheckpoint, error) {
	cursor, err := json.Marshal(cursorState{
		Version: string(CursorVersion), Format: string(p.selected.format), EventConvention: p.selected.eventConvention,
		Count: count, LastKey: lastKey,
		MarkerState: p.selected.markerState,
	})
	if err != nil {
		return importer.ImportCheckpoint{}, err
	}
	fingerprint, err := json.Marshal(fingerprintState{Version: fingerprintVersion, SHA256: hex.EncodeToString(digest.Sum(nil))})
	if err != nil {
		return importer.ImportCheckpoint{}, err
	}
	return importer.ImportCheckpoint{
		SourceID: p.source.ID, RecordSequence: count - 1, StateVersion: CursorVersion,
		Cursor: cursor, Fingerprint: fingerprint,
	}, nil
}

func writeFingerprint(digest interface{ Write([]byte) (int, error) }, raw []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(raw)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(raw)
}
