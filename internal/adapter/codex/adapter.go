// Package codex implements discovery and streaming import of Codex CLI rollout
// JSONL files.
package codex

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

const (
	// AdapterVersion identifies the persisted Codex adapter implementation.
	AdapterVersion model.Version = "1"
	// ModelVersion identifies the canonical event model emitted by this adapter.
	ModelVersion model.Version = "1"
	// NormalizationVersion identifies the Codex-to-canonical normalization rules.
	NormalizationVersion model.Version = "1"
	// CursorVersion identifies the persisted Codex rollout cursor schema.
	CursorVersion model.Version = "codex-rollout-cursor-v1"
	// FingerprintVersion identifies the persisted Codex source fingerprint schema.
	FingerprintVersion model.Version = "codex-rollout-fingerprint-v1"

	formatLegacy         = "codex-rollout-jsonl-v1"
	formatOrdinal        = "codex-rollout-jsonl-v2-ordinal"
	readBuffer           = 32 << 10
	initialRecords       = 8
	inspectionPrefix     = 64 << 10
	maxRecordDiagnostics = 8
)

// Adapter probes and imports Codex CLI rollout files.
type Adapter struct{}

// New returns a Codex rollout adapter.
func New() *Adapter { return &Adapter{} }

// Name returns the stable adapter name persisted with imported sessions.
func (*Adapter) Name() string { return "codex" }

// Version returns the persisted Codex adapter implementation version.
func (*Adapter) Version() model.Version { return AdapterVersion }

// Probe inspects a bounded initial record window to identify a Codex rollout.
func (a *Adapter) Probe(ctx context.Context, source importer.Source) (importer.ProbeResult, error) {
	if err := source.Validate(); err != nil {
		return importer.ProbeResult{}, err
	}
	r, err := source.OpenFrom(ctx, 0)
	if err != nil {
		return importer.ProbeResult{}, fmt.Errorf("open rollout for probe: %w", err)
	}
	defer r.Close()
	inspection, err := inspectInitial(ctx, bufio.NewReaderSize(r, readBuffer), nil)
	if err != nil {
		return importer.ProbeResult{}, fmt.Errorf("inspect rollout probe: %w", err)
	}
	if !inspection.valid && !inspection.possibleMalformed {
		return importer.ProbeResult{Confidence: importer.ProbeUnsupported, Diagnostics: inspection.diagnostics}, nil
	}
	confidence := importer.ProbePossible
	if inspection.recognized {
		confidence = importer.ProbeCertain
	}
	return importer.ProbeResult{
		Confidence:    confidence,
		FormatVersion: compositeFormat(inspection.ordinal, inspection.cliVersion),
		Diagnostics:   inspection.diagnostics,
	}, nil
}

// Prepare opens a Codex rollout for verification, reconciliation, or import.
func (a *Adapter) Prepare(ctx context.Context, source importer.Source) (importer.PreparedSource, error) {
	if err := source.Validate(); err != nil {
		return nil, err
	}
	stream, err := source.OpenFrom(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("open rollout: %w", err)
	}
	replay, err := os.CreateTemp("", "agentsession-codex-window-*")
	if err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("create rollout replay file: %w", err)
	}
	cleanup := func() {
		_ = errors.Join(stream.Close(), replay.Close(), os.Remove(replay.Name()))
	}
	reader := bufio.NewReaderSize(stream, readBuffer)
	replayWriter := bufio.NewWriterSize(replay, readBuffer)
	inspection, err := inspectInitial(ctx, reader, replayWriter)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("inspect rollout header window: %w", err)
	}
	if err := replayWriter.Flush(); err != nil {
		cleanup()
		return nil, fmt.Errorf("flush rollout replay file: %w", err)
	}
	if _, err := replay.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, fmt.Errorf("rewind rollout replay file: %w", err)
	}
	view := &prepared{
		adapter:      a,
		source:       source,
		sourceStream: stream,
		replay:       replay,
		reader:       bufio.NewReaderSize(io.MultiReader(replay, reader), readBuffer),
		ordinal:      inspection.ordinal,
		cliVersion:   inspection.cliVersion,
		sessionID:    inspection.sessionID,
		startedAt:    inspection.startedAt,
		diagnostics:  sessionInspectionDiagnostics(inspection.diagnostics),
	}
	if view.sessionID == "" {
		sum := sha256.Sum256([]byte(source.ID))
		view.sessionID = "codex_" + hex.EncodeToString(sum[:])
	}
	view.format = compositeFormat(view.ordinal, view.cliVersion)
	return view, nil
}

func compositeFormat(ordinal bool, cli string) model.Version {
	base := formatLegacy
	if ordinal {
		base = formatOrdinal
	}
	cli = strings.TrimSpace(cli)
	if cli == "" {
		cli = "unknown"
	}
	return model.Version(base + "+cli-" + cli)
}
