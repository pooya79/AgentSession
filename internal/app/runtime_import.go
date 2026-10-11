package app

import (
	"context"
	"fmt"

	"github.com/pooya79/AgentSession/internal/discovery"
	"github.com/pooya79/AgentSession/internal/model"
)

// BatchImportResult contains completed discovery and terminal import states.
type BatchImportResult struct {
	Discovery discovery.Result
	Imports   []ImportProgress
}

// BatchImportError reports independent failures without retaining source
// record contents in its presentation-safe message.
type BatchImportError struct {
	DiscoveryFailures int
	ImportFailures    int
}

// Error returns a presentation-safe count of discovery and import failures.
func (e *BatchImportError) Error() string {
	return fmt.Sprintf("import completed with %d discovery failure(s) and %d source failure(s)", e.DiscoveryFailures, e.ImportFailures)
}

// RequestImport imports one source from the most recent discovery result.
func (r *Runtime) RequestImport(sourceID model.SourceID) (*ImportSubscription, bool, error) {
	if err := r.accepting(); err != nil {
		return nil, false, err
	}
	r.mu.RLock()
	discovered, ok := r.catalog[sourceID]
	r.mu.RUnlock()
	if !ok {
		return nil, false, fmt.Errorf("request import for source %q: %w", sourceID, ErrSourceNotFound)
	}
	source, err := importerSource(discovered)
	if err != nil {
		return nil, false, fmt.Errorf("request import for source %q: %w", sourceID, err)
	}
	return r.imports.Request(source)
}

// DiscoverAndImport processes sources sequentially and continues after
// independent failures. Context cancellation stops scheduling new work.
func (r *Runtime) DiscoverAndImport(ctx context.Context) (BatchImportResult, error) {
	discovered, discoverErr := r.Discover(ctx)
	result := BatchImportResult{Discovery: discovered}
	if discoverErr != nil {
		return result, discoverErr
	}
	discoveryFailures := 0
	for _, diagnostic := range discovered.Diagnostics {
		if diagnostic.Severity == model.SeverityError {
			discoveryFailures++
		}
	}
	importFailures := 0
	for _, source := range discovered.Sources {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		subscription, _, err := r.RequestImport(source.ID)
		if err != nil {
			importFailures++
			result.Imports = append(result.Imports, ImportProgress{SourceID: source.ID, Phase: ImportFailed, Failure: err})
			continue
		}
		terminal := waitForTerminal(ctx, subscription)
		subscription.Close()
		result.Imports = append(result.Imports, terminal)
		if terminal.Failure != nil {
			importFailures++
		}
	}
	if discoveryFailures > 0 || importFailures > 0 {
		return result, &BatchImportError{DiscoveryFailures: discoveryFailures, ImportFailures: importFailures}
	}
	return result, nil
}

// waitForTerminal observes import progress until completion or caller cancellation.
func waitForTerminal(ctx context.Context, subscription *ImportSubscription) ImportProgress {
	var last ImportProgress
	for {
		select {
		case <-ctx.Done():
			last.Failure = ctx.Err()
			last.Phase = ImportFailed
			return last
		case progress, ok := <-subscription.Updates():
			if !ok {
				return last
			}
			last = progress
		}
	}
}
