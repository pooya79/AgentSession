// Package apptest provides reusable contracts for presentation integrations.
package apptest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/discovery"
	"github.com/pooya79/AgentSession/internal/model"
)

// Consumer is the exploration portion every presentation integration exposes.
type Consumer interface {
	ListSessions(context.Context, app.ListSessionsRequest) (app.SessionPage, error)
	Timeline(context.Context, app.TimelineRequest) (app.TimelinePage, error)
	EventDetail(context.Context, app.EventDetailRequest) (app.EventDetail, error)
}

// Fixture is a runtime populated through discovery and the composed import path.
type Fixture struct {
	Services app.Services
}

func NewFixture(t *testing.T) Fixture {
	t.Helper()
	root := t.TempDir()
	_, filename, _, _ := runtime.Caller(0)
	sourceFixture := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "adapter", "codex", "testdata", "ordinal.jsonl"))
	contents, err := os.ReadFile(sourceFixture)
	if err != nil {
		t.Fatalf("read contract fixture: %v", err)
	}
	sourcePath := filepath.Join(root, "sources", "contract.jsonl")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o700); err != nil {
		t.Fatalf("create contract source directory: %v", err)
	}
	if err := os.WriteFile(sourcePath, contents, 0o600); err != nil {
		t.Fatalf("write contract fixture: %v", err)
	}
	inputs := discovery.Inputs{
		FileSystem: discovery.OSFileSystem{}, HomeDir: root, WorkingDir: root, GOOS: "linux",
		ExplicitPaths: []discovery.ConfiguredPath{{Kind: discovery.SourceCodex, Path: sourcePath}},
	}
	pathInputs := app.PathInputs{GOOS: "linux", HomeDir: root, WorkingDir: root}
	runtimeService, err := app.OpenRuntime(context.Background(), app.RuntimeConfig{
		DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "config"),
		PathInputs: &pathInputs, DiscoveryInputs: &inputs,
	})
	if err != nil {
		t.Fatalf("open contract runtime: %v", err)
	}
	t.Cleanup(func() {
		if err := runtimeService.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown contract runtime: %v", err)
		}
	})
	if _, err := runtimeService.DiscoverAndImport(context.Background()); err != nil {
		t.Fatalf("populate contract runtime: %v", err)
	}
	return Fixture{Services: runtimeService}
}

// RunConsumerContract verifies the behavior TUI and web must preserve.
func RunConsumerContract(t *testing.T, consumer Consumer) {
	t.Helper()
	ctx := context.Background()
	sessions, err := consumer.ListSessions(ctx, app.ListSessionsRequest{Limit: 1})
	if err != nil || sessions.State == app.EvidenceNotFound || len(sessions.Sessions) != 1 {
		t.Fatalf("ListSessions() = (%#v, %v), want one imported session", sessions, err)
	}
	sessionID := sessions.Sessions[0].ID
	timeline, err := consumer.Timeline(ctx, app.TimelineRequest{SessionID: sessionID, Limit: 1})
	if err != nil || len(timeline.Events) != 1 {
		t.Fatalf("Timeline() = (%#v, %v), want first event", timeline, err)
	}
	if timeline.NextCursor != "" {
		next, err := consumer.Timeline(ctx, app.TimelineRequest{SessionID: sessionID, Cursor: timeline.NextCursor, Limit: 1})
		if err != nil || len(next.Events) != 1 || next.Events[0].Sequence <= timeline.Events[0].Sequence {
			t.Fatalf("Timeline(next) = (%#v, %v), want later source sequence", next, err)
		}
	}
	eventID := timeline.Events[0].ID
	withoutPayload, err := consumer.EventDetail(ctx, app.EventDetailRequest{SessionID: sessionID, EventID: eventID})
	if err != nil || withoutPayload.State == app.EvidenceNotFound || withoutPayload.Payload != nil {
		t.Fatalf("EventDetail(no payload) = (%#v, %v)", withoutPayload, err)
	}
	withPayload, err := consumer.EventDetail(ctx, app.EventDetailRequest{SessionID: sessionID, EventID: eventID, IncludePayload: true})
	if err != nil || withPayload.Payload == nil || withPayload.Event.ID != withoutPayload.Event.ID {
		t.Fatalf("EventDetail(payload) = (%#v, %v)", withPayload, err)
	}
	mismatch, err := consumer.EventDetail(ctx, app.EventDetailRequest{SessionID: "other-session", EventID: eventID})
	if err != nil || mismatch.State != app.EvidenceNotFound {
		t.Fatalf("EventDetail(session mismatch) = (%#v, %v), want not-found", mismatch, err)
	}
	missing, err := consumer.Timeline(ctx, app.TimelineRequest{SessionID: model.SessionID("missing-session")})
	if err != nil || missing.State != app.EvidenceNotFound {
		t.Fatalf("Timeline(missing) = (%#v, %v), want not-found", missing, err)
	}
	if _, err := consumer.EventDetail(ctx, app.EventDetailRequest{SessionID: sessionID, EventID: "bad"}); !errors.Is(err, app.ErrInvalidRequest) {
		t.Fatalf("EventDetail(invalid) error = %v, want invalid request", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := consumer.ListSessions(canceled, app.ListSessionsRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListSessions(canceled) error = %v, want canceled", err)
	}
}
