package importer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pooya79/AgentSession/internal/model"
)

const (
	DefaultBatchRecords   = 256
	DefaultBatchBytes     = 8 << 20
	DefaultMaxRecordBytes = 64 << 20
)

var (
	ErrUnsupportedSource = errors.New("no adapter supports import source")
	ErrAmbiguousSource   = errors.New("multiple adapters equally support import source")
	ErrRecordTooLarge    = errors.New("import record exceeds configured limit")
	ErrInvalidProgress   = errors.New("adapter produced invalid import progress")
)

// Options bounds the evidence retained by the coordinator before a commit.
type Options struct {
	BatchRecords   int
	BatchBytes     int64
	MaxRecordBytes int64
}

func (o Options) withDefaults() Options {
	if o.BatchRecords == 0 {
		o.BatchRecords = DefaultBatchRecords
	}
	if o.BatchBytes == 0 {
		o.BatchBytes = DefaultBatchBytes
	}
	if o.MaxRecordBytes == 0 {
		o.MaxRecordBytes = DefaultMaxRecordBytes
	}
	return o
}

func (o Options) validate() error {
	if o.BatchRecords <= 0 || o.BatchBytes <= 0 || o.MaxRecordBytes <= 0 {
		return fmt.Errorf("import limits must be positive")
	}
	return nil
}

// ProjectionRequest identifies authoritative state that is already durable.
type ProjectionRequest struct {
	SourceID   model.SourceID
	SessionID  model.SessionID
	Checkpoint ImportCheckpoint
}

// Projector performs rebuildable work after authoritative importing succeeds.
type Projector interface {
	Project(context.Context, ProjectionRequest) error
}

// ImportResult distinguishes canonical import success from projection health.
type ImportResult struct {
	SourceID         model.SourceID
	SessionID        model.SessionID
	Checkpoint       ImportCheckpoint
	RecordsCommitted int64
	BatchesCommitted int64
	CanonicalChanged bool
	Change           SourceChange
	Reconciled       bool
	ProjectionError  error
}

// Coordinator selects adapters and turns their synchronous stream into atomic
// canonical batches. It is safe to construct once, but callers must serialize
// imports for the same source at the application-work layer.
type Coordinator struct {
	store     ImportStore
	adapters  []Adapter
	projector Projector
	options   Options
}

func NewCoordinator(store ImportStore, adapters []Adapter, projector Projector, options Options) (*Coordinator, error) {
	if store == nil {
		return nil, fmt.Errorf("import coordinator: store is required")
	}
	options = options.withDefaults()
	if err := options.validate(); err != nil {
		return nil, fmt.Errorf("import coordinator: options: %w", err)
	}
	if len(adapters) == 0 {
		return nil, fmt.Errorf("import coordinator: at least one adapter is required")
	}
	seen := make(map[string]struct{}, len(adapters))
	for i, adapter := range adapters {
		if adapter == nil || strings.TrimSpace(adapter.Name()) == "" || strings.TrimSpace(string(adapter.Version())) == "" {
			return nil, fmt.Errorf("import coordinator: adapter %d has incomplete identity", i)
		}
		identity := adapter.Name() + "\x00" + string(adapter.Version())
		if _, exists := seen[identity]; exists {
			return nil, fmt.Errorf("import coordinator: duplicate adapter %q version %q", adapter.Name(), adapter.Version())
		}
		_, stream := adapter.(StreamAdapter)
		_, container := adapter.(ContainerAdapter)
		if !stream && !container {
			return nil, fmt.Errorf("import coordinator: adapter %q version %q has no preparation capability", adapter.Name(), adapter.Version())
		}
		seen[identity] = struct{}{}
	}
	return &Coordinator{store: store, adapters: append([]Adapter(nil), adapters...), projector: projector, options: options}, nil
}

func (c *Coordinator) Import(ctx context.Context, source Source) (result ImportResult, err error) {
	return c.importOne(ctx, source, nil)
}

func (c *Coordinator) importOne(ctx context.Context, source Source, progress *progressTracker) (result ImportResult, err error) {
	result.SourceID = source.ID
	if err := source.Validate(); err != nil {
		return result, fmt.Errorf("import source: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	if progress == nil {
		progress = newProgressTracker(source.ID, nil)
	}
	progress.activate(source.ID)
	progress.publish(PhaseProbing, nil)
	adapter, probe, err := c.selectAdapter(ctx, source)
	if err != nil {
		return result, err
	}
	progress.publish(PhasePreparing, probe.Diagnostics)
	streamAdapter, ok := adapter.(StreamAdapter)
	if !ok {
		return result, fmt.Errorf("import source %q with adapter %q: container source requires ImportAll", source.ID, adapter.Name())
	}
	prepared, err := streamAdapter.Prepare(ctx, source)
	if err != nil {
		return result, fmt.Errorf("import source %q with adapter %q: prepare read-only view: %w", source.ID, adapter.Name(), err)
	}
	defer func() {
		if closeErr := prepared.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("import source %q with adapter %q: close prepared view: %w", source.ID, adapter.Name(), closeErr))
		}
	}()
	return c.importPrepared(ctx, source, adapter, prepared, progress)
}

