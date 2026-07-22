package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

func managedSource(id model.SourceID) importer.Source {
	return importer.Source{ID: id, Open: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(&emptyReader{}), nil
	}}
}

type emptyReader struct{}

func (*emptyReader) Read([]byte) (int, error) { return 0, io.EOF }

func terminalProgress(t *testing.T, subscription *ImportSubscription) ImportProgress {
	t.Helper()
	var last ImportProgress
	for progress := range subscription.Updates() {
		last = progress
	}
	return last
}

func TestImportManagerCoalescesRequestsAndDetachesObservers(t *testing.T) {
	started := make(chan struct{})
	reported := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	var importCanceled atomic.Bool
	manager, err := NewImportManager(func(ctx context.Context, source importer.Source, observe importer.ProgressObserver) ([]importer.ImportResult, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		observe(importer.Progress{
			SourceID: source.ID, ActiveSourceID: source.ID, Phase: importer.PhaseImporting, RecordsProcessed: 1,
			DiagnosticsObserved: 1, Diagnostics: []model.Diagnostic{{Code: "shared", Severity: model.SeverityWarning, Message: "original"}},
		})
		if calls.Load() == 1 {
			close(reported)
		}
		select {
		case <-ctx.Done():
			importCanceled.Store(true)
			return nil, ctx.Err()
		case <-release:
			return []importer.ImportResult{{SourceID: source.ID}}, nil
		}
	}, ImportManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}

	first, coalesced, err := manager.Request(managedSource("source-1"))
	if err != nil || coalesced {
		t.Fatalf("first Request() = (%v, %v, %v)", first, coalesced, err)
	}
	<-started
	<-reported
	second, coalesced, err := manager.Request(managedSource("source-1"))
	if err != nil || !coalesced {
		t.Fatalf("duplicate Request() = (%v, %v, %v)", second, coalesced, err)
	}
	firstSnapshot := <-first.Updates()
	secondSnapshot := <-second.Updates()
	firstSnapshot.RecentDiagnostics[0].Message = "mutated by observer"
	if secondSnapshot.RecentDiagnostics[0].Message != "original" {
		t.Fatal("observers shared mutable diagnostic storage")
	}

	first.Close()
	close(release)
	terminal := terminalProgress(t, second)
	if calls.Load() != 1 || importCanceled.Load() {
		t.Fatalf("calls=%d canceled=%v, want one uncanceled import", calls.Load(), importCanceled.Load())
	}
	if !terminal.Complete || terminal.Phase != ImportCompleted || terminal.RecordsProcessed != 1 {
		t.Fatalf("terminal progress = %#v", terminal)
	}

	again, coalesced, err := manager.Request(managedSource("source-1"))
	if err != nil || coalesced {
		t.Fatalf("post-completion Request() coalesced=%v error=%v", coalesced, err)
	}
	again.Close()
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestImportManagerSlowObserverGetsLatestTerminalSnapshot(t *testing.T) {
	release := make(chan struct{})
	manager, _ := NewImportManager(func(_ context.Context, source importer.Source, observe importer.ProgressObserver) ([]importer.ImportResult, error) {
		<-release
		for i := int64(1); i <= 1000; i++ {
			observe(importer.Progress{SourceID: source.ID, ActiveSourceID: source.ID, Phase: importer.PhaseImporting, RecordsProcessed: i})
		}
		return nil, nil
	}, ImportManagerOptions{})
	subscription, _, _ := manager.Request(managedSource("slow"))
	close(release)
	terminal := terminalProgress(t, subscription)
	if !terminal.Complete || terminal.RecordsProcessed != 1000 {
		t.Fatalf("slow observer terminal = %#v", terminal)
	}
}

func TestImportManagerRunsDifferentSourcesConcurrently(t *testing.T) {
	started := make(chan model.SourceID, 2)
	release := make(chan struct{})
	manager, _ := NewImportManager(func(_ context.Context, source importer.Source, _ importer.ProgressObserver) ([]importer.ImportResult, error) {
		started <- source.ID
		<-release
		return nil, nil
	}, ImportManagerOptions{})
	first, firstCoalesced, err := manager.Request(managedSource("source-a"))
	if err != nil || firstCoalesced {
		t.Fatalf("first source request coalesced=%v error=%v", firstCoalesced, err)
	}
	second, secondCoalesced, err := manager.Request(managedSource("source-b"))
	if err != nil || secondCoalesced {
		t.Fatalf("second source request coalesced=%v error=%v", secondCoalesced, err)
	}
	seen := map[model.SourceID]bool{<-started: true, <-started: true}
	if !seen["source-a"] || !seen["source-b"] {
		t.Fatalf("concurrently started sources = %#v", seen)
	}
	close(release)
	if !terminalProgress(t, first).Complete || !terminalProgress(t, second).Complete {
		t.Fatal("different-source imports did not complete")
	}
}

func TestImportManagerBoundsRecentDiagnosticsAndPreservesFailure(t *testing.T) {
	wantErr := errors.New("import failed")
	manager, _ := NewImportManager(func(_ context.Context, source importer.Source, observe importer.ProgressObserver) ([]importer.ImportResult, error) {
		for i := int64(1); i <= 5; i++ {
			observe(importer.Progress{
				SourceID: source.ID, ActiveSourceID: source.ID, Phase: importer.PhaseImporting,
				DiagnosticsObserved: i, Diagnostics: []model.Diagnostic{{Code: fmt.Sprintf("diagnostic.%d", i), Severity: model.SeverityWarning, Message: "warning"}},
			})
		}
		return nil, fmt.Errorf("run source: %w", wantErr)
	}, ImportManagerOptions{RecentDiagnostics: 3})
	subscription, _, _ := manager.Request(managedSource("diagnostics"))
	terminal := terminalProgress(t, subscription)
	if terminal.Complete || terminal.Phase != ImportFailed || !errors.Is(terminal.Failure, wantErr) {
		t.Fatalf("failure terminal = %#v", terminal)
	}
	if terminal.DiagnosticsObserved != 5 || terminal.DiagnosticsOmitted != 2 || len(terminal.RecentDiagnostics) != 3 {
		t.Fatalf("diagnostic progress = %#v", terminal)
	}
	if terminal.RecentDiagnostics[0].Code != "diagnostic.3" || terminal.RecentDiagnostics[2].Code != "diagnostic.5" {
		t.Fatalf("recent diagnostics = %#v", terminal.RecentDiagnostics)
	}
}

func TestImportManagerBoundsSafeResultSummaries(t *testing.T) {
	manager, err := NewImportManager(func(_ context.Context, source importer.Source, _ importer.ProgressObserver) ([]importer.ImportResult, error) {
		return []importer.ImportResult{
			{SourceID: "logical-1", SessionID: "session-1", Change: importer.SourceUnchanged, Checkpoint: importer.ImportCheckpoint{Cursor: []byte("secret")}},
			{SourceID: "logical-2", SessionID: "session-2", Change: importer.SourceUnchanged},
		}, nil
	}, ImportManagerOptions{ImportResults: 1})
	if err != nil {
		t.Fatal(err)
	}
	subscription, _, err := manager.Request(managedSource("container"))
	if err != nil {
		t.Fatal(err)
	}
	terminal := terminalProgress(t, subscription)
	if terminal.ImportResultsOmitted != 1 || len(terminal.ImportedSessions) != 1 || terminal.ImportedSessions[0].SessionID != "session-2" {
		t.Fatalf("result summaries = %#v", terminal)
	}
	if terminal.ImportResultsObserved != 2 || terminal.UnchangedResultsObserved != 2 {
		t.Fatalf("aggregate result counts = %#v", terminal)
	}
}

func TestImportManagerShutdownRejectsWorkAndWaitsForSettlement(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	settle := make(chan struct{})
	manager, _ := NewImportManager(func(ctx context.Context, _ importer.Source, _ importer.ProgressObserver) ([]importer.ImportResult, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-settle
		return nil, ctx.Err()
	}, ImportManagerOptions{})
	subscription, _, _ := manager.Request(managedSource("shutdown"))
	defer subscription.Close()
	<-started

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	<-canceled
	if _, _, err := manager.Request(managedSource("new")); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Request() during shutdown error = %v", err)
	}
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown() returned before transaction settled: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(settle)
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	terminal := terminalProgress(t, subscription)
	if !errors.Is(terminal.Failure, context.Canceled) {
		t.Fatalf("shutdown terminal = %#v", terminal)
	}
}

func TestImportManagerConcurrentDuplicateRequests(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	manager, _ := NewImportManager(func(context.Context, importer.Source, importer.ProgressObserver) ([]importer.ImportResult, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil, nil
	}, ImportManagerOptions{})

	const observers = 64
	subscriptions := make(chan *ImportSubscription, observers)
	var wg sync.WaitGroup
	wg.Add(observers)
	for range observers {
		go func() {
			defer wg.Done()
			subscription, _, err := manager.Request(managedSource("shared"))
			if err != nil {
				t.Errorf("Request() error = %v", err)
				return
			}
			subscriptions <- subscription
		}()
	}
	<-started
	wg.Wait()
	close(release)
	close(subscriptions)
	for subscription := range subscriptions {
		terminal := terminalProgress(t, subscription)
		if !terminal.Complete {
			t.Errorf("terminal = %#v", terminal)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("import calls = %d, want 1", calls.Load())
	}
}
