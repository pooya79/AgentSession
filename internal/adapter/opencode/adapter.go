// Package opencode imports the supported generations of OpenCode's SQLite store.
package opencode

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

const (
	AdapterVersion       model.Version = "1"
	FormatVersion        model.Version = "opencode-sqlite-container-v1"
	LegacyFormatVersion  model.Version = "opencode-sqlite-message-part-v1"
	MessageFormatVersion model.Version = "opencode-sqlite-session-message-v1"
	EventFormatVersion   model.Version = "opencode-sqlite-durable-event-v1"
	ModelVersion         model.Version = "1"
	NormalizationVersion model.Version = "1"
	CursorVersion        model.Version = "opencode-logical-cursor-v1"
	fingerprintVersion                 = "opencode-logical-fingerprint-v1"
)

type Adapter struct{}

func New() *Adapter                     { return &Adapter{} }
func (*Adapter) Name() string           { return "opencode" }
func (*Adapter) Version() model.Version { return AdapterVersion }

func (a *Adapter) Probe(ctx context.Context, source importer.Source) (importer.ProbeResult, error) {
	if err := source.Validate(); err != nil {
		return importer.ProbeResult{}, err
	}
	if source.LocalPath == "" {
		return importer.ProbeResult{Confidence: importer.ProbeUnsupported}, nil
	}
	view, err := openSnapshot(ctx, source.LocalPath)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not a database") {
			return importer.ProbeResult{Confidence: importer.ProbeUnsupported}, nil
		}
		return importer.ProbeResult{}, fmt.Errorf("open OpenCode database for probe: %w", err)
	}
	defer view.close()
	_, ok, err := probeSchema(ctx, view.tx)
	if err != nil {
		return importer.ProbeResult{}, fmt.Errorf("probe OpenCode schema: %w", err)
	}
	if !ok {
		return importer.ProbeResult{Confidence: importer.ProbeUnsupported}, nil
	}
	return importer.ProbeResult{Confidence: importer.ProbeCertain, FormatVersion: FormatVersion}, nil
}

func (a *Adapter) PrepareContainer(ctx context.Context, source importer.Source) (importer.PreparedContainer, error) {
	if err := source.Validate(); err != nil {
		return nil, err
	}
	if source.LocalPath == "" {
		return nil, errors.New("OpenCode SQLite adapter requires a local path")
	}
	view, err := openSnapshot(ctx, source.LocalPath)
	if err != nil {
		return nil, err
	}
	schema, ok, err := probeSchema(ctx, view.tx)
	if err != nil || !ok {
		_ = view.close()
		if err != nil {
			return nil, fmt.Errorf("probe OpenCode schema for %q: %w", source.LocalPath, err)
		}
		return nil, errors.New("database does not contain a supported OpenCode schema")
	}
	return &container{adapter: a, source: source, snapshot: view, schema: schema}, nil
}
