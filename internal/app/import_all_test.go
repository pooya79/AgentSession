package app

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestImportAllCoalescesLimitsConcurrencyAndAggregatesSnapshots(t *testing.T) {
	release := make(chan struct{})
	var active atomic.Int64
	var maximum atomic.Int64
	manager, err := NewImportManager(func(_ context.Context, source importer.Source, observe importer.ProgressObserver) ([]importer.ImportResult, error) {
		now := active.Add(1)
		defer active.Add(-1)
		for {
			old := maximum.Load()
			if now <= old || maximum.CompareAndSwap(old, now) {
				break
			}
		}
		observe(importer.Progress{
			SourceID: source.ID, ActiveSourceID: source.ID, Phase: importer.PhaseImporting,
			RecordsProcessed: 2, EventsProcessed: 3, DiagnosticsObserved: 1,
			Diagnostics: []model.Diagnostic{{Code: "retained", Severity: model.SeverityWarning, Message: "partial"}},
		})
		<-release
		if source.ID == "source-2" {
			return nil, errors.New("independent failure")
		}
		return []importer.ImportResult{{SourceID: source.ID, SessionID: model.SessionID("session-" + source.ID), Change: importer.SourceUnchanged}}, nil
	}, ImportManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sources := []SourceSummary{{ID: "source-1"}, {ID: "source-2"}, {ID: "source-3"}}
	coordinator := newImportAllCoordinator(
		func(context.Context) (SourceDiscovery, error) {
			return SourceDiscovery{State: EvidencePartial, Sources: sources, Diagnostics: []DiscoveryDiagnostic{{Code: "discovery.warning", Severity: model.SeverityWarning}}}, nil
		},
		func(_ context.Context, id model.SourceID) (ImportStart, error) {
			subscription, joined, err := manager.Request(managedSource(id))
			return ImportStart{State: EvidenceComplete, Subscription: subscription, Joined: joined}, err
		},
	)
	first, err := coordinator.Start(context.Background())
	if err != nil || first.Joined {
		t.Fatalf("first start = (%#v, %v)", first, err)
	}
	joined, err := coordinator.Start(context.Background())
	if err != nil || !joined.Joined || joined.Status.RunID != first.Status.RunID {
		t.Fatalf("joined start = (%#v, %v)", joined, err)
	}
	waitForAllStatus(t, coordinator, func(status ImportAllStatus) bool {
		return active.Load() == 2 && status.SourcesDiscovered == 3
	})
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrent imports = %d, want 2", maximum.Load())
	}
	close(release)
	terminal := waitForAllStatus(t, coordinator, func(status ImportAllStatus) bool { return !status.Active })
	if terminal.Phase != ImportAllIssues || terminal.SourcesCompleted != 3 || terminal.SourcesFailed != 1 {
		t.Fatalf("terminal status = %#v", terminal)
	}
	if terminal.RecordsProcessed != 6 || terminal.EventsProcessed != 9 || terminal.SessionsObserved != 2 || terminal.UnchangedSessions != 2 {
		t.Fatalf("aggregate counts = %#v", terminal)
	}
	if terminal.DiagnosticsTotal != 4 || len(terminal.RecentDiagnostics) != 4 || terminal.DiagnosticsOmitted != 0 {
		t.Fatalf("diagnostics = %#v", terminal)
	}
	again, err := coordinator.Start(context.Background())
	if err != nil || again.Joined || again.Status.RunID == first.Status.RunID {
		t.Fatalf("retrigger = (%#v, %v)", again, err)
	}
	if err := coordinator.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestImportAllUnavailableAndBoundedDiagnostics(t *testing.T) {
	discoveryFailure := newImportAllCoordinator(
		func(context.Context) (SourceDiscovery, error) { return SourceDiscovery{}, errors.New("offline") },
		func(context.Context, model.SourceID) (ImportStart, error) { return ImportStart{}, nil },
	)
	if _, err := discoveryFailure.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := waitForAllStatus(t, discoveryFailure, func(status ImportAllStatus) bool { return !status.Active })
	if status.Phase != ImportAllUnavailable || status.Failure == "" {
		t.Fatalf("discovery failure = %#v", status)
	}

	diagnostics := make([]DiscoveryDiagnostic, 40)
	for i := range diagnostics {
		diagnostics[i] = DiscoveryDiagnostic{Code: fmt.Sprintf("diagnostic.%02d", i), Severity: model.SeverityWarning}
	}
	empty := newImportAllCoordinator(
		func(context.Context) (SourceDiscovery, error) {
			return SourceDiscovery{State: EvidenceUnavailable, Diagnostics: diagnostics}, nil
		},
		func(context.Context, model.SourceID) (ImportStart, error) { return ImportStart{}, nil },
	)
	if _, err := empty.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	status = waitForAllStatus(t, empty, func(status ImportAllStatus) bool { return !status.Active })
	if status.Phase != ImportAllIssues || status.DiagnosticsTotal != 40 || status.DiagnosticsOmitted != 8 || len(status.RecentDiagnostics) != 32 {
		t.Fatalf("bounded diagnostic status = %#v", status)
	}
	if status.RecentDiagnostics[0].Code != "diagnostic.08" {
		t.Fatalf("oldest retained diagnostic = %q", status.RecentDiagnostics[0].Code)
	}
}

func TestImportAllShutdownStopsScheduling(t *testing.T) {
	started := make(chan struct{})
	manager, _ := NewImportManager(func(ctx context.Context, _ importer.Source, _ importer.ProgressObserver) ([]importer.ImportResult, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}, ImportManagerOptions{})
	var requested atomic.Int64
	coordinator := newImportAllCoordinator(
		func(context.Context) (SourceDiscovery, error) {
			return SourceDiscovery{Sources: []SourceSummary{{ID: "one"}, {ID: "two"}, {ID: "three"}, {ID: "four"}}}, nil
		},
		func(_ context.Context, id model.SourceID) (ImportStart, error) {
			requested.Add(1)
			subscription, joined, err := manager.Request(managedSource(id))
			return ImportStart{Subscription: subscription, Joined: joined}, err
		},
	)
	if _, err := coordinator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := coordinator.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requested.Load() > importAllConcurrency {
		t.Fatalf("scheduled %d sources after shutdown, want at most %d", requested.Load(), importAllConcurrency)
	}
	if _, err := coordinator.Start(context.Background()); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("start after shutdown error = %v", err)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func waitForAllStatus(t *testing.T, coordinator *importAllCoordinator, ready func(ImportAllStatus) bool) ImportAllStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := coordinator.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if ready(status) {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	status, _ := coordinator.Status(context.Background())
	t.Fatalf("timed out waiting for import-all status: %#v", status)
	return ImportAllStatus{}
}
