package projection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

type Registration struct {
	Definition Definition
	Builder    Builder
}

// ResultError reports only sanitized projection diagnostics.
type ResultError struct {
	Diagnostics []Diagnostic
}

func (e *ResultError) Error() string {
	return fmt.Sprintf("%d projection operation(s) did not complete; inspect projection status", len(e.Diagnostics))
}

type Manager struct {
	store    Store
	reader   Reader
	builders map[Kind]Builder

	mu      sync.Mutex
	flights map[model.SessionID]*flight
}

type flight struct {
	done chan struct{}
	err  error
}

const projectionLeaseDuration = time.Minute

var _ importer.Projector = (*Manager)(nil)

func NewManager(ctx context.Context, store Store, reader Reader, registrations []Registration) (*Manager, error) {
	if store == nil || reader == nil {
		return nil, errors.New("projection manager: store and authoritative reader are required")
	}
	if registrations == nil {
		for _, definition := range DefaultDefinitions() {
			registrations = append(registrations, Registration{Definition: definition})
		}
	}
	definitions := make([]Definition, 0, len(registrations))
	builders := make(map[Kind]Builder, len(registrations))
	seen := make(map[Kind]struct{}, len(registrations))
	for _, registration := range registrations {
		if err := registration.Definition.Validate(); err != nil {
			return nil, fmt.Errorf("projection manager: %w", err)
		}
		if _, exists := seen[registration.Definition.Kind]; exists {
			return nil, fmt.Errorf("projection manager: duplicate registration %q", registration.Definition.Kind)
		}
		seen[registration.Definition.Kind] = struct{}{}
		definitions = append(definitions, registration.Definition)
		if registration.Builder != nil {
			builders[registration.Definition.Kind] = registration.Builder
		}
	}
	for _, kind := range Kinds() {
		if _, exists := seen[kind]; !exists {
			return nil, fmt.Errorf("projection manager: fixed projection %q is not registered", kind)
		}
	}
	if err := store.Register(ctx, definitions); err != nil {
		return nil, fmt.Errorf("projection manager: register definitions: %w", err)
	}
	return &Manager{store: store, reader: reader, builders: builders, flights: make(map[model.SessionID]*flight)}, nil
}

func (m *Manager) Project(ctx context.Context, request importer.ProjectionRequest) error {
	if strings.TrimSpace(string(request.SessionID)) == "" {
		return errors.New("projection request has no session ID")
	}
	err, _ := m.run(ctx, request.SessionID)
	return err
}

func (m *Manager) Retry(ctx context.Context, sessionID model.SessionID) error {
	err, _ := m.run(ctx, sessionID)
	return err
}

func (m *Manager) Rebuild(ctx context.Context, sessionID model.SessionID, kind *Kind) error {
	if err := m.store.Invalidate(ctx, sessionID, kind); err != nil {
		return &ResultError{Diagnostics: []Diagnostic{{
			Kind: diagnosticKind(kind), Code: "projection.storage_unavailable",
			Summary: "Projection rebuild could not be requested.", At: time.Now().UTC(),
		}}}
	}
	err, joined := m.run(ctx, sessionID)
	if joined && ctx.Err() == nil {
		// Invalidation may have made the joined run stale. Ensure the newly
		// pending target receives its own pass after that run settles.
		err, _ = m.run(ctx, sessionID)
	}
	return err
}

func (m *Manager) Status(ctx context.Context, sessionID model.SessionID) ([]State, error) {
	return m.store.States(ctx, sessionID)
}

func (m *Manager) run(ctx context.Context, sessionID model.SessionID) (error, bool) {
	m.mu.Lock()
	if existing := m.flights[sessionID]; existing != nil {
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err(), true
		case <-existing.done:
			return existing.err, true
		}
	}
	work := &flight{done: make(chan struct{})}
	m.flights[sessionID] = work
	m.mu.Unlock()

	work.err = m.execute(ctx, sessionID)
	m.mu.Lock()
	delete(m.flights, sessionID)
	close(work.done)
	m.mu.Unlock()
	return work.err, false
}

