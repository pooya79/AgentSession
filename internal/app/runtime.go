package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/pooya79/AgentSession/internal/adapter/claude"
	"github.com/pooya79/AgentSession/internal/adapter/codex"
	"github.com/pooya79/AgentSession/internal/adapter/opencode"
	"github.com/pooya79/AgentSession/internal/discovery"
	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/projection"
	searchprojection "github.com/pooya79/AgentSession/internal/search"
	"github.com/pooya79/AgentSession/internal/storage"
	sqlitestore "github.com/pooya79/AgentSession/internal/storage/sqlite"
)

// RuntimeConfig controls application composition and provides deterministic
// environment seams for tests. Zero values use the current process.
type RuntimeConfig struct {
	DataDir         string
	ConfigDir       string
	Paths           PathOptions
	PathInputs      *PathInputs
	DiscoveryInputs *discovery.Inputs
	ExplicitPaths   []discovery.ConfiguredPath
	ImporterOptions importer.Options
	ManagerOptions  ImportManagerOptions
}

// AuthoritativeReader combines lightweight retained evidence reads with the
// ordered full-event view used by projection builders.
type AuthoritativeReader interface {
	storage.SessionReader
	projection.Reader
}

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

// ErrSourceNotFound means an import ID is absent from the latest successful
// discovery catalog.
var ErrSourceNotFound = errors.New("discovered source was not found")

// Error returns a presentation-safe count of discovery and import failures.
func (e *BatchImportError) Error() string {
	return fmt.Sprintf("import completed with %d discovery failure(s) and %d source failure(s)", e.DiscoveryFailures, e.ImportFailures)
}

// Runtime owns all long-lived application infrastructure.
type Runtime struct {
	paths        RuntimePaths
	db           *sql.DB
	discoverer   *discovery.Discoverer
	store        *sqlitestore.ImportStore
	imports      *ImportManager
	importAll    *importAllCoordinator
	explorer     Explorer
	searcher     Searcher
	projections  *ProjectionService
	databaseLock *databaseLock

	mu       sync.RWMutex
	catalog  map[model.SourceID]discovery.Source
	closing  bool
	closed   bool
	shutdown sync.Mutex
}

