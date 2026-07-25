package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/projection"
)

type projectionControllerStub struct {
	mu      sync.Mutex
	states  []projection.State
	started chan string
	release chan struct{}
	calls   []string
}

func (s *projectionControllerStub) Status(ctx context.Context, _ model.SessionID) ([]projection.State, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]projection.State(nil), s.states...), nil
}

func (s *projectionControllerStub) Retry(ctx context.Context, _ model.SessionID) error {
	return s.call(ctx, "retry")
}

func (s *projectionControllerStub) Rebuild(ctx context.Context, _ model.SessionID, kind *projection.Kind) error {
	name := ProjectionKindAll
	if kind != nil {
		name = string(*kind)
	}
	return s.call(ctx, "rebuild:"+name)
}

func (s *projectionControllerStub) call(ctx context.Context, name string) error {
	s.mu.Lock()
	s.calls = append(s.calls, name)
	s.mu.Unlock()
	if s.started != nil {
		s.started <- name
	}
	if s.release != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.release:
		}
	}
	return nil
}

func TestProjectionStatusDerivesUsableStaleAndSafeDiagnostics(t *testing.T) {
	readyRevision := int64(7)
	staleRevision := int64(6)
	now := time.Now().UTC()
	controller := &projectionControllerStub{states: []projection.State{
		{Kind: projection.KindSearch, Status: projection.StatusReady, TargetVersion: "1", TargetRevision: 7, ReadyVersion: "1", ReadyRevision: &readyRevision, UpdatedAt: now},
		{Kind: projection.KindFindings, Status: projection.StatusPending, TargetVersion: "2", TargetRevision: 7, ReadyVersion: "1", ReadyRevision: &staleRevision, UpdatedAt: now},
		{Kind: projection.KindOutcomes, Status: projection.StatusFailed, TargetVersion: "1", TargetRevision: 7, UpdatedAt: now, Diagnostic: &projection.Diagnostic{
			Code: strings.Repeat("c", 80), Summary: strings.Repeat("s", 300), Attempt: 3,
		}},
	}}
	service := NewProjectionService(controller)
	defer service.Shutdown(context.Background())

	status, err := service.ProjectionStatus(context.Background(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.Summary != (ProjectionSummary{Ready: 1, Usable: 1, Pending: 1, Failed: 1, Stale: 1}) {
		t.Fatalf("summary = %#v", status.Summary)
	}
	if !status.Projections[0].Usable || status.Projections[0].Stale || status.Projections[1].Usable || !status.Projections[1].Stale {
		t.Fatalf("derived projection states = %#v", status.Projections)
	}
	diagnostic := status.Projections[2].Diagnostic
	if diagnostic == nil || len([]rune(diagnostic.Code)) != 64 || len([]rune(diagnostic.Summary)) != 256 {
		t.Fatalf("bounded diagnostic = %#v", diagnostic)
	}
}

func TestProjectionActionsValidateAndDoNotCreateMissingWork(t *testing.T) {
	service := NewProjectionService(&projectionControllerStub{})
	defer service.Shutdown(context.Background())

	if _, err := service.RetryProjections(context.Background(), " bad"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("malformed session error = %v", err)
	}
	if _, err := service.RebuildProjections(context.Background(), "session", "invalid"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid kind error = %v", err)
	}
	action, err := service.RetryProjections(context.Background(), "missing")
	if err != nil || action.State != EvidenceNotFound || action.Active {
		t.Fatalf("missing action = (%#v, %v)", action, err)
	}
}

func TestProjectionCoordinatorCoalescesAndRebuildAllSubsumesQueuedKinds(t *testing.T) {
	controller := &projectionControllerStub{
		states:  []projection.State{{Kind: projection.KindSearch, Status: projection.StatusPending}},
		started: make(chan string, 4), release: make(chan struct{}, 4),
	}
	service := NewProjectionService(controller)

	first, err := service.RetryProjections(context.Background(), "session")
	if err != nil || first.Joined {
		t.Fatalf("first action = (%#v, %v)", first, err)
	}
	if got := <-controller.started; got != "retry" {
		t.Fatalf("first operation = %q", got)
	}
	duplicate, err := service.RetryProjections(context.Background(), "session")
	if err != nil || !duplicate.Joined {
		t.Fatalf("duplicate action = (%#v, %v)", duplicate, err)
	}
	if _, err := service.RebuildProjections(context.Background(), "session", string(projection.KindSearch)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RebuildProjections(context.Background(), "session", ProjectionKindAll); err != nil {
		t.Fatal(err)
	}
	controller.release <- struct{}{}
	if got := <-controller.started; got != "rebuild:all" {
		t.Fatalf("queued operation = %q, want rebuild:all", got)
	}
	controller.release <- struct{}{}
	if err := service.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if len(controller.calls) != 2 {
		t.Fatalf("calls = %v, want coalesced retry and rebuild-all", controller.calls)
	}
}

func TestProjectionActionOutlivesInitiatingContextAndShutdownCancelsWorker(t *testing.T) {
	controller := &projectionControllerStub{
		states:  []projection.State{{Kind: projection.KindSearch, Status: projection.StatusPending}},
		started: make(chan string, 1), release: make(chan struct{}),
	}
	service := NewProjectionService(controller)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	if _, err := service.RetryProjections(requestCtx, "session"); err != nil {
		t.Fatal(err)
	}
	<-controller.started
	cancelRequest()

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := service.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown did not cancel and await worker: %v", err)
	}
}
