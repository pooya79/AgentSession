// Package app owns shared application workflows used by every presentation.
package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

// DefaultRecentDiagnostics bounds diagnostic details retained for late and
// slow observers. The cumulative diagnostic count remains exact.
const DefaultRecentDiagnostics = 32

// ErrShuttingDown means the manager has permanently stopped accepting work.
var ErrShuttingDown = errors.New("application is shutting down")

// ImportPhase is the presentation-neutral lifecycle state of an import job.
type ImportPhase string

const (
	ImportQueued      ImportPhase = "queued"
	ImportProbing     ImportPhase = "probing"
	ImportPreparing   ImportPhase = "preparing"
	ImportVerifying   ImportPhase = "verifying"
	ImportImporting   ImportPhase = "importing"
	ImportReconciling ImportPhase = "reconciling"
	ImportCommitting  ImportPhase = "committing"
	ImportProjecting  ImportPhase = "projecting"
	ImportFinalizing  ImportPhase = "finalizing"
	ImportCompleted   ImportPhase = "completed"
	ImportFailed      ImportPhase = "failed"
)

// ImportProgress is an immutable cumulative snapshot of one application job.
type ImportProgress struct {
	RunID               uint64
	SourceID            model.SourceID
	ActiveSourceID      model.SourceID
	Phase               ImportPhase
	RecordsProcessed    int64
	EventsProcessed     int64
	RecordsCommitted    int64
	BatchesCommitted    int64
	DiagnosticsObserved int64
	DiagnosticsOmitted  int64
	RecentDiagnostics   []model.Diagnostic
	Complete            bool
	Failure             error
}

// ImportFunc is the synchronous importer operation managed by ImportManager.
type ImportFunc func(context.Context, importer.Source, importer.ProgressObserver) ([]importer.ImportResult, error)

// ImportManager owns import execution, cancellation, coalescing, and fan-out.
type ImportManager struct {
	mu              sync.Mutex
	run             ImportFunc
	recentLimit     int
	jobs            map[model.SourceID]*importJob
	nextRunID       uint64
	shuttingDown    bool
	shutdownStarted bool
	settled         chan struct{}
	wg              sync.WaitGroup
}

// ImportManagerOptions controls bounded in-memory lifecycle state.
type ImportManagerOptions struct {
	RecentDiagnostics int
}

func NewImportManager(run ImportFunc, options ImportManagerOptions) (*ImportManager, error) {
	if run == nil {
		return nil, fmt.Errorf("import manager: import function is required")
	}
	limit := options.RecentDiagnostics
	if limit == 0 {
		limit = DefaultRecentDiagnostics
	}
	if limit < 0 {
		return nil, fmt.Errorf("import manager: recent diagnostic limit must not be negative")
	}
	return &ImportManager{
		run: run, recentLimit: limit, jobs: make(map[model.SourceID]*importJob), settled: make(chan struct{}),
	}, nil
}

// Request starts an import or attaches to the active import for source.ID.
// The returned boolean reports whether the request joined existing work.
func (m *ImportManager) Request(source importer.Source) (*ImportSubscription, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shuttingDown {
		return nil, false, ErrShuttingDown
	}
	if err := source.Validate(); err != nil {
		return nil, false, fmt.Errorf("request import: %w", err)
	}
	if existing := m.jobs[source.ID]; existing != nil {
		return existing.subscribe(), true, nil
	}

	m.nextRunID++
	ctx, cancel := context.WithCancel(context.Background())
	job := &importJob{
		manager: m, source: source, ctx: ctx, cancel: cancel,
		subscribers: make(map[*ImportSubscription]chan ImportProgress), recentLimit: m.recentLimit,
		latest: ImportProgress{RunID: m.nextRunID, SourceID: source.ID, ActiveSourceID: source.ID, Phase: ImportQueued},
	}
	m.jobs[source.ID] = job
	m.wg.Add(1)
	subscription := job.subscribe()
	go job.run()
	return subscription, false, nil
}

// Shutdown rejects new work, cancels active imports, and waits for each runner
// to return after its current storage transaction has committed or rolled back.
func (m *ImportManager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	if !m.shutdownStarted {
		m.shutdownStarted = true
		m.shuttingDown = true
		jobs := make([]*importJob, 0, len(m.jobs))
		for _, job := range m.jobs {
			jobs = append(jobs, job)
		}
		go func() {
			m.wg.Wait()
			close(m.settled)
		}()
		m.mu.Unlock()
		for _, job := range jobs {
			job.cancel()
		}
	} else {
		m.mu.Unlock()
	}

	select {
	case <-m.settled:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ImportSubscription is one independently detachable view of shared work.
type ImportSubscription struct {
	job     *importJob
	updates <-chan ImportProgress
	once    sync.Once
}

func (s *ImportSubscription) Updates() <-chan ImportProgress { return s.updates }

// Close detaches this observer. It never cancels the underlying import.
func (s *ImportSubscription) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() { s.job.detach(s) })
}

