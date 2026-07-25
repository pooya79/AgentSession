package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/projection"
)

const (
	// ProjectionKindAll is the application-level selector for rebuilding every
	// registered projection kind. It is not a durable projection kind.
	ProjectionKindAll = "all"

	// ProjectionStatusPending is the presentation-safe pending status.
	ProjectionStatusPending = "pending"
	// ProjectionStatusRunning is the presentation-safe running status.
	ProjectionStatusRunning = "running"
	// ProjectionStatusFailed is the presentation-safe failed status.
	ProjectionStatusFailed = "failed"
	// ProjectionStatusReady is the presentation-safe ready status.
	ProjectionStatusReady = "ready"
)

// ProjectionController is the application-owned boundary around the durable
// projection manager. Presentation layers never invalidate storage directly.
type ProjectionController interface {
	Status(context.Context, model.SessionID) ([]projection.State, error)
	Retry(context.Context, model.SessionID) error
	Rebuild(context.Context, model.SessionID, *projection.Kind) error
}

// ProjectionDiagnostic contains only the manager's bounded, explicitly safe
// diagnostic fields.
type ProjectionDiagnostic struct {
	Code    string
	Summary string
	Attempt int64
	At      time.Time
}

// ProjectionState is the presentation-safe lifecycle view of one durable
// projection state. Usable and Stale are derived rather than persisted states.
type ProjectionState struct {
	Kind           string
	Status         string
	TargetVersion  string
	TargetRevision int64
	ReadyVersion   string
	ReadyRevision  *int64
	AttemptCount   int64
	Usable         bool
	Stale          bool
	StartedAt      *time.Time
	UpdatedAt      time.Time
	Diagnostic     *ProjectionDiagnostic
}

// ProjectionSummary contains bounded aggregate counts for a session. Ready
// counts the durable status; Usable additionally requires the current target.
type ProjectionSummary struct {
	Ready   int64
	Usable  int64
	Pending int64
	Running int64
	Failed  int64
	Stale   int64
}

// ProjectionStatus combines durable per-kind state with transient
// application-owned operation state for one session.
type ProjectionStatus struct {
	State               EvidenceState
	SessionID           model.SessionID
	Projections         []ProjectionState
	Summary             ProjectionSummary
	Active              bool
	OperationDiagnostic *ProjectionDiagnostic
}

// ProjectionAction acknowledges an admitted retry or rebuild without waiting
// for projection work to finish.
type ProjectionAction struct {
	State  EvidenceState
	Joined bool
	Active bool
	Status ProjectionStatus
}

// Projections is the shared projection lifecycle contract used by both UIs.
type Projections interface {
	ProjectionStatus(context.Context, model.SessionID) (ProjectionStatus, error)
	RetryProjections(context.Context, model.SessionID) (ProjectionAction, error)
	RebuildProjections(context.Context, model.SessionID, string) (ProjectionAction, error)
}

type projectionRequest struct {
	retry      bool
	rebuildAll bool
	kinds      map[projection.Kind]struct{}
}

type projectionJob struct {
	running projectionRequest
	queued  projectionRequest
}

// ProjectionService transfers action lifetimes from request contexts to the
// runtime. It serializes work per session while leaving the manager's leases
// and cross-caller flight coordination authoritative.
type ProjectionService struct {
	controller ProjectionController
	ctx        context.Context
	cancel     context.CancelFunc

	mu          sync.Mutex
	jobs        map[model.SessionID]*projectionJob
	diagnostics map[model.SessionID]ProjectionDiagnostic
	closing     bool
	wg          sync.WaitGroup
}

// NewProjectionService creates a runtime-lifetime coordinator around the
// durable projection controller.
func NewProjectionService(controller ProjectionController) *ProjectionService {
	ctx, cancel := context.WithCancel(context.Background())
	return &ProjectionService{
		controller: controller, ctx: ctx, cancel: cancel,
		jobs:        make(map[model.SessionID]*projectionJob),
		diagnostics: make(map[model.SessionID]ProjectionDiagnostic),
	}
}