// ImportAll imports either one stream source or every logical child in a
// container snapshot. Container membership is published only after all child
// imports have completed successfully.
func (c *Coordinator) ImportAll(ctx context.Context, source Source) (results []ImportResult, err error) {
	return c.ImportAllObserved(ctx, source, nil)
}

// ImportAllObserved is ImportAll with cumulative source-neutral progress.
func (c *Coordinator) ImportAllObserved(ctx context.Context, source Source, observer ProgressObserver) (results []ImportResult, err error) {
	if err := source.Validate(); err != nil {
		return nil, fmt.Errorf("import source: %w", err)
	}
	progress := newProgressTracker(source.ID, observer)
	progress.publish(PhaseProbing, nil)
	adapter, probe, err := c.selectAdapter(ctx, source)
	if err != nil {
		return nil, err
	}
	progress.publish(PhasePreparing, probe.Diagnostics)
	if containerAdapter, ok := adapter.(ContainerAdapter); ok {
		membershipStore, ok := c.store.(ContainerMembershipStore)
		if !ok {
			return nil, fmt.Errorf("import source %q with adapter %q: store does not support container membership", source.ID, adapter.Name())
		}
		container, err := containerAdapter.PrepareContainer(ctx, source)
		if err != nil {
			return nil, fmt.Errorf("import source %q with adapter %q: prepare read-only container: %w", source.ID, adapter.Name(), err)
		}
		defer func() {
			if closeErr := container.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("import source %q with adapter %q: close prepared container: %w", source.ID, adapter.Name(), closeErr))
			}
		}()
		children, err := container.Children(ctx)
		if err != nil {
			return nil, fmt.Errorf("import source %q with adapter %q: enumerate logical sources: %w", source.ID, adapter.Name(), err)
		}
		members := make([]model.SourceID, 0, len(children))
		results = make([]ImportResult, 0, len(children))
		for i, child := range children {
			if child.Prepared == nil {
				return nil, fmt.Errorf("import source %q with adapter %q: logical source %d has no prepared view", source.ID, adapter.Name(), i)
			}
			if err := child.Source.Validate(); err != nil {
				return nil, fmt.Errorf("import source %q with adapter %q: logical source %d: %w", source.ID, adapter.Name(), i, err)
			}
			progress.activate(child.Source.ID)
			result, err := c.importPrepared(ctx, child.Source, adapter, child.Prepared, progress)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
			members = append(members, child.Source.ID)
		}
		progress.activate(source.ID)
		progress.publish(PhaseFinalizing, nil)
		if err := membershipStore.SyncContainerMembers(ctx, source.ID, members); err != nil {
			return nil, fmt.Errorf("import source %q with adapter %q: synchronize logical-source inventory: %w", source.ID, adapter.Name(), err)
		}
		return results, nil
	}
	streamAdapter, ok := adapter.(StreamAdapter)
	if !ok {
		return nil, fmt.Errorf("import source %q with adapter %q: adapter has no preparation capability", source.ID, adapter.Name())
	}
	prepared, err := streamAdapter.Prepare(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("import source %q with adapter %q: prepare read-only view: %w", source.ID, adapter.Name(), err)
	}
	defer func() {
		if closeErr := prepared.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("import source %q with adapter %q: close prepared view: %w", source.ID, adapter.Name(), closeErr))
		}
	}()
	result, err := c.importPrepared(ctx, source, adapter, prepared, progress)
	if err != nil {
		return nil, err
	}
	progress.publish(PhaseFinalizing, nil)
	return []ImportResult{result}, nil
}

