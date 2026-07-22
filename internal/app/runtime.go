package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/pooya79/AgentSession/internal/adapter/claude"
	"github.com/pooya79/AgentSession/internal/adapter/codex"
	"github.com/pooya79/AgentSession/internal/adapter/opencode"
	"github.com/pooya79/AgentSession/internal/discovery"
	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/projection"
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

func (e *BatchImportError) Error() string {
	return fmt.Sprintf("import completed with %d discovery failure(s) and %d source failure(s)", e.DiscoveryFailures, e.ImportFailures)
}

// Runtime owns all long-lived application infrastructure.
type Runtime struct {
	paths       RuntimePaths
	db          *sql.DB
	discoverer  *discovery.Discoverer
	store       *sqlitestore.ImportStore
	imports     *ImportManager
	explorer    Explorer
	projections *ProjectionService

	mu       sync.RWMutex
	catalog  map[model.SourceID]discovery.Source
	closing  bool
	closed   bool
	shutdown sync.Mutex
}

// OpenRuntime creates the private database directory, migrates SQLite, and
// composes discovery, adapters, importing, projections, and read services.
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
	db, err := sqlitestore.Open(ctx, paths.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("open runtime: %w", err)
	}
	store, err := sqlitestore.NewImportStore(db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open runtime: %w", err)
	}
	projectionManager, err := projection.NewManager(ctx, store, store, nil)
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
	return &Runtime{
		paths: paths, db: db, discoverer: discoverer, store: store, imports: manager, explorer: explorer,
		projections: NewProjectionService(projectionManager), catalog: make(map[model.SourceID]discovery.Source),
	}, nil
}

func currentPathInputs() (PathInputs, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return PathInputs{}, fmt.Errorf("resolve user home directory: %w", err)
	}
	working, err := os.Getwd()
	if err != nil {
		return PathInputs{}, fmt.Errorf("resolve working directory: %w", err)
	}
	return PathInputs{GOOS: runtime.GOOS, HomeDir: home, WorkingDir: working, LookupEnv: os.LookupEnv}, nil
}

func (r *Runtime) Paths() RuntimePaths                      { return r.paths }
func (r *Runtime) ImportManager() *ImportManager            { return r.imports }
func (r *Runtime) Reader() storage.SessionReader            { return r.store }
func (r *Runtime) AuthoritativeReader() AuthoritativeReader { return r.store }
func (r *Runtime) ProjectionService() *ProjectionService    { return r.projections }
func (r *Runtime) Projections() *ProjectionService          { return r.projections }
func (r *Runtime) Explorer() Explorer                       { return r.explorer }

func (r *Runtime) ListSessions(ctx context.Context, request ListSessionsRequest) (SessionPage, error) {
	return r.explorer.ListSessions(ctx, request)
}

func (r *Runtime) Timeline(ctx context.Context, request TimelineRequest) (TimelinePage, error) {
	return r.explorer.Timeline(ctx, request)
}

func (r *Runtime) EventDetail(ctx context.Context, request EventDetailRequest) (EventDetail, error) {
	return r.explorer.EventDetail(ctx, request)
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
		return nil, false, fmt.Errorf("request import: discovered source %q was not found", sourceID)
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

func (r *Runtime) accepting() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closing || r.closed {
		return ErrShuttingDown
	}
	return nil
}

// Shutdown rejects new work, cancels and settles imports, then closes SQLite.
// A context timeout leaves database closure retryable.
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
	if err := r.imports.Shutdown(ctx); err != nil {
		return err
	}
	if err := r.db.Close(); err != nil {
		return fmt.Errorf("shutdown runtime: close database: %w", err)
	}
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return nil
}

func sortDiscoverySources(sources []discovery.Source) {
	for i := 1; i < len(sources); i++ {
		for j := i; j > 0 && (sources[j].Kind < sources[j-1].Kind || (sources[j].Kind == sources[j-1].Kind && sources[j].Path < sources[j-1].Path)); j-- {
			sources[j], sources[j-1] = sources[j-1], sources[j]
		}
	}
}
