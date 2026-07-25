package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/pooya79/AgentSession/internal/model"
)

const importAllConcurrency = 2

type ImportAllPhase string

const (
	ImportAllUnavailable ImportAllPhase = "unavailable"
	ImportAllIndexing    ImportAllPhase = "indexing"
	ImportAllUpToDate    ImportAllPhase = "up_to_date"
	ImportAllIssues      ImportAllPhase = "completed_with_issues"
)

type ImportAllDiagnostic struct {
	SourceID     model.SourceID
	SourcePath   string
	Code         string
	Severity     model.Severity
	Message      string
	EventIDs     []model.EventID
	RawRecordIDs []model.RawRecordID
}

type ImportAllSourceStatus struct {
	ID          model.SourceID
	Kind        string
	Path        string
	Origin      string
	Phase       ImportPhase
	Failure     string
	Records     int64
	Events      int64
	Sessions    int64
	Unchanged   int64
	Diagnostics int64
	Complete    bool
}

type ImportAllStatus struct {
	RunID              uint64
	Phase              ImportAllPhase
	Active             bool
	SourcesDiscovered  int64
	SourcesCompleted   int64
	SourcesFailed      int64
	RecordsProcessed   int64
	EventsProcessed    int64
	SessionsObserved   int64
	UnchangedSessions  int64
	DiagnosticsTotal   int64
	DiagnosticsOmitted int64
	RecentDiagnostics  []ImportAllDiagnostic
	Sources            []ImportAllSourceStatus
	Failure            string
}

type ImportAllStart struct {
	Status ImportAllStatus
	Joined bool
}

type importAllCoordinator struct {
	mu                   sync.Mutex
	discover             func(context.Context) (SourceDiscovery, error)
	start                func(context.Context, model.SourceID) (ImportStart, error)
	latest               ImportAllStatus
	active               bool
	stopping             bool
	cancel               context.CancelFunc
	wg                   sync.WaitGroup
	discoveryDiagnostics int64
}

func newImportAllCoordinator(
	discover func(context.Context) (SourceDiscovery, error),
	start func(context.Context, model.SourceID) (ImportStart, error),
) *importAllCoordinator {
	return &importAllCoordinator{discover: discover, start: start, latest: ImportAllStatus{Phase: ImportAllUnavailable}}
}

func (c *importAllCoordinator) Start(ctx context.Context) (ImportAllStart, error) {
	if err := ctx.Err(); err != nil {
		return ImportAllStart{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopping {
		return ImportAllStart{}, ErrShuttingDown
	}
	if c.active {
		return ImportAllStart{Status: cloneImportAllStatus(c.latest), Joined: true}, nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.active = true
	c.latest.RunID++
	c.latest = ImportAllStatus{RunID: c.latest.RunID, Phase: ImportAllIndexing, Active: true}
	c.discoveryDiagnostics = 0
	c.wg.Add(1)
	go c.run(runCtx, c.latest.RunID)
	return ImportAllStart{Status: cloneImportAllStatus(c.latest)}, nil
}

func (c *importAllCoordinator) Status(ctx context.Context) (ImportAllStatus, error) {
	if err := ctx.Err(); err != nil {
		return ImportAllStatus{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneImportAllStatus(c.latest), nil
}

func (c *importAllCoordinator) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	c.stopping = true
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *importAllCoordinator) run(ctx context.Context, runID uint64) {
	defer c.wg.Done()
	discovery, err := c.discover(ctx)
	if err != nil {
		c.finishDiscoveryFailure(runID, err)
		return
	}

	c.mu.Lock()
	if c.latest.RunID != runID {
		c.mu.Unlock()
		return
	}
	for _, source := range discovery.Sources {
		c.latest.Sources = append(c.latest.Sources, ImportAllSourceStatus{
			ID: source.ID, Kind: source.Kind, Path: source.Path, Origin: source.Origin, Phase: ImportQueued,
		})
	}
	c.latest.SourcesDiscovered = int64(len(discovery.Sources))
	for _, diagnostic := range discovery.Diagnostics {
		c.discoveryDiagnostics++
		c.appendDiagnosticLocked(ImportAllDiagnostic{
			SourcePath: diagnostic.Path, Code: diagnostic.Code, Severity: diagnostic.Severity, Message: diagnostic.Message,
		})
	}
	c.recomputeLocked()
	c.mu.Unlock()

	jobs := make(chan SourceSummary)
	var workers sync.WaitGroup
	for range importAllConcurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for source := range jobs {
				if ctx.Err() != nil {
					return
				}
				c.importSource(ctx, runID, source)
			}
		}()
	}
schedule:
	for _, source := range discovery.Sources {
		select {
		case jobs <- source:
		case <-ctx.Done():
			break schedule
		}
	}
	close(jobs)
	workers.Wait()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.latest.RunID != runID {
		return
	}
	c.active = false
	c.latest.Active = false
	c.recomputeLocked()
	if ctx.Err() != nil {
		c.latest.Phase = ImportAllUnavailable
		c.latest.Failure = ctx.Err().Error()
	} else if c.latest.SourcesFailed > 0 || c.latest.DiagnosticsTotal > 0 {
		c.latest.Phase = ImportAllIssues
	} else {
		c.latest.Phase = ImportAllUpToDate
	}
}

func (c *importAllCoordinator) importSource(ctx context.Context, runID uint64, source SourceSummary) {
	started, err := c.start(ctx, source.ID)
	if err != nil || started.Subscription == nil {
		if err == nil {
			err = errors.New("import subscription is unavailable")
		}
		c.updateSource(runID, source.ID, ImportProgress{SourceID: source.ID, Phase: ImportFailed, Failure: err})
		return
	}
	defer started.Subscription.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case progress, ok := <-started.Subscription.Updates():
			if !ok {
				return
			}
			c.updateSource(runID, source.ID, progress)
		}
	}
}

