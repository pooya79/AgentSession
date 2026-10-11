package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/discovery"
	"github.com/pooya79/AgentSession/internal/importer"
)

func TestRuntimeTimedOutShutdownRemainsRetryable(t *testing.T) {
	root := t.TempDir()
	inputs := discovery.Inputs{FileSystem: discovery.OSFileSystem{}, HomeDir: root, WorkingDir: root, GOOS: "linux"}
	pathInputs := PathInputs{GOOS: "linux", HomeDir: root, WorkingDir: root}
	runtime, err := OpenRuntime(context.Background(), RuntimeConfig{DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "config"), PathInputs: &pathInputs, DiscoveryInputs: &inputs})
	if err != nil {
		t.Fatal(err)
	}
	settle := make(chan struct{})
	manager, err := NewImportManager(func(ctx context.Context, _ importer.Source, _ importer.ProgressObserver) ([]importer.ImportResult, error) {
		<-ctx.Done()
		<-settle
		return nil, ctx.Err()
	}, ImportManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	runtime.imports = manager
	subscription, _, err := manager.Request(managedSource("active"))
	if err != nil {
		t.Fatal(err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := runtime.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed shutdown error = %v", err)
	}
	if err := runtime.db.PingContext(context.Background()); err != nil {
		t.Fatalf("database closed before import settlement: %v", err)
	}
	if _, err := runtime.Discover(context.Background()); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Discover during shutdown = %v", err)
	}
	if start, err := runtime.StartImport(context.Background(), "unknown-during-shutdown"); !errors.Is(err, ErrShuttingDown) || start.State == EvidenceNotFound {
		t.Fatalf("StartImport(unknown) during shutdown = (%#v, %v), want shutdown error", start, err)
	}
	close(settle)
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if terminal := terminalProgress(t, subscription); !errors.Is(terminal.Failure, context.Canceled) {
		t.Fatalf("terminal progress = %#v", terminal)
	}
}
