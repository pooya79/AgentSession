package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/projection"
)

func TestProjectionLifecycleTransitionsVersioningAndCascade(t *testing.T) {
	store := openImportStore(t)
	ctx := context.Background()
	batch := testImportBatch()
	if err := store.CommitBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	revision, found, err := store.CanonicalRevision(ctx, batch.Session.ID)
	if err != nil || !found || revision != 1 {
		t.Fatalf("CanonicalRevision() = (%d, %v, %v), want (1, true, nil)", revision, found, err)
	}
	states, err := store.States(ctx, batch.Session.ID)
	if err != nil || len(states) != 5 {
		t.Fatalf("States() = (%#v, %v), want five states", states, err)
	}
	for _, state := range states {
		if state.Status != projection.StatusPending || state.TargetRevision != 1 || state.TargetVersion != "1" {
			t.Fatalf("initial state = %#v", state)
		}
	}

	claim, claimed, err := store.Claim(ctx, batch.Session.ID, projection.KindSearch)
	if err != nil || !claimed || claim.Attempt != 1 {
		t.Fatalf("Claim() = (%#v, %v, %v)", claim, claimed, err)
	}
	if completed, err := store.Complete(ctx, claim); err != nil || !completed {
		t.Fatalf("Complete() = (%v, %v)", completed, err)
	}
	state := projectionState(t, store, batch.Session.ID, projection.KindSearch)
	if !state.Usable() {
		t.Fatalf("ready state = %#v", state)
	}

	if err := store.Register(ctx, []projection.Definition{{Kind: projection.KindSearch, Version: "2"}}); err != nil {
		t.Fatal(err)
	}
	state = projectionState(t, store, batch.Session.ID, projection.KindSearch)
	if state.Status != projection.StatusPending || state.TargetVersion != "2" || state.ReadyVersion != "1" {
		t.Fatalf("version-bumped state = %#v", state)
	}
	claim, _, _ = store.Claim(ctx, batch.Session.ID, projection.KindSearch)
	diagnostic := projection.Diagnostic{Code: "search.unavailable", Summary: "Search projection unavailable.", At: time.Now()}
	if recorded, err := store.Fail(ctx, claim, diagnostic); err != nil || !recorded {
		t.Fatalf("Fail() = (%v, %v)", recorded, err)
	}
	state = projectionState(t, store, batch.Session.ID, projection.KindSearch)
	if state.Status != projection.StatusFailed || state.Diagnostic == nil || state.Diagnostic.Attempt != 1 {
		t.Fatalf("failed state = %#v", state)
	}
	claim, _, _ = store.Claim(ctx, batch.Session.ID, projection.KindSearch)
	if completed, err := store.Complete(ctx, claim); err != nil || !completed {
		t.Fatalf("retry Complete() = (%v, %v)", completed, err)
	}
	if state = projectionState(t, store, batch.Session.ID, projection.KindSearch); !state.Usable() || state.ReadyVersion != "2" {
		t.Fatalf("retried state = %#v", state)
	}

	if err := store.Invalidate(ctx, batch.Session.ID, nil); err != nil {
		t.Fatal(err)
	}
	claim, _, _ = store.Claim(ctx, batch.Session.ID, projection.KindSearch)
	if _, err := store.db.ExecContext(ctx, `UPDATE session_projection_states SET lease_expires_at = ? WHERE session_id = ? AND kind = ?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), batch.Session.ID, projection.KindSearch); err != nil {
		t.Fatal(err)
	}
	recovered, recoveredClaimed, err := store.Claim(ctx, batch.Session.ID, projection.KindSearch)
	if err != nil || !recoveredClaimed {
		t.Fatalf("recover expired claim = (%#v, %v, %v)", recovered, recoveredClaimed, err)
	}
	if state = projectionState(t, store, batch.Session.ID, projection.KindSearch); state.Status != projection.StatusRunning || recovered.RunToken == claim.RunToken {
		t.Fatalf("recovered state = %#v", state)
	}
	if completed, err := store.Complete(ctx, claim); err != nil || completed {
		t.Fatalf("stale Complete() = (%v, %v), want false", completed, err)
	}

	deleted, err := store.DeleteSession(ctx, batch.Session.ID)
	if err != nil || !deleted {
		t.Fatalf("DeleteSession() = (%v, %v)", deleted, err)
	}
	if states, err := store.States(ctx, batch.Session.ID); err != nil || len(states) != 0 {
		t.Fatalf("states after delete = (%#v, %v)", states, err)
	}
}

func TestCanonicalCommitInvalidatesRunningProjectionAndRejectsStaleCompletion(t *testing.T) {
	store := openImportStore(t)
	ctx := context.Background()
	batch := testImportBatch()
	if err := store.CommitBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	claim, _, _ := store.Claim(ctx, batch.Session.ID, projection.KindSearch)

	next := batch
	next.RawRecords = nil
	next.Events = nil
	next.RecordDiagnostics = nil
	next.Checkpoint.RecordSequence = 1
	next.Checkpoint.Cursor = []byte("next")
	next.Checkpoint.Fingerprint = []byte("next-fingerprint")
	if err := store.CommitBatch(ctx, next); err != nil {
		t.Fatal(err)
	}
	state := projectionState(t, store, batch.Session.ID, projection.KindSearch)
	if state.Status != projection.StatusPending || state.TargetRevision != 2 {
		t.Fatalf("advanced state = %#v", state)
	}
	if completed, err := store.Complete(ctx, claim); err != nil || completed {
		t.Fatalf("old revision Complete() = (%v, %v)", completed, err)
	}
}

func TestProjectionManagerSanitizesFailuresAndCoalescesWork(t *testing.T) {
	store := openImportStore(t)
	ctx := context.Background()
	batch := testImportBatch()
	if err := store.CommitBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	var aggregateCalls atomic.Int64
	registrations := make([]projection.Registration, 0, 5)
	for _, definition := range projection.DefaultDefinitions() {
		registration := projection.Registration{Definition: definition}
		if definition.Kind == projection.KindSearch {
			registration.Builder = projection.BuilderFunc(func(ctx context.Context, request projection.BuildRequest) error {
				if request.Reader != store || request.CanonicalRevision != 1 {
					t.Errorf("builder request = %#v", request)
				}
				call := calls.Add(1)
				if call == 1 {
					close(started)
				}
				if _, found, err := request.Reader.Session(ctx, request.SessionID); err != nil || !found {
					t.Errorf("authoritative Session() = (_, %v, %v)", found, err)
				}
				if events, err := request.Reader.Events(ctx, request.SessionID); err != nil || len(events) != 1 {
					t.Errorf("authoritative Events() = (%d, %v)", len(events), err)
				}
				<-release
				if call == 1 {
					return errors.New("secret=raw-token path=/private/session.jsonl")
				}
				return nil
			})
		} else if definition.Kind == projection.KindAggregates {
			registration.Builder = projection.BuilderFunc(func(context.Context, projection.BuildRequest) error {
				aggregateCalls.Add(1)
				return nil
			})
		}
		registrations = append(registrations, registration)
	}
	manager, err := projection.NewManager(ctx, store, store, registrations)
	if err != nil {
		t.Fatal(err)
	}
	service := app.NewProjectionService(manager)
	errorsOut := make(chan error, 2)
	go func() { errorsOut <- manager.Retry(ctx, batch.Session.ID) }()
	<-started
	joining := make(chan struct{})
	go func() {
		close(joining)
		kind := projection.KindSearch
		errorsOut <- service.Rebuild(ctx, batch.Session.ID, &kind)
	}()
	<-joining
	time.Sleep(10 * time.Millisecond)
	close(release)
	for range 2 {
		err := <-errorsOut
		if err != nil && (strings.Contains(err.Error(), "raw-token") || strings.Contains(err.Error(), "/private")) {
			t.Fatalf("manager error = %v", err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("builder calls = %d, want 2", calls.Load())
	}
	if aggregateCalls.Load() != 1 || !projectionState(t, store, batch.Session.ID, projection.KindAggregates).Usable() {
		t.Fatal("a failed search projection prevented the later aggregate projection")
	}
	state := projectionState(t, store, batch.Session.ID, projection.KindSearch)
	if state.Diagnostic != nil {
		t.Fatalf("persisted diagnostic = %#v", state.Diagnostic)
	}
	var stored string
	if err := store.db.QueryRowContext(ctx, `SELECT COALESCE(failure_summary, '') FROM session_projection_states WHERE session_id = ? AND kind = 'search'`, batch.Session.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "raw-token") || strings.Contains(stored, "/private") {
		t.Fatalf("unsafe SQLite diagnostic = %q", stored)
	}
	if checkpoint, found, err := store.Checkpoint(ctx, batch.Checkpoint.SourceID); err != nil || !found || checkpoint.RecordSequence != batch.Checkpoint.RecordSequence {
		t.Fatalf("canonical checkpoint after projection failure = (%#v, %v, %v)", checkpoint, found, err)
	}
	kind := projection.KindSearch
	if err := service.Rebuild(ctx, batch.Session.ID, &kind); err != nil {
		t.Fatalf("forced Rebuild() error = %v", err)
	}
	if state := projectionState(t, store, batch.Session.ID, kind); !state.Usable() || calls.Load() != 3 {
		t.Fatalf("forced rebuild state = %#v, calls = %d", state, calls.Load())
	}
}

func TestProjectionManagerCancellationLeavesWorkPending(t *testing.T) {
	store := openImportStore(t)
	batch := testImportBatch()
	if err := store.CommitBatch(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	registrations := make([]projection.Registration, 0, 5)
	for _, definition := range projection.DefaultDefinitions() {
		registration := projection.Registration{Definition: definition}
		if definition.Kind == projection.KindSearch {
			registration.Builder = projection.BuilderFunc(func(ctx context.Context, _ projection.BuildRequest) error {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			})
		}
		registrations = append(registrations, registration)
	}
	manager, err := projection.NewManager(context.Background(), store, store, registrations)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Retry(ctx, batch.Session.ID) }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Retry() error = %v, want context.Canceled", err)
	}
	if state := projectionState(t, store, batch.Session.ID, projection.KindSearch); state.Status != projection.StatusPending {
		t.Fatalf("canceled projection state = %#v", state)
	}
}

func TestProjectionManagerRerunsAfterStaleCompletion(t *testing.T) {
	store := openImportStore(t)
	ctx := context.Background()
	batch := testImportBatch()
	if err := store.CommitBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var revisionsMu sync.Mutex
	var revisions []int64
	registrations := registrationsWithBuilder(projection.KindSearch, func(_ context.Context, request projection.BuildRequest) error {
		revisionsMu.Lock()
		revisions = append(revisions, request.CanonicalRevision)
		call := len(revisions)
		revisionsMu.Unlock()
		if call == 1 {
			close(started)
			<-release
		}
		return nil
	})
	manager, err := projection.NewManager(ctx, store, store, registrations)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() { results <- manager.Project(ctx, importer.ProjectionRequest{SessionID: batch.Session.ID}) }()
	<-started
	next := batch
	next.RawRecords = nil
	next.Events = nil
	next.RecordDiagnostics = nil
	next.Checkpoint.RecordSequence++
	next.Checkpoint.Cursor = []byte("next")
	next.Checkpoint.Fingerprint = []byte("next-fingerprint")
	if err := store.CommitBatch(ctx, next); err != nil {
		t.Fatal(err)
	}
	go func() { results <- manager.Project(ctx, importer.ProjectionRequest{SessionID: batch.Session.ID}) }()
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			var resultErr *projection.ResultError
			if errors.As(err, &resultErr) {
				t.Fatalf("projection error: %#v", resultErr.Diagnostics)
			}
			t.Fatal(err)
		}
	}
	revisionsMu.Lock()
	defer revisionsMu.Unlock()
	if !reflect.DeepEqual(revisions, []int64{1, 2}) {
		t.Fatalf("built revisions = %v, want [1 2]", revisions)
	}
	if state := projectionState(t, store, batch.Session.ID, projection.KindSearch); !state.Usable() || state.TargetRevision != 2 {
		t.Fatalf("current projection state = %#v", state)
	}
}

func TestProjectionManagerRebuildAfterKindAlreadyPassed(t *testing.T) {
	store := openImportStore(t)
	ctx := context.Background()
	batch := testImportBatch()
	if err := store.CommitBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	var searchCalls atomic.Int64
	aggregateStarted := make(chan struct{})
	releaseAggregate := make(chan struct{})
	registrations := make([]projection.Registration, 0, len(projection.Kinds()))
	for _, definition := range projection.DefaultDefinitions() {
		registration := projection.Registration{Definition: definition}
		switch definition.Kind {
		case projection.KindSearch:
			registration.Builder = projection.BuilderFunc(func(context.Context, projection.BuildRequest) error {
				searchCalls.Add(1)
				return nil
			})
		case projection.KindAggregates:
			registration.Builder = projection.BuilderFunc(func(context.Context, projection.BuildRequest) error {
				close(aggregateStarted)
				<-releaseAggregate
				return nil
			})
		}
		registrations = append(registrations, registration)
	}
	invalidated := make(chan struct{})
	managerStore := &invalidateNotifyingStore{Store: store, invalidated: invalidated}
	manager, err := projection.NewManager(ctx, managerStore, store, registrations)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() { results <- manager.Retry(ctx, batch.Session.ID) }()
	<-aggregateStarted
	kind := projection.KindSearch
	go func() { results <- manager.Rebuild(ctx, batch.Session.ID, &kind) }()
	<-invalidated
	close(releaseAggregate)
	for range 2 {
		if err := <-results; err != nil {
			var resultErr *projection.ResultError
			if errors.As(err, &resultErr) {
				t.Fatalf("projection error: %#v", resultErr.Diagnostics)
			}
			t.Fatal(err)
		}
	}
	if searchCalls.Load() != 2 {
		t.Fatalf("search builder calls = %d, want 2", searchCalls.Load())
	}
	if state := projectionState(t, store, batch.Session.ID, projection.KindSearch); !state.Usable() {
		t.Fatalf("rebuilt search state = %#v", state)
	}
}

func TestNewProjectionManagerPreservesAnotherManagersLiveClaim(t *testing.T) {
	store := openImportStore(t)
	ctx := context.Background()
	batch := testImportBatch()
	if err := store.CommitBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	registrations := registrationsWithBuilder(projection.KindSearch, func(context.Context, projection.BuildRequest) error {
		close(started)
		<-release
		return nil
	})
	first, err := projection.NewManager(ctx, store, store, registrations)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- first.Retry(ctx, batch.Session.ID) }()
	<-started
	var token string
	if err := store.db.QueryRowContext(ctx, `SELECT run_token FROM session_projection_states WHERE session_id = ? AND kind = ?`, batch.Session.ID, projection.KindSearch).Scan(&token); err != nil {
		t.Fatal(err)
	}
	if _, err := projection.NewManager(ctx, store, store, registrations); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := store.db.QueryRowContext(ctx, `SELECT run_token FROM session_projection_states WHERE session_id = ? AND kind = ?`, batch.Session.ID, projection.KindSearch).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != token {
		t.Fatalf("live claim token changed from %q to %q", token, after)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if state := projectionState(t, store, batch.Session.ID, projection.KindSearch); !state.Usable() {
		t.Fatalf("live manager completion was rejected: %#v", state)
	}
}

func registrationsWithBuilder(kind projection.Kind, build func(context.Context, projection.BuildRequest) error) []projection.Registration {
	registrations := make([]projection.Registration, 0, len(projection.Kinds()))
	for _, definition := range projection.DefaultDefinitions() {
		registration := projection.Registration{Definition: definition}
		if definition.Kind == kind {
			registration.Builder = projection.BuilderFunc(build)
		}
		registrations = append(registrations, registration)
	}
	return registrations
}

type invalidateNotifyingStore struct {
	projection.Store
	invalidated chan struct{}
}

func (s *invalidateNotifyingStore) Invalidate(ctx context.Context, sessionID model.SessionID, kind *projection.Kind) error {
	err := s.Store.Invalidate(ctx, sessionID, kind)
	if err == nil {
		close(s.invalidated)
	}
	return err
}

func projectionState(t *testing.T, store *ImportStore, sessionID model.SessionID, kind projection.Kind) projection.State {
	t.Helper()
	states, err := store.States(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range states {
		if state.Kind == kind {
			return state
		}
	}
	t.Fatalf("projection state %q not found", kind)
	return projection.State{}
}
