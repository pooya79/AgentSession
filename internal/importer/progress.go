package importer

import (
	"sync"

	"github.com/pooya79/AgentSession/internal/model"
)

// ProgressPhase identifies the coordinator operation currently in progress.
type ProgressPhase string

const (
	PhaseProbing     ProgressPhase = "probing"
	PhasePreparing   ProgressPhase = "preparing"
	PhaseVerifying   ProgressPhase = "verifying"
	PhaseImporting   ProgressPhase = "importing"
	PhaseReconciling ProgressPhase = "reconciling"
	PhaseCommitting  ProgressPhase = "committing"
	PhaseProjecting  ProgressPhase = "projecting"
	PhaseFinalizing  ProgressPhase = "finalizing"
)

// Progress is a cumulative, source-neutral snapshot emitted by Coordinator.
// Diagnostics contains only diagnostics newly observed at this update; the
// application layer owns retention and fan-out policy.
type Progress struct {
	SourceID            model.SourceID
	ActiveSourceID      model.SourceID
	Phase               ProgressPhase
	RecordsProcessed    int64
	EventsProcessed     int64
	RecordsCommitted    int64
	BatchesCommitted    int64
	DiagnosticsObserved int64
	Diagnostics         []model.Diagnostic
}

// ProgressObserver receives synchronous coordinator progress. Implementations
// must return promptly; application fan-out should never wait for consumers.
type ProgressObserver func(Progress)

type progressTracker struct {
	mu       sync.Mutex
	observer ProgressObserver
	progress Progress
}

func newProgressTracker(sourceID model.SourceID, observer ProgressObserver) *progressTracker {
	return &progressTracker{observer: observer, progress: Progress{SourceID: sourceID, ActiveSourceID: sourceID}}
}

func (t *progressTracker) publish(phase ProgressPhase, diagnostics []model.Diagnostic) {
	if t == nil || t.observer == nil {
		return
	}
	t.mu.Lock()
	t.progress.Phase = phase
	t.progress.DiagnosticsObserved += int64(len(diagnostics))
	t.progress.Diagnostics = cloneDiagnostics(diagnostics)
	snapshot := cloneProgress(t.progress)
	t.mu.Unlock()
	t.observer(snapshot)
}

func (t *progressTracker) activate(sourceID model.SourceID) {
	if t == nil || t.observer == nil {
		return
	}
	t.mu.Lock()
	t.progress.ActiveSourceID = sourceID
	t.mu.Unlock()
}

func (t *progressTracker) processed(envelope RecordEnvelope, phase ProgressPhase) {
	if t == nil || t.observer == nil {
		return
	}
	t.mu.Lock()
	t.progress.Phase = phase
	t.progress.RecordsProcessed++
	t.progress.EventsProcessed += int64(len(envelope.Events))
	t.progress.DiagnosticsObserved += int64(len(envelope.Diagnostics))
	t.progress.Diagnostics = cloneDiagnostics(envelope.Diagnostics)
	snapshot := cloneProgress(t.progress)
	observer := t.observer
	t.mu.Unlock()
	if observer != nil {
		observer(snapshot)
	}
}

func (t *progressTracker) committed(records, batches int64, nextPhase ProgressPhase) {
	if t == nil || t.observer == nil {
		return
	}
	t.mu.Lock()
	t.progress.RecordsCommitted += records
	t.progress.BatchesCommitted += batches
	t.progress.Phase = nextPhase
	t.progress.Diagnostics = nil
	snapshot := cloneProgress(t.progress)
	observer := t.observer
	t.mu.Unlock()
	if observer != nil {
		observer(snapshot)
	}
}

func cloneProgress(progress Progress) Progress {
	progress.Diagnostics = cloneDiagnostics(progress.Diagnostics)
	return progress
}

func cloneDiagnostics(diagnostics []model.Diagnostic) []model.Diagnostic {
	if len(diagnostics) == 0 {
		return nil
	}
	cloned := make([]model.Diagnostic, len(diagnostics))
	for i, diagnostic := range diagnostics {
		cloned[i] = diagnostic
		cloned[i].EventIDs = append([]model.EventID(nil), diagnostic.EventIDs...)
		cloned[i].RawRecordIDs = append([]model.RawRecordID(nil), diagnostic.RawRecordIDs...)
	}
	return cloned
}
