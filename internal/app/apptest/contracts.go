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
	SessionFindings(context.Context, model.SessionID) (app.FindingsPage, error)
	// LibraryOverview exposes exact committed library counts.
	LibraryOverview(context.Context) (app.LibraryOverview, error)
	// ListSessions exposes bounded canonical session pages.
	ListSessions(context.Context, app.ListSessionsRequest) (app.SessionPage, error)
	// Timeline exposes bounded ordered canonical events.
	Timeline(context.Context, app.TimelineRequest) (app.TimelinePage, error)
	// EventDetail exposes explicit normalized payload reads.
	EventDetail(context.Context, app.EventDetailRequest) (app.EventDetail, error)
	// EventLocations resolves bounded evidence references.
	EventLocations(context.Context, []model.EventID) (map[model.EventID]app.EventLocation, error)
	// InspectUnknownEvidence exposes the same explicit retained-evidence action.
	InspectUnknownEvidence(context.Context, model.SessionID, model.EventID) (app.UnknownEvidenceInspection, error)
	// ProjectionStatus reports secondary derived-data readiness separately.
	ProjectionStatus(context.Context, model.SessionID) (app.ProjectionStatus, error)
	// RetryProjections schedules retryable derived-data work.
	RetryProjections(context.Context, model.SessionID) (app.ProjectionAction, error)
	// RebuildProjections schedules a validated projection rebuild.
	RebuildProjections(context.Context, model.SessionID, string) (app.ProjectionAction, error)
	// Search exposes bounded lifecycle-aware canonical evidence search.
	Search(context.Context, app.SearchRequest) (app.SearchPage, error)
}

// Fixture is a runtime populated through discovery and the composed import path.
type Fixture struct {
	Services app.Services
}