// Status remains the internal raw-state seam used by projection-focused tests
// and import composition. Presentations use ProjectionStatus.
func (s *ProjectionService) Status(ctx context.Context, sessionID model.SessionID) ([]projection.State, error) {
	return s.controller.Status(ctx, sessionID)
}

// ProjectionStatus validates the session identifier and returns safe lifecycle
// state without treating projection readiness as canonical evidence readiness.
func (s *ProjectionService) ProjectionStatus(ctx context.Context, sessionID model.SessionID) (ProjectionStatus, error) {
	if err := validateIdentifier("session", string(sessionID)); err != nil {
		return ProjectionStatus{}, err
	}
	states, err := s.controller.Status(ctx, sessionID)
	if err != nil {
		return ProjectionStatus{}, fmt.Errorf("read projection status for %q: %w", sessionID, err)
	}
	status := ProjectionStatus{State: EvidenceComplete, SessionID: sessionID}
	if len(states) == 0 {
		status.State = EvidenceNotFound
		return status, nil
	}
	status.Projections = make([]ProjectionState, 0, len(states))
	for _, state := range states {
		item := projectionStateDTO(state)
		status.Projections = append(status.Projections, item)
		switch state.Status {
		case projection.StatusReady:
			status.Summary.Ready++
		case projection.StatusPending:
			status.Summary.Pending++
		case projection.StatusRunning:
			status.Summary.Running++
		case projection.StatusFailed:
			status.Summary.Failed++
		}
		if item.Usable {
			status.Summary.Usable++
		}
		if item.Stale {
			status.Summary.Stale++
		}
	}
	s.mu.Lock()
	_, status.Active = s.jobs[sessionID]
	if diagnostic, ok := s.diagnostics[sessionID]; ok {
		copy := diagnostic
		status.OperationDiagnostic = &copy
	}
	s.mu.Unlock()
	return status, nil
}

// RetryProjections admits one pass over currently pending or failed kinds.
func (s *ProjectionService) RetryProjections(ctx context.Context, sessionID model.SessionID) (ProjectionAction, error) {
	return s.start(ctx, sessionID, projectionRequest{retry: true})
}

// RebuildProjections invalidates and schedules one registered kind or all
// registered kinds when kind is ProjectionKindAll.
func (s *ProjectionService) RebuildProjections(ctx context.Context, sessionID model.SessionID, kind string) (ProjectionAction, error) {
	request := projectionRequest{}
	if kind == ProjectionKindAll {
		request.rebuildAll = true
	} else {
		parsed := projection.Kind(kind)
		if !parsed.Valid() {
			return ProjectionAction{}, fmt.Errorf("%w: invalid projection kind", ErrInvalidRequest)
		}
		request.kinds = map[projection.Kind]struct{}{parsed: {}}
	}
	return s.start(ctx, sessionID, request)
}

func (s *ProjectionService) start(ctx context.Context, sessionID model.SessionID, request projectionRequest) (ProjectionAction, error) {
	// Admission uses the caller context for validation and existence checks.
	// Once admitted, run uses the service context owned by the runtime.
	status, err := s.ProjectionStatus(ctx, sessionID)
	if err != nil {
		return ProjectionAction{}, err
	}
	if status.State == EvidenceNotFound {
		return ProjectionAction{State: EvidenceNotFound, Status: status}, nil
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return ProjectionAction{}, ErrShuttingDown
	}
	delete(s.diagnostics, sessionID)
	job, joined := s.jobs[sessionID]
	if !joined {
		job = &projectionJob{running: request}
		s.jobs[sessionID] = job
		s.wg.Add(1)
		go s.run(sessionID, job)
	} else if !requestCovered(job.running, request) {
		mergeProjectionRequest(&job.queued, request)
	}
	s.mu.Unlock()

	status.Active = true
	return ProjectionAction{State: EvidenceComplete, Joined: joined, Active: true, Status: status}, nil
}