// OpenRuntime creates the private database directory, acquires a shared
// maintenance lock, migrates SQLite, and composes discovery, adapters,
// importing, projections, and read services.
func OpenRuntime(ctx context.Context, config RuntimeConfig) (*Runtime, error) {
	var pathInputs PathInputs
	var err error
	if config.PathInputs != nil {
		pathInputs = *config.PathInputs
	} else {
		pathInputs, err = currentPathInputs()
		if err != nil {
			return nil, err
		}
	}
	pathOptions := config.Paths
	if config.DataDir != "" {
		pathOptions.DataDir = config.DataDir
	}
	if config.ConfigDir != "" {
		pathOptions.ConfigDir = config.ConfigDir
	}
	paths, err := ResolveRuntimePaths(pathInputs, pathOptions)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(paths.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("open runtime: create private data directory: %w", err)
	}
	if err := os.Chmod(paths.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("open runtime: protect data directory: %w", err)
	}
	databaseLock, err := acquireDatabaseLock(paths.DatabasePath, false)
	if err != nil {
		return nil, fmt.Errorf("open runtime: %w", err)
	}
	keepDatabaseLock := false
	defer func() {
		if !keepDatabaseLock {
			_ = databaseLock.release()
		}
	}()
	db, err := sqlitestore.Open(ctx, paths.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("open runtime: %w", err)
	}
	store, err := sqlitestore.NewImportStore(db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open runtime: %w", err)
	}
	searchBuilder, err := searchprojection.NewBuilder(store)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open runtime: %w", err)
	}
	registrations := make([]projection.Registration, 0, len(projection.Kinds()))
	for _, definition := range projection.DefaultDefinitions() {
		registration := projection.Registration{Definition: definition}
		if definition.Kind == projection.KindSearch {
			registration.Builder = searchBuilder
		}
		registrations = append(registrations, registration)
	}
	projectionManager, err := projection.NewManager(ctx, store, store, registrations)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open runtime: %w", err)
	}
	coordinator, err := importer.NewCoordinator(store, []importer.Adapter{codex.New(), claude.New(), opencode.New()}, projectionManager, config.ImporterOptions)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open runtime: %w", err)
	}
	manager, err := NewImportManager(coordinator.ImportAllObserved, config.ManagerOptions)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open runtime: %w", err)
	}
	explorer, err := NewExplorer(store)
	if err != nil {
		_ = manager.Shutdown(context.Background())
		_ = db.Close()
		return nil, fmt.Errorf("open runtime: %w", err)
	}
	searcher, err := NewSearcher(store)
	if err != nil {
		_ = manager.Shutdown(context.Background())
		_ = db.Close()
		return nil, fmt.Errorf("open runtime: %w", err)
	}

	var discoverer *discovery.Discoverer
	if config.DiscoveryInputs != nil {
		inputs := *config.DiscoveryInputs
		inputs.ExplicitPaths = append(append([]discovery.ConfiguredPath(nil), inputs.ExplicitPaths...), config.ExplicitPaths...)
		discoverer, err = discovery.New(inputs)
	} else {
		discoverer, err = discovery.NewOS(config.ExplicitPaths)
	}
	if err != nil {
		_ = manager.Shutdown(context.Background())
		_ = db.Close()
		return nil, fmt.Errorf("open runtime: %w", err)
	}
	runtime := &Runtime{
		paths: paths, db: db, discoverer: discoverer, store: store, imports: manager, explorer: explorer, searcher: searcher,
		projections: NewProjectionService(projectionManager), catalog: make(map[model.SourceID]discovery.Source),
		databaseLock: databaseLock,
	}
	runtime.importAll = newImportAllCoordinator(runtime.DiscoverSources, runtime.StartImport)
	keepDatabaseLock = true
	return runtime, nil
}

// Paths returns the private index and configuration paths owned by AgentSession.
func (r *Runtime) Paths() RuntimePaths { return r.paths }

// ImportManager returns the runtime-owned incremental import coordinator.
func (r *Runtime) ImportManager() *ImportManager { return r.imports }

// Reader returns read-only access to retained canonical session evidence.
func (r *Runtime) Reader() storage.SessionReader { return r.store }

// AuthoritativeReader returns the canonical reader shared with projection builders.
func (r *Runtime) AuthoritativeReader() AuthoritativeReader { return r.store }

// ProjectionService returns the runtime-owned derived-data coordinator.
func (r *Runtime) ProjectionService() *ProjectionService { return r.projections }

// Projections returns the shared projection application service.
func (r *Runtime) Projections() *ProjectionService { return r.projections }

// Explorer returns the shared bounded canonical exploration service.
func (r *Runtime) Explorer() Explorer { return r.explorer }

// Search delegates to the shared lifecycle-aware search service.
func (r *Runtime) Search(ctx context.Context, request SearchRequest) (SearchPage, error) {
	return r.searcher.Search(ctx, request)
}

// LibraryOverview reports aggregate counts over committed canonical evidence.
func (r *Runtime) LibraryOverview(ctx context.Context) (LibraryOverview, error) {
	return r.explorer.LibraryOverview(ctx)
}

// ListSessions delegates bounded session exploration to the shared service.
func (r *Runtime) ListSessions(ctx context.Context, request ListSessionsRequest) (SessionPage, error) {
	page, err := r.explorer.ListSessions(ctx, request)
	if err != nil {
		return page, err
	}
	for index := range page.Sessions {
		status, statusErr := r.projections.ProjectionStatus(ctx, page.Sessions[index].ID)
		if statusErr != nil {
			if errors.Is(statusErr, context.Canceled) || errors.Is(statusErr, context.DeadlineExceeded) {
				return page, statusErr
			}
			continue
		}
		page.Sessions[index].Projections = status.Summary
	}
	return page, nil
}

