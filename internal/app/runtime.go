package app

import (
	"database/sql"
	"sync"

	"github.com/pooya79/AgentSession/internal/discovery"
	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/projection"
	"github.com/pooya79/AgentSession/internal/storage"
	sqlitestore "github.com/pooya79/AgentSession/internal/storage/sqlite"
)

// AuthoritativeReader combines lightweight retained evidence reads with the
// ordered full-event view used by projection builders.
type AuthoritativeReader interface {
	storage.SessionReader
	projection.Reader
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