type importJob struct {
	manager     *ImportManager
	source      importer.Source
	ctx         context.Context
	cancel      context.CancelFunc
	recentLimit int

	mu          sync.Mutex
	latest      ImportProgress
	subscribers map[*ImportSubscription]chan ImportProgress
	finished    bool
}

func (j *importJob) subscribe() *ImportSubscription {
	j.mu.Lock()
	defer j.mu.Unlock()
	updates := make(chan ImportProgress, 1)
	subscription := &ImportSubscription{job: j, updates: updates}
	updates <- cloneImportProgress(j.latest)
	if j.finished {
		close(updates)
		return subscription
	}
	j.subscribers[subscription] = updates
	return subscription
}

func (j *importJob) detach(subscription *ImportSubscription) {
	j.mu.Lock()
	defer j.mu.Unlock()
	updates, exists := j.subscribers[subscription]
	if !exists {
		return
	}
	delete(j.subscribers, subscription)
	close(updates)
}

func (j *importJob) run() {
	defer j.manager.wg.Done()
	_, err := j.manager.run(j.ctx, j.source, j.observe)

	j.manager.mu.Lock()
	if j.manager.jobs[j.source.ID] == j {
		delete(j.manager.jobs, j.source.ID)
	}
	j.manager.mu.Unlock()

	j.finish(err)
}

func (j *importJob) observe(progress importer.Progress) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.finished {
		return
	}
	j.latest.ActiveSourceID = progress.ActiveSourceID
	j.latest.Phase = ImportPhase(progress.Phase)
	j.latest.RecordsProcessed = progress.RecordsProcessed
	j.latest.EventsProcessed = progress.EventsProcessed
	j.latest.RecordsCommitted = progress.RecordsCommitted
	j.latest.BatchesCommitted = progress.BatchesCommitted
	j.latest.DiagnosticsObserved = progress.DiagnosticsObserved
	j.appendDiagnostics(progress.Diagnostics)
	j.publishLocked()
}

func (j *importJob) appendDiagnostics(diagnostics []model.Diagnostic) {
	for _, diagnostic := range diagnostics {
		j.latest.RecentDiagnostics = append(j.latest.RecentDiagnostics, cloneDiagnostic(diagnostic))
	}
	if excess := len(j.latest.RecentDiagnostics) - j.recentLimit; excess > 0 {
		copy(j.latest.RecentDiagnostics, j.latest.RecentDiagnostics[excess:])
		j.latest.RecentDiagnostics = j.latest.RecentDiagnostics[:j.recentLimit]
	}
	j.latest.DiagnosticsOmitted = j.latest.DiagnosticsObserved - int64(len(j.latest.RecentDiagnostics))
	if j.latest.DiagnosticsOmitted < 0 {
		j.latest.DiagnosticsOmitted = 0
	}
}

func (j *importJob) finish(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err != nil {
		j.latest.Phase = ImportFailed
		j.latest.Failure = err
	} else {
		j.latest.Phase = ImportCompleted
		j.latest.Complete = true
	}
	j.finished = true
	j.publishLocked()
	for subscription, updates := range j.subscribers {
		close(updates)
		delete(j.subscribers, subscription)
	}
}

func (j *importJob) publishLocked() {
	snapshot := cloneImportProgress(j.latest)
	for _, updates := range j.subscribers {
		select {
		case <-updates:
		default:
		}
		updates <- cloneImportProgress(snapshot)
	}
}

func cloneImportProgress(progress ImportProgress) ImportProgress {
	if len(progress.RecentDiagnostics) == 0 {
		progress.RecentDiagnostics = nil
		return progress
	}
	diagnostics := progress.RecentDiagnostics
	progress.RecentDiagnostics = make([]model.Diagnostic, len(diagnostics))
	for i, diagnostic := range diagnostics {
		progress.RecentDiagnostics[i] = cloneDiagnostic(diagnostic)
	}
	return progress
}

func cloneDiagnostic(diagnostic model.Diagnostic) model.Diagnostic {
	diagnostic.EventIDs = append([]model.EventID(nil), diagnostic.EventIDs...)
	diagnostic.RawRecordIDs = append([]model.RawRecordID(nil), diagnostic.RawRecordIDs...)
	return diagnostic
}
