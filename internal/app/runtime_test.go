package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/discovery"
	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/projection"
	_ "modernc.org/sqlite"
)

func TestRuntimeImportsAllAdaptersIdempotently(t *testing.T) {
	root := t.TempDir()
	sources := filepath.Join(root, "sources")
	codexPath := copyFixture(t, filepath.Join("..", "adapter", "codex", "testdata", "ordinal.jsonl"), filepath.Join(sources, "rollout-test.jsonl"))
	claudePath := copyFixture(t, filepath.Join("..", "adapter", "claude", "testdata", "main.jsonl"), filepath.Join(sources, "claude.jsonl"))
	openCodePath := filepath.Join(sources, "opencode.db")
	createOpenCodeFixture(t, filepath.Join("..", "adapter", "opencode", "testdata", "valid_multi_session.sql"), openCodePath)
	fixturePaths := []string{codexPath, claudePath, openCodePath}
	fixtureHashes := make(map[string][sha256.Size]byte, len(fixturePaths))
	for _, path := range fixturePaths {
		fixtureHashes[path] = hashFile(t, path)
	}

	explicit := []discovery.ConfiguredPath{{Kind: discovery.SourceCodex, Path: codexPath}, {Kind: discovery.SourceClaude, Path: claudePath}, {Kind: discovery.SourceOpenCode, Path: openCodePath}}
	inputs := discovery.Inputs{FileSystem: discovery.OSFileSystem{}, HomeDir: root, WorkingDir: root, GOOS: "linux", ExplicitPaths: explicit}
	pathInputs := PathInputs{GOOS: "linux", HomeDir: root, WorkingDir: root}
	runtime, err := OpenRuntime(context.Background(), RuntimeConfig{DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "config"), PathInputs: &pathInputs, DiscoveryInputs: &inputs})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "config")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config directory was created: %v", err)
	}
	discoveredView, err := runtime.DiscoverSources(context.Background())
	if err != nil || discoveredView.State != EvidenceComplete || len(discoveredView.Sources) != 3 {
		t.Fatalf("DiscoverSources() = (%#v, %v), want three app-owned summaries", discoveredView, err)
	}
	missingStart, err := runtime.StartImport(context.Background(), "missing-source")
	if err != nil || missingStart.State != EvidenceNotFound {
		t.Fatalf("StartImport(missing) = (%#v, %v), want not-found", missingStart, err)
	}
	started, err := runtime.StartImport(context.Background(), discoveredView.Sources[0].ID)
	if err != nil || started.State != EvidenceComplete || started.Subscription == nil {
		t.Fatalf("StartImport() = (%#v, %v)", started, err)
	}
	if terminal := terminalProgress(t, started.Subscription); !terminal.Complete || terminal.Failure != nil {
		t.Fatalf("StartImport terminal progress = %#v", terminal)
	}
	started.Subscription.Close()

	first, err := runtime.DiscoverAndImport(context.Background())
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if len(first.Discovery.Sources) != 3 {
		t.Fatalf("discovered %d sources, want 3", len(first.Discovery.Sources))
	}
	var sessionIDs []model.SessionID
	for _, progress := range first.Imports {
		for _, summary := range progress.ImportedSessions {
			sessionIDs = append(sessionIDs, summary.SessionID)
			if _, found, err := runtime.Reader().Session(context.Background(), summary.SessionID); err != nil || !found {
				t.Fatalf("read session %q: found=%v err=%v", summary.SessionID, found, err)
			}
			events, err := runtime.Reader().EventSummaries(context.Background(), summary.SessionID)
			if err != nil || len(events) == 0 {
				t.Fatalf("events for %q = %d, err=%v", summary.SessionID, len(events), err)
			}
			states, err := runtime.ProjectionService().Status(context.Background(), summary.SessionID)
			if err != nil || len(states) != len(projection.Kinds()) {
				t.Fatalf("projection states for %q = %d, err=%v", summary.SessionID, len(states), err)
			}
			for _, state := range states {
				if state.Status != projection.StatusPending {
					t.Fatalf("projection %q status = %q", state.Kind, state.Status)
				}
			}
		}
	}
	if len(sessionIDs) < 4 {
		t.Fatalf("imported %d logical sessions, want Codex, Claude, and multiple OpenCode", len(sessionIDs))
	}
	var listed []model.SessionID
	cursor := ""
	for {
		page, err := runtime.ListSessions(context.Background(), ListSessionsRequest{Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatalf("list imported sessions: %v", err)
		}
		if len(page.Sessions) > 2 {
			t.Fatalf("session page is unbounded: %d items", len(page.Sessions))
		}
		for _, session := range page.Sessions {
			listed = append(listed, session.ID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(listed) != len(sessionIDs) {
		t.Fatalf("paginated session count = %d, want %d", len(listed), len(sessionIDs))
	}
	repeatedPage, err := runtime.ListSessions(context.Background(), ListSessionsRequest{Limit: MaximumPageSize})
	if err != nil {
		t.Fatalf("repeat session listing: %v", err)
	}
	repeatedIDs := make([]model.SessionID, 0, len(repeatedPage.Sessions))
	for _, session := range repeatedPage.Sessions {
		repeatedIDs = append(repeatedIDs, session.ID)
	}
	if !slices.Equal(listed, repeatedIDs) {
		t.Fatalf("session listing is not deterministic: paged=%v repeated=%v", listed, repeatedIDs)
	}
	timeline, err := runtime.Timeline(context.Background(), TimelineRequest{SessionID: sessionIDs[0], Limit: 1})
	if err != nil || len(timeline.Events) != 1 {
		t.Fatalf("first timeline page = (%#v, %v)", timeline, err)
	}
	detail, err := runtime.EventDetail(context.Background(), EventDetailRequest{SessionID: sessionIDs[0], EventID: timeline.Events[0].ID})
	if err != nil || detail.Payload != nil {
		t.Fatalf("payload-excluded detail = (%#v, %v)", detail, err)
	}
	detail, err = runtime.EventDetail(context.Background(), EventDetailRequest{SessionID: sessionIDs[0], EventID: timeline.Events[0].ID, IncludePayload: true})
	if err != nil || detail.Payload == nil {
		t.Fatalf("payload-included detail = (%#v, %v)", detail, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runtime.ListSessions(canceled, ListSessionsRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled session list error = %v", err)
	}
	firstCounts := readDatabaseCounts(t, runtime.Paths().DatabasePath)
	if firstCounts.sessions != len(sessionIDs) || firstCounts.rawRecords == 0 || firstCounts.events == 0 || firstCounts.checkpoints != len(sessionIDs) || firstCounts.revisions != int64(len(sessionIDs)) || firstCounts.projectionStates != len(sessionIDs)*len(projection.Kinds()) {
		t.Fatalf("first database counts = %#v", firstCounts)
	}

	second, err := runtime.DiscoverAndImport(context.Background())
	if err != nil {
		t.Fatalf("unchanged import: %v", err)
	}
	for _, progress := range second.Imports {
		for _, summary := range progress.ImportedSessions {
			if summary.Change != "unchanged" || summary.RecordsCommitted != 0 || summary.BatchesCommitted != 0 {
				t.Fatalf("unchanged summary = %#v", summary)
			}
		}
	}
	secondCounts := readDatabaseCounts(t, runtime.Paths().DatabasePath)
	if secondCounts != firstCounts {
		t.Fatalf("unchanged import advanced database counts: first=%#v second=%#v", firstCounts, secondCounts)
	}
	for _, path := range fixturePaths {
		if got := hashFile(t, path); got != fixtureHashes[path] {
			t.Fatalf("source fixture %q changed during import", path)
		}
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
	if _, _, err := runtime.Reader().Session(context.Background(), sessionIDs[0]); err == nil {
		t.Fatal("reader succeeded after database closure")
	}
	if page, err := runtime.Timeline(context.Background(), TimelineRequest{SessionID: sessionIDs[0]}); err == nil || page.State == EvidenceNotFound {
		t.Fatalf("timeline after database closure = (%#v, %v), want storage failure distinct from not-found", page, err)
	}
	if _, err := runtime.Discover(context.Background()); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Discover after shutdown = %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(runtime.Paths().DatabasePath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sessions, projectionStates int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM session_projection_states`).Scan(&projectionStates); err != nil {
		t.Fatal(err)
	}
	if sessions != len(sessionIDs) || projectionStates != sessions*len(projection.Kinds()) {
		t.Fatalf("sessions=%d projection states=%d", sessions, projectionStates)
	}
}

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

func copyFixture(t *testing.T, source, destination string) string {
	t.Helper()
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return destination
}

func createOpenCodeFixture(t *testing.T, fixture, destination string) {
	t.Helper()
	contents, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(contents)); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

type databaseCounts struct {
	sessions         int
	rawRecords       int
	events           int
	checkpoints      int
	revisions        int64
	projectionStates int
}

func readDatabaseCounts(t *testing.T, path string) databaseCounts {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var counts databaseCounts
	queries := []struct {
		query       string
		destination any
	}{
		{`SELECT COUNT(*) FROM sessions`, &counts.sessions},
		{`SELECT COUNT(*) FROM raw_records`, &counts.rawRecords},
		{`SELECT COUNT(*) FROM events`, &counts.events},
		{`SELECT COUNT(*) FROM import_checkpoints`, &counts.checkpoints},
		{`SELECT COALESCE(SUM(canonical_revision), 0) FROM sessions`, &counts.revisions},
		{`SELECT COUNT(*) FROM session_projection_states`, &counts.projectionStates},
	}
	for _, item := range queries {
		if err := db.QueryRow(item.query).Scan(item.destination); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

func hashFile(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(contents)
}