func (c *Coordinator) importPrepared(ctx context.Context, source Source, adapter Adapter, prepared PreparedSource, progress *progressTracker) (result ImportResult, err error) {
	result.SourceID = source.ID
	progress.publish(PhaseVerifying, nil)
	state, found, err := c.store.SourceState(ctx, source.ID)
	if err != nil {
		return result, fmt.Errorf("import source %q: load durable state: %w", source.ID, err)
	}
	change := SourceNew
	if found {
		change, err = prepared.Verify(ctx, cloneSourceState(state))
		if err != nil {
			return result, fmt.Errorf("import source %q with adapter %q: verify durable state: %w", source.ID, adapter.Name(), err)
		}
		if !change.valid() || change == SourceNew {
			return result, fmt.Errorf("import source %q with adapter %q: invalid verification classification %q", source.ID, adapter.Name(), change)
		}
	}
	result.Change = change
	if change.requiresReconciliation() {
		progress.publish(PhaseReconciling, nil)
		if err := c.reconcile(ctx, source, prepared, state, &result, progress); err != nil {
			return result, fmt.Errorf("import source %q with adapter %q: reconcile %s: %w", source.ID, adapter.Name(), change, err)
		}
	} else {
		progress.publish(PhaseImporting, nil)
		var resume *ImportCheckpoint
		if found {
			checkpoint := cloneCheckpoint(state.Checkpoint)
			resume = &checkpoint
		}
		sink := &batchSink{
			store: c.store, options: c.options, source: source,
			state: state, hasState: found, result: &result, progress: progress, workPhase: PhaseImporting,
		}
		if err := prepared.Import(ctx, resume, sink); err != nil {
			return result, fmt.Errorf("import source %q with adapter %q: %w", source.ID, adapter.Name(), err)
		}
		if !sink.completed {
			return result, fmt.Errorf("import source %q with adapter %q: adapter returned without completion", source.ID, adapter.Name())
		}
		result.CanonicalChanged = result.BatchesCommitted > 0
	}
	if c.projector != nil {
		progress.publish(PhaseProjecting, nil)
		result.ProjectionError = c.projector.Project(ctx, ProjectionRequest{
			SourceID: source.ID, SessionID: result.SessionID, Checkpoint: cloneCheckpoint(result.Checkpoint),
		})
		if result.ProjectionError != nil {
			progress.publish(PhaseProjecting, []model.Diagnostic{{
				Code: "import.projection.failed", Severity: model.SeverityWarning,
				Message: "One or more projections remain incomplete; inspect projection status and retry.",
			}})
		}
	}
	return result, nil
}

func (c *Coordinator) reconcile(ctx context.Context, source Source, prepared PreparedSource, state SourceState, result *ImportResult, progress *progressTracker) (err error) {
	reconciliation, err := c.store.BeginReconciliation(ctx, source.ID, cloneCheckpoint(state.Checkpoint))
	if err != nil {
		return fmt.Errorf("begin staged replacement: %w", err)
	}
	finalized := false
	defer func() {
		if finalized {
			return
		}
		if abortErr := reconciliation.Abort(context.WithoutCancel(ctx)); abortErr != nil {
			err = errors.Join(err, fmt.Errorf("abort staged replacement: %w", abortErr))
		}
	}()

	stagedResult := ImportResult{SourceID: source.ID, Change: result.Change}
	sink := &batchSink{
		store: stagedCommitter{reconciliation}, options: c.options, source: source,
		result: &stagedResult, progress: progress, workPhase: PhaseReconciling,
		deferCommitProgress: true,
	}
	if err := prepared.Reconcile(ctx, sink); err != nil {
		return err
	}
	if !sink.completed {
		return fmt.Errorf("adapter returned without reconciliation completion")
	}
	progress.publish(PhaseCommitting, nil)
	if err := reconciliation.Finalize(ctx); err != nil {
		return fmt.Errorf("promote staged replacement: %w", err)
	}
	progress.committed(stagedResult.RecordsCommitted, stagedResult.BatchesCommitted, PhaseReconciling)
	finalized = true
	result.SessionID = stagedResult.SessionID
	result.Checkpoint = cloneCheckpoint(stagedResult.Checkpoint)
	result.RecordsCommitted = stagedResult.RecordsCommitted
	result.BatchesCommitted = stagedResult.BatchesCommitted
	result.CanonicalChanged = true
	result.Reconciled = true
	return nil
}

func (c *Coordinator) selectAdapter(ctx context.Context, source Source) (Adapter, ProbeResult, error) {
	var selected Adapter
	var selectedProbe ProbeResult
	ambiguous := false
	for _, adapter := range c.adapters {
		probe, err := adapter.Probe(ctx, source)
		if err != nil {
			return nil, ProbeResult{}, fmt.Errorf("probe source %q with adapter %q: %w", source.ID, adapter.Name(), err)
		}
		if err := probe.Validate(); err != nil {
			return nil, ProbeResult{}, fmt.Errorf("probe source %q with adapter %q returned invalid result: %w", source.ID, adapter.Name(), err)
		}
		if probe.Confidence == ProbeUnsupported || probe.Confidence < selectedProbe.Confidence {
			continue
		}
		if selected != nil && probe.Confidence == selectedProbe.Confidence {
			ambiguous = true
			continue
		}
		selected, selectedProbe = adapter, probe
		ambiguous = false
	}
	if selected == nil {
		return nil, ProbeResult{}, fmt.Errorf("%w: %q", ErrUnsupportedSource, source.ID)
	}
	if ambiguous {
		return nil, ProbeResult{}, fmt.Errorf("%w: source %q has multiple matches at confidence %d", ErrAmbiguousSource, source.ID, selectedProbe.Confidence)
	}
	return selected, selectedProbe, nil
}
