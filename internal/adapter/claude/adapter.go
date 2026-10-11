// Package claude imports Claude Code session JSONL files.
package claude

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

const (
	AdapterVersion       model.Version = "1"
	ModelVersion         model.Version = "1"
	NormalizationVersion model.Version = "1"
	CursorVersion        model.Version = "claude-code-jsonl-cursor-v1"
	FingerprintVersion   model.Version = "claude-code-jsonl-fingerprint-v1"

	formatBase  = "claude-code-jsonl-v1"
	readBuffer  = 32 << 10
	probeWindow = 8
	probeHeader = 64 << 10
)

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (*Adapter) Name() string           { return "claude" }
func (*Adapter) Version() model.Version { return AdapterVersion }

func (a *Adapter) Probe(ctx context.Context, source importer.Source) (importer.ProbeResult, error) {
	if err := source.Validate(); err != nil {
		return importer.ProbeResult{}, err
	}
	stream, err := source.OpenFrom(ctx, 0)
	if err != nil {
		return importer.ProbeResult{}, fmt.Errorf("open Claude session for probe: %w", err)
	}
	defer stream.Close()

	reader := bufio.NewReaderSize(stream, readBuffer)
	if header, _ := reader.Peek(len("SQLite format 3\x00")); bytes.Equal(header, []byte("SQLite format 3\x00")) {
		return importer.ProbeResult{Confidence: importer.ProbeUnsupported}, nil
	}
	recognized, valid, possibleMalformed := false, false, false
	cliVersion := "unknown"
	var diagnostics []model.Diagnostic
	for i := 0; i < probeWindow; i++ {
		if err := ctx.Err(); err != nil {
			return importer.ProbeResult{}, err
		}
		line, complete, readErr := readLine(reader)
		if len(line) == 0 && readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return importer.ProbeResult{}, fmt.Errorf("read Claude session probe: %w", readErr)
		}
		if !complete {
			possibleMalformed = possibleMalformed || looksClaudeLike(line)
			break
		}
		var record wireRecord
		if err := json.Unmarshal(trimLineEnding(line), &record); err != nil {
			diagnostics = append(diagnostics, model.Diagnostic{Code: "claude.probe.malformed", Severity: model.SeverityWarning, Message: "A complete JSONL record is malformed."})
			possibleMalformed = possibleMalformed || looksClaudeLike(line)
			continue
		}
		valid = true
		if knownTopLevel(record.Type) || record.SessionID != "" || hasJSONField(trimLineEnding(line), "isSidechain") {
			recognized = true
		}
		if strings.TrimSpace(record.AgentID) != "" {
			recognized = true
		}
		if value := strings.TrimSpace(record.Version); value != "" {
			cliVersion = value
		}
		if readErr != nil {
			break
		}
	}
	if !valid && !possibleMalformed {
		return importer.ProbeResult{Confidence: importer.ProbeUnsupported, Diagnostics: diagnostics}, nil
	}
	confidence := importer.ProbePossible
	if recognized {
		confidence = importer.ProbeCertain
	}
	return importer.ProbeResult{Confidence: confidence, FormatVersion: compositeFormat(cliVersion), Diagnostics: diagnostics}, nil
}

// Prepare opens a replayable streaming view, scanning sparse leading records
// for session metadata without loading the complete source into memory.
func (a *Adapter) Prepare(ctx context.Context, source importer.Source) (importer.PreparedSource, error) {
	if err := source.Validate(); err != nil {
		return nil, err
	}
	stream, err := source.OpenFrom(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("open Claude session: %w", err)
	}
	reader := bufio.NewReaderSize(stream, readBuffer)
	replay, err := os.CreateTemp("", "agentsession-claude-probe-*")
	if err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("create Claude probe replay: %w", err)
	}
	view := &prepared{adapter: a, source: source, sourceStream: stream, replay: replay, cliVersion: "unknown"}
	for i := 0; i < probeWindow; i++ {
		if err := ctx.Err(); err != nil {
			_ = view.Close()
			return nil, err
		}
		line, header, complete, truncated, readErr := replayLine(reader, replay, probeHeader)
		if complete {
			view.inspectRecord(line, header, truncated)
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				_ = view.Close()
				return nil, fmt.Errorf("inspect Claude session header: %w", readErr)
			}
			break
		}
	}
	if view.sessionID == "" {
		sum := sha256.Sum256([]byte(source.ID))
		view.sessionID = "claude_" + hex.EncodeToString(sum[:])
	} else if view.agentID != "" {
		view.sessionID = agentSessionID(view.sessionID, view.agentID)
	}
	view.format = compositeFormat(view.cliVersion)
	if _, err := replay.Seek(0, io.SeekStart); err != nil {
		_ = view.Close()
		return nil, fmt.Errorf("rewind Claude probe replay: %w", err)
	}
	view.reader = bufio.NewReaderSize(io.MultiReader(replay, reader), readBuffer)
	return view, nil
}

func compositeFormat(cli string) model.Version {
	cli = strings.TrimSpace(cli)
	if cli == "" {
		cli = "unknown"
	}
	return model.Version(formatBase + "+cli-" + cli)
}