// NewFixture creates an isolated runtime backed by sanitized test session data.
func NewFixture(t *testing.T, name string) Fixture {
	t.Helper()
	root := t.TempDir()
	_, filename, _, _ := runtime.Caller(0)
	sourceFixture := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "adapter", "codex", "testdata", name))
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
	overview, err := consumer.LibraryOverview(ctx)
	if err != nil || overview.Sessions < 1 || overview.Events < 1 || overview.Agents < 1 {
		t.Fatalf("LibraryOverview() = (%#v, %v), want indexed totals", overview, err)
	}
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
	findings, err := consumer.SessionFindings(ctx, sessionID)
	if err != nil || findings.State != app.EvidenceComplete || len(findings.Assessments) != 2 || findings.Assessments[0].State != model.FindingTriggered {
		t.Fatalf("SessionFindings() = %#v, %v", findings, err)
	}
	f := findings.Assessments[0]
	if f.Metadata["invocations"] != "4" || f.Metadata["failures"] != "2" || f.Metadata["recovered"] != "1" || f.Metadata["uncertainties"] != "1" {
		t.Fatalf("streamed repetition, recovery and pending evidence = %#v", f)
	}
	v := findings.Assessments[1]
	if v.RuleID != "unresolved_failed_verification" || v.State != model.FindingInsufficientEvidence || v.Metadata["failures"] != "2" || v.Metadata["recovered"] != "2" || v.Metadata["unresolved"] != "0" {
		t.Fatalf("verification recovery and pending rerun = %#v", v)
	}
	for _, f := range findings.Assessments {
		locations, err := consumer.EventLocations(ctx, f.EventIDs)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range f.EventIDs {
			if locations[id].SessionID != sessionID {
				t.Fatal("foreign evidence")
			}
			focused, err := consumer.Timeline(ctx, app.TimelineRequest{SessionID: sessionID, FocusedEvent: id, Limit: 1})
			if err != nil || focused.FocusedEvent != id {
				t.Fatalf("focused evidence = %#v, %v", focused, err)
			}
		}
	}
	if _, err := consumer.SessionFindings(ctx, "bad\nidentifier"); !errors.Is(err, app.ErrInvalidRequest) {
		t.Fatalf("invalid findings identifier = %v", err)
	}
	canceledFindings, cancelFindings := context.WithCancel(ctx)
	cancelFindings()
	if _, err := consumer.SessionFindings(canceledFindings, sessionID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled findings = %v", err)
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
	if _, err := consumer.InspectUnknownEvidence(ctx, sessionID, eventID); !errors.Is(err, app.ErrInvalidRequest) {
		t.Fatalf("InspectUnknownEvidence(typed) error = %v, want invalid request", err)
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
	searchPage, err := consumer.Search(ctx, app.SearchRequest{Query: "user", Limit: 1})
	if err != nil || searchPage.State == app.EvidenceUnavailable || searchPage.Availability.Usable < 1 {
		t.Fatalf("Search() = (%#v, %v), want current searchable projection", searchPage, err)
	}
	if _, err := consumer.Search(ctx, app.SearchRequest{Query: "kind:not-a-kind"}); !errors.Is(err, app.ErrInvalidRequest) {
		t.Fatalf("Search(invalid) error = %v, want invalid request", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := consumer.ListSessions(canceled, app.ListSessionsRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListSessions(canceled) error = %v, want canceled", err)
	}
	projections, err := consumer.ProjectionStatus(ctx, sessionID)
	if err != nil || projections.State != app.EvidenceComplete || len(projections.Projections) == 0 {
		t.Fatalf("ProjectionStatus() = (%#v, %v)", projections, err)
	}
	var detailed app.ProjectionSummary
	for _, state := range projections.Projections {
		switch state.Status {
		case app.ProjectionStatusPending:
			detailed.Pending++
		case app.ProjectionStatusRunning:
			detailed.Running++
		case app.ProjectionStatusFailed:
			detailed.Failed++
		case app.ProjectionStatusReady:
			detailed.Ready++
		}
		if state.Usable {
			detailed.Usable++
		}
		if state.Stale {
			detailed.Stale++
		}
		if !state.BuildAvailable {
			detailed.Unimplemented++
		}
	}
	if projections.Summary != detailed {
		t.Fatalf("projection aggregate = %#v, detailed = %#v", projections.Summary, detailed)
	}
	action, err := consumer.RetryProjections(ctx, sessionID)
	if err != nil || action.State != app.EvidenceComplete || !action.Active {
		t.Fatalf("RetryProjections() = (%#v, %v)", action, err)
	}
	if _, err := consumer.RebuildProjections(ctx, sessionID, "invalid"); !errors.Is(err, app.ErrInvalidRequest) {
		t.Fatalf("RebuildProjections(invalid) error = %v", err)
	}
	missingProjection, err := consumer.ProjectionStatus(ctx, "missing-session")
	if err != nil || missingProjection.State != app.EvidenceNotFound {
		t.Fatalf("ProjectionStatus(missing) = (%#v, %v)", missingProjection, err)
	}
}

// RunVerificationContract exercises explicit imported command evidence through
// the same assessment and navigation services consumed by both interfaces.
func RunVerificationContract(t *testing.T, consumer Consumer) {
	t.Helper()
	ctx := context.Background()
	page, err := consumer.SessionFindings(ctx, "go-verification")
	if err != nil || page.State != app.EvidenceComplete || len(page.Assessments) != 2 {
		t.Fatalf("verification findings = %#v, %v", page, err)
	}
	f := page.Assessments[0]
	if f.RuleID != "unresolved_failed_verification" || f.State != model.FindingTriggered || f.Metadata["invocations"] != "8" || f.Metadata["tests"] != "4" || f.Metadata["static_checks"] != "3" || f.Metadata["builds"] != "1" || f.Metadata["failures"] != "3" || f.Metadata["recovered"] != "2" || f.Metadata["unresolved"] != "1" || f.Metadata["uncertainties"] != "0" || f.Metadata["settled_unknown_results"] != "1" {
		t.Fatalf("verification recovery/reopening = %#v", f)
	}
	// The unknown vet result can still change historical repetition, even
	// though later success settles the separate unresolved-failure predicate.
	if page.Assessments[1].RuleID != "repeated_command_failures" || page.Assessments[1].State != model.FindingInsufficientEvidence {
		t.Fatalf("triggered ordering = %#v", page)
	}
	for _, assessment := range page.Assessments {
		if assessment.RuleVersion != "1" || assessment.Explanation == "" || len(assessment.EventIDs) > 12 {
			t.Fatalf("invalid assessment = %#v", assessment)
		}
		for _, id := range assessment.EventIDs {
			detail, err := consumer.EventDetail(ctx, app.EventDetailRequest{SessionID: "go-verification", EventID: id, IncludePayload: true})
			if err != nil || detail.State != app.EvidenceComplete || detail.Event.SessionID != "go-verification" {
				t.Fatalf("verification event = %#v, %v", detail, err)
			}
			focused, err := consumer.Timeline(ctx, app.TimelineRequest{SessionID: "go-verification", FocusedEvent: id, Limit: 1})
			if err != nil || focused.FocusedEvent != id {
				t.Fatalf("focused verification = %#v, %v", focused, err)
			}
		}
	}
}
