package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/pooya79/AgentSession/internal/discovery"
	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

// ErrSourceNotFound means an import ID is absent from the latest successful
// discovery catalog.
var ErrSourceNotFound = errors.New("discovered source was not found")

// Discover refreshes the runtime source catalog.
func (r *Runtime) Discover(ctx context.Context) (discovery.Result, error) {
	if err := r.accepting(); err != nil {
		return discovery.Result{}, err
	}
	result, err := r.discoverer.Discover(ctx)
	if err != nil {
		return result, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing || r.closed {
		return result, ErrShuttingDown
	}
	r.catalog = make(map[model.SourceID]discovery.Source, len(result.Sources))
	for _, source := range result.Sources {
		r.catalog[source.ID] = source
	}
	return result, nil
}

// Sources returns the last discovered catalog in deterministic discovery order.
func (r *Runtime) Sources() []discovery.Source {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]discovery.Source, 0, len(r.catalog))
	for _, source := range r.catalog {
		result = append(result, source)
	}
	// IDs are stable and sorting by path mirrors discovery's order sufficiently
	// for catalog consumers; batch workflows use the original result directly.
	sortDiscoverySources(result)
	return result
}

// DiscoveredSource resolves one source from the last successful discovery.
func (r *Runtime) DiscoveredSource(sourceID model.SourceID) (discovery.Source, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	source, ok := r.catalog[sourceID]
	return source, ok
}

// importerSource converts a discovered, allowlisted source into an adapter input.
func importerSource(source discovery.Source) (importer.Source, error) {
	info, err := os.Stat(source.Path)
	if err != nil {
		return importer.Source{}, err
	}
	if !info.Mode().IsRegular() {
		return importer.Source{}, errors.New("source is no longer a regular file")
	}
	openAt := func(ctx context.Context, offset int64) (io.ReadCloser, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := os.Open(source.Path)
		if err != nil {
			return nil, err
		}
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			_ = file.Close()
			return nil, err
		}
		return file, nil
	}
	return importer.Source{
		ID: source.ID, Size: info.Size(), Hint: string(source.Kind), LocalPath: filepath.Clean(source.Path),
		OpenAt: openAt,
	}, nil
}

// sortDiscoverySources establishes deterministic source ordering for presentation and imports.
func sortDiscoverySources(sources []discovery.Source) {
	for i := 1; i < len(sources); i++ {
		for j := i; j > 0 && (sources[j].Kind < sources[j-1].Kind || (sources[j].Kind == sources[j-1].Kind && sources[j].Path < sources[j-1].Path)); j-- {
			sources[j], sources[j-1] = sources[j-1], sources[j]
		}
	}
}