// Timeline delegates bounded canonical timeline reads to the shared service.
func (r *Runtime) Timeline(ctx context.Context, request TimelineRequest) (TimelinePage, error) {
	return r.explorer.Timeline(ctx, request)
}

// EventDetail delegates explicit event payload reads to the shared service.
func (r *Runtime) EventDetail(ctx context.Context, request EventDetailRequest) (EventDetail, error) {
	return r.explorer.EventDetail(ctx, request)
}

// EventLocations resolves bounded diagnostic references without loading payloads.
func (r *Runtime) EventLocations(ctx context.Context, eventIDs []model.EventID) (map[model.EventID]EventLocation, error) {
	return r.explorer.EventLocations(ctx, eventIDs)
}

// InspectUnknownEvidence delegates the explicit retained-evidence action.
func (r *Runtime) InspectUnknownEvidence(ctx context.Context, sessionID model.SessionID, eventID model.EventID) (UnknownEvidenceInspection, error) {
	return r.explorer.InspectUnknownEvidence(ctx, sessionID, eventID)
}

// ProjectionStatus exposes the runtime-owned projection lifecycle service.
func (r *Runtime) ProjectionStatus(ctx context.Context, sessionID model.SessionID) (ProjectionStatus, error) {
	return r.projections.ProjectionStatus(ctx, sessionID)
}

// RetryProjections admits retry work only while the runtime accepts new work.
func (r *Runtime) RetryProjections(ctx context.Context, sessionID model.SessionID) (ProjectionAction, error) {
	if err := r.accepting(); err != nil {
		return ProjectionAction{}, err
	}
	return r.projections.RetryProjections(ctx, sessionID)
}

// RebuildProjections admits rebuild work only while the runtime accepts new
// work.
func (r *Runtime) RebuildProjections(ctx context.Context, sessionID model.SessionID, kind string) (ProjectionAction, error) {
	if err := r.accepting(); err != nil {
		return ProjectionAction{}, err
	}
	return r.projections.RebuildProjections(ctx, sessionID, kind)
}

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
		Open: func(ctx context.Context) (io.ReadCloser, error) { return openAt(ctx, 0) }, OpenAt: openAt,
	}, nil
}

// accepting rejects new work once runtime shutdown begins.
func (r *Runtime) accepting() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closing || r.closed {
		return ErrShuttingDown
	}
	return nil
}

// Shutdown rejects new work, cancels and settles imports, closes SQLite, and
// releases the maintenance lock. A context timeout leaves database closure
// retryable.
func (r *Runtime) Shutdown(ctx context.Context) error {
	r.shutdown.Lock()
	defer r.shutdown.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closing = true
	r.mu.Unlock()
	if err := r.importAll.Shutdown(ctx); err != nil {
		return err
	}
	if err := r.imports.Shutdown(ctx); err != nil {
		return err
	}
	if err := r.projections.Shutdown(ctx); err != nil {
		return err
	}
	if err := r.db.Close(); err != nil {
		return fmt.Errorf("shutdown runtime: close database: %w", err)
	}
	if err := r.databaseLock.release(); err != nil {
		return fmt.Errorf("shutdown runtime: %w", err)
	}
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return nil
}

// sortDiscoverySources establishes deterministic source ordering for presentation and imports.
func sortDiscoverySources(sources []discovery.Source) {
	for i := 1; i < len(sources); i++ {
		for j := i; j > 0 && (sources[j].Kind < sources[j-1].Kind || (sources[j].Kind == sources[j-1].Kind && sources[j].Path < sources[j-1].Path)); j-- {
			sources[j], sources[j-1] = sources[j-1], sources[j]
		}
	}
}
