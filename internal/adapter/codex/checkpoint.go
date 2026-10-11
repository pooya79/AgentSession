package codex

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/pooya79/AgentSession/internal/importer"
)

type cursorState struct {
	Version string `json:"version"`
	Offset  int64  `json:"offset"`
}

type fingerprintState struct {
	Version   string `json:"version"`
	SessionID string `json:"session_id"`
	SHA256    string `json:"sha256"`
}

func (p *prepared) Verify(ctx context.Context, state importer.SourceState) (importer.SourceChange, error) {
	if state.Import.AdapterName != p.adapter.Name() || state.Import.AdapterVersion != p.adapter.Version() ||
		state.Import.FormatVersion != p.format || state.Import.ModelVersion != ModelVersion || state.Import.NormalizationVersion != NormalizationVersion {
		return importer.SourceReplaced, nil
	}
	var cursor cursorState
	var fingerprint fingerprintState
	if state.Checkpoint.StateVersion != CursorVersion || json.Unmarshal(state.Checkpoint.Cursor, &cursor) != nil ||
		json.Unmarshal(state.Checkpoint.Fingerprint, &fingerprint) != nil || cursor.Version != string(CursorVersion) ||
		fingerprint.Version != string(FingerprintVersion) {
		return importer.SourceReplaced, nil
	}
	if fingerprint.SessionID != p.sessionID {
		return importer.SourceReplaced, nil
	}
	if cursor.Offset > p.source.Size {
		return importer.SourceTruncated, nil
	}
	if cursor.Offset < 0 {
		return importer.SourceMutated, nil
	}
	if err := p.consumePrefix(ctx, cursor.Offset); err != nil {
		return "", err
	}
	if hex.EncodeToString(p.digest.Sum(nil)) != fingerprint.SHA256 {
		return importer.SourceMutated, nil
	}
	p.seq = state.Checkpoint.RecordSequence + 1
	if state.LastEventSequence != nil {
		p.eventSeq = *state.LastEventSequence + 1
	}
	if cursor.Offset == p.source.Size {
		return importer.SourceUnchanged, nil
	}
	return importer.SourceAppend, nil
}

func (p *prepared) consumePrefix(ctx context.Context, target int64) error {
	p.digest = sha256.New()
	spool, err := os.CreateTemp("", "agentsession-codex-prefix-*")
	if err != nil {
		return fmt.Errorf("create verification spool: %w", err)
	}
	p.spool = spool
	remaining := target
	buf := make([]byte, readBuffer)
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := int64(len(buf))
		if remaining < want {
			want = remaining
		}
		n, readErr := io.ReadFull(p.reader, buf[:want])
		if n > 0 {
			if err := p.writeVerified(buf[:n]); err != nil {
				return err
			}
			remaining -= int64(n)
		}
		if readErr != nil {
			return fmt.Errorf("verify checkpoint prefix: %w", readErr)
		}
	}
	p.offset = target
	// Reconstruct adapter-owned linkage from the already verified, streamed
	// prefix. No unbounded checkpoint state or live directory is needed.
	if _, err := p.spool.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind command context: %w", err)
	}
	p.commands = commandContext{}
	reader := bufio.NewReaderSize(p.spool, readBuffer)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, complete, err := readLine(reader)
		if complete {
			var record wireRecord
			if json.Unmarshal(trimLineEnding(line), &record) == nil {
				p.commands.normalize(record)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("restore command context: %w", err)
		}
	}
	return nil
}

func (p *prepared) writeVerified(data []byte) error {
	if _, err := p.digest.Write(data); err != nil {
		return err
	}
	if _, err := p.spool.Write(data); err != nil {
		return fmt.Errorf("spool verified prefix: %w", err)
	}
	return nil
}

func (p *prepared) checkpoint(sequence int64) (importer.ImportCheckpoint, error) {
	cursor, err := json.Marshal(cursorState{Version: string(CursorVersion), Offset: p.offset})
	if err != nil {
		return importer.ImportCheckpoint{}, err
	}
	fingerprint, err := json.Marshal(fingerprintState{Version: string(FingerprintVersion), SessionID: p.sessionID, SHA256: hex.EncodeToString(p.digest.Sum(nil))})
	if err != nil {
		return importer.ImportCheckpoint{}, err
	}
	return importer.ImportCheckpoint{SourceID: p.source.ID, RecordSequence: sequence, StateVersion: CursorVersion, Cursor: cursor, Fingerprint: fingerprint}, nil
}