func (m *Manager) execute(ctx context.Context, sessionID model.SessionID) error {
	var deferred error
	for {
		retry, err := m.executePass(ctx, sessionID)
		if retry {
			deferred = errors.Join(deferred, err)
			continue
		}
		return errors.Join(deferred, err)
	}
}

func (m *Manager) executePass(ctx context.Context, sessionID model.SessionID) (bool, error) {
	var diagnostics []Diagnostic
	retry := false
	for _, kind := range Kinds() {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		builder := m.builders[kind]
		if builder == nil {
			continue
		}
		claim, claimed, err := m.store.Claim(ctx, sessionID, kind)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{
				Kind: kind, Code: "projection.storage_unavailable",
				Summary: "Projection work could not be claimed.", At: time.Now().UTC(),
			})
			continue
		}
		if !claimed {
			continue
		}
		buildErr := m.buildWithLease(ctx, claim, builder, BuildRequest{
			SessionID: sessionID, CanonicalRevision: claim.Revision, Reader: m.reader,
		})
		if buildErr == nil {
			if completed, err := m.store.Complete(ctx, claim); err != nil {
				_ = m.store.Release(context.WithoutCancel(ctx), claim)
				diagnostics = append(diagnostics, Diagnostic{
					Kind: kind, TargetVersion: claim.Version, TargetRevision: claim.Revision,
					Code: "projection.storage_unavailable", Summary: "Projection completion could not be recorded.",
					Attempt: claim.Attempt, At: time.Now().UTC(),
				})
			} else if !completed {
				retry = true
			}
			continue
		}
		if ctx.Err() != nil || errors.Is(buildErr, context.Canceled) || errors.Is(buildErr, context.DeadlineExceeded) {
			_ = m.store.Release(context.WithoutCancel(ctx), claim)
			if err := ctx.Err(); err != nil {
				return false, err
			}
			return false, buildErr
		}
		diagnostic := safeBuildDiagnostic(claim, buildErr)
		if recorded, err := m.store.Fail(ctx, claim, diagnostic); err != nil {
			_ = m.store.Release(context.WithoutCancel(ctx), claim)
			diagnostic.Code = "projection.storage_unavailable"
			diagnostic.Summary = "Projection failure could not be recorded."
		} else if !recorded {
			// The canonical revision changed or this claim was superseded. The
			// durable state is already pending, so this obsolete error is omitted.
			retry = true
			continue
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	if len(diagnostics) > 0 {
		return retry, &ResultError{Diagnostics: diagnostics}
	}
	return retry, nil
}

func (m *Manager) buildWithLease(ctx context.Context, claim Claim, builder Builder, request BuildRequest) error {
	buildCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	leaseErr := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(projectionLeaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-buildCtx.Done():
				return
			case <-ticker.C:
				renewed, err := m.store.Renew(buildCtx, claim)
				if err != nil || !renewed {
					if err == nil {
						err = errors.New("projection claim lease was lost")
					}
					leaseErr <- err
					cancel()
					return
				}
			}
		}
	}()
	buildErr := builder.Build(buildCtx, request)
	cancel()
	<-done
	select {
	case err := <-leaseErr:
		return err
	default:
		return buildErr
	}
}

func safeBuildDiagnostic(claim Claim, buildErr error) Diagnostic {
	diagnostic := Diagnostic{
		Kind: claim.Kind, TargetVersion: claim.Version, TargetRevision: claim.Revision,
		Code: "projection.build_failed", Summary: "Projection build failed. Retry or inspect projection status.",
		Attempt: claim.Attempt, At: time.Now().UTC(),
	}
	var safe SafeError
	matched := errors.As(buildErr, &safe)
	if !matched {
		var safePointer *SafeError
		if errors.As(buildErr, &safePointer) && safePointer != nil {
			safe = *safePointer
			matched = true
		}
	}
	if matched {
		if code := strings.TrimSpace(safe.Code); code != "" && len(code) <= 64 {
			diagnostic.Code = code
		}
		if summary := strings.TrimSpace(safe.Summary); summary != "" {
			if len(summary) > 256 {
				summary = summary[:256]
			}
			diagnostic.Summary = summary
		}
	}
	return diagnostic
}

func diagnosticKind(kind *Kind) Kind {
	if kind != nil {
		return *kind
	}
	return KindSearch
}