func (s *ProjectionService) run(sessionID model.SessionID, job *projectionJob) {
	defer s.wg.Done()
	for {
		s.mu.Lock()
		request := job.running
		s.mu.Unlock()

		err := s.execute(sessionID, request)

		s.mu.Lock()
		// Never retain controller error text: it may contain source-derived
		// payloads, paths, commands, or environment values.
		if err != nil && s.ctx.Err() == nil {
			s.diagnostics[sessionID] = ProjectionDiagnostic{
				Code: "projection.operation_failed", Summary: "Projection work did not complete. Retry or inspect per-kind status.",
				At: time.Now().UTC(),
			}
		} else if err == nil {
			delete(s.diagnostics, sessionID)
		}
		if emptyProjectionRequest(job.queued) || s.closing {
			delete(s.jobs, sessionID)
			s.mu.Unlock()
			return
		}
		job.running = job.queued
		job.queued = projectionRequest{}
		s.mu.Unlock()
	}
}

func (s *ProjectionService) execute(sessionID model.SessionID, request projectionRequest) error {
	if request.rebuildAll {
		return s.controller.Rebuild(s.ctx, sessionID, nil)
	}
	var result error
	for _, kind := range projection.Kinds() {
		if _, ok := request.kinds[kind]; ok {
			selected := kind
			result = errors.Join(result, s.controller.Rebuild(s.ctx, sessionID, &selected))
		}
	}
	if request.retry {
		result = errors.Join(result, s.controller.Retry(s.ctx, sessionID))
	}
	return result
}

// Shutdown rejects new work, cancels the runtime-owned operation context, and
// waits for all admitted projection workers before storage can be closed.
func (s *ProjectionService) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.closing {
		s.closing = true
		s.cancel()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func projectionStateDTO(state projection.State) ProjectionState {
	// Retained ready identity is useful for diagnosis after invalidation, but
	// identity mismatch can never satisfy Usable for the current target.
	stale := state.ReadyRevision != nil &&
		(state.ReadyVersion != state.TargetVersion || *state.ReadyRevision != state.TargetRevision)
	item := ProjectionState{
		Kind: string(state.Kind), Status: string(state.Status),
		TargetVersion: state.TargetVersion, TargetRevision: state.TargetRevision,
		ReadyVersion: state.ReadyVersion, ReadyRevision: state.ReadyRevision,
		AttemptCount: state.AttemptCount, Usable: state.Usable(), Stale: stale,
		StartedAt: state.StartedAt, UpdatedAt: state.UpdatedAt,
	}
	if state.Diagnostic != nil {
		item.Diagnostic = &ProjectionDiagnostic{
			Code:    boundedSafeText(state.Diagnostic.Code, 64, "projection.failed"),
			Summary: boundedSafeText(state.Diagnostic.Summary, 256, "Projection work failed."),
			Attempt: state.Diagnostic.Attempt, At: state.Diagnostic.At,
		}
	}
	return item
}

func boundedSafeText(value string, maximum int, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	runes := []rune(value)
	if len(runes) > maximum {
		value = string(runes[:maximum])
	}
	return value
}

func requestCovered(active, incoming projectionRequest) bool {
	if active.rebuildAll {
		return true
	}
	if incoming.retry && !active.retry {
		return false
	}
	if incoming.rebuildAll {
		return false
	}
	for kind := range incoming.kinds {
		if _, ok := active.kinds[kind]; !ok {
			return false
		}
	}
	return true
}

func mergeProjectionRequest(target *projectionRequest, incoming projectionRequest) {
	// Rebuild-all subsumes queued individual rebuilds and their retry pass
	// because the manager visits every pending kind after invalidation.
	if incoming.rebuildAll {
		target.rebuildAll = true
		target.retry = false
		target.kinds = nil
		return
	}
	if target.rebuildAll {
		return
	}
	target.retry = target.retry || incoming.retry
	if len(incoming.kinds) > 0 {
		if target.kinds == nil {
			target.kinds = make(map[projection.Kind]struct{})
		}
		for kind := range incoming.kinds {
			target.kinds[kind] = struct{}{}
		}
	}
}

func emptyProjectionRequest(request projectionRequest) bool {
	return !request.retry && !request.rebuildAll && len(request.kinds) == 0
}
