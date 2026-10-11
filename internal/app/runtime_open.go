package app

import (
	"context"
	"fmt"
	"os"

	"github.com/pooya79/AgentSession/internal/adapter/claude"
	"github.com/pooya79/AgentSession/internal/adapter/codex"
	"github.com/pooya79/AgentSession/internal/adapter/opencode"
	"github.com/pooya79/AgentSession/internal/analysis"
	"github.com/pooya79/AgentSession/internal/discovery"
	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/projection"
	searchprojection "github.com/pooya79/AgentSession/internal/search"
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
	findingsBuilder, err := analysis.NewBuilder(store)
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
		if definition.Kind == projection.KindFindings {
			registration.Builder = findingsBuilder
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
	assessor, err := NewAssessor(store)
	if err != nil {
		_ = manager.Shutdown(context.Background())
		_ = db.Close()
		return nil, err
	}
	runtime := &Runtime{
		paths: paths, db: db, discoverer: discoverer, store: store, imports: manager, explorer: explorer, searcher: searcher, assessor: assessor,
		projections: NewProjectionService(projectionManager), catalog: make(map[model.SourceID]discovery.Source),
		databaseLock: databaseLock,
	}
	runtime.importAll = newImportAllCoordinator(runtime.DiscoverSources, runtime.StartImport)
	keepDatabaseLock = true
	return runtime, nil
}