func (c *importAllCoordinator) updateSource(runID uint64, sourceID model.SourceID, progress ImportProgress) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.latest.RunID != runID {
		return
	}
	for i := range c.latest.Sources {
		source := &c.latest.Sources[i]
		if source.ID != sourceID {
			continue
		}
		source.Phase = progress.Phase
		source.Records = progress.RecordsProcessed
		source.Events = progress.EventsProcessed
		source.Sessions = progress.ImportResultsObserved
		source.Unchanged = progress.UnchangedResultsObserved
		previousDiagnostics := source.Diagnostics
		source.Diagnostics = progress.DiagnosticsObserved
		source.Complete = progress.Complete
		if progress.Failure != nil {
			source.Failure = progress.Failure.Error()
		}
		newCount := progress.DiagnosticsObserved - previousDiagnostics
		start := len(progress.RecentDiagnostics) - int(newCount)
		if start < 0 {
			start = 0
		}
		for _, diagnostic := range progress.RecentDiagnostics[start:] {
			c.appendDiagnosticLocked(ImportAllDiagnostic{
				SourceID: sourceID, SourcePath: source.Path, Code: diagnostic.Code, Severity: diagnostic.Severity,
				Message: diagnostic.Message, EventIDs: diagnostic.EventIDs, RawRecordIDs: diagnostic.RawRecordIDs,
			})
		}
		break
	}
	c.recomputeLocked()
}

func (c *importAllCoordinator) appendDiagnosticLocked(diagnostic ImportAllDiagnostic) {
	c.latest.RecentDiagnostics = append(c.latest.RecentDiagnostics, diagnostic)
	if excess := len(c.latest.RecentDiagnostics) - DefaultRecentDiagnostics; excess > 0 {
		copy(c.latest.RecentDiagnostics, c.latest.RecentDiagnostics[excess:])
		c.latest.RecentDiagnostics = c.latest.RecentDiagnostics[:DefaultRecentDiagnostics]
	}
}

func (c *importAllCoordinator) recomputeLocked() {
	var completed, failed, records, events, sessions, unchanged, diagnostics int64
	for _, source := range c.latest.Sources {
		records += source.Records
		events += source.Events
		sessions += source.Sessions
		unchanged += source.Unchanged
		diagnostics += source.Diagnostics
		if source.Failure != "" {
			failed++
			completed++
		} else if source.Complete {
			completed++
		}
	}
	c.latest.SourcesCompleted = completed
	c.latest.SourcesFailed = failed
	c.latest.RecordsProcessed = records
	c.latest.EventsProcessed = events
	c.latest.SessionsObserved = sessions
	c.latest.UnchangedSessions = unchanged
	c.latest.DiagnosticsTotal = diagnostics + c.discoveryDiagnostics
	c.latest.DiagnosticsOmitted = c.latest.DiagnosticsTotal - int64(len(c.latest.RecentDiagnostics))
	if c.latest.DiagnosticsOmitted < 0 {
		c.latest.DiagnosticsOmitted = 0
	}
}

func (c *importAllCoordinator) finishDiscoveryFailure(runID uint64, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.latest.RunID != runID {
		return
	}
	c.active = false
	c.latest.Active = false
	c.latest.Phase = ImportAllUnavailable
	c.latest.Failure = fmt.Sprintf("discover sources: %v", err)
}

func cloneImportAllStatus(status ImportAllStatus) ImportAllStatus {
	status.Sources = append([]ImportAllSourceStatus(nil), status.Sources...)
	status.RecentDiagnostics = append([]ImportAllDiagnostic(nil), status.RecentDiagnostics...)
	for i := range status.RecentDiagnostics {
		status.RecentDiagnostics[i].EventIDs = append([]model.EventID(nil), status.RecentDiagnostics[i].EventIDs...)
		status.RecentDiagnostics[i].RawRecordIDs = append([]model.RawRecordID(nil), status.RecentDiagnostics[i].RawRecordIDs...)
	}
	return status
}
