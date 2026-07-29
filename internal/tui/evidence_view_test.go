package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestIndexingStatesDiagnosticsAndHostileContentAreSafe(t *testing.T) {
	hostile := "bad\x1b]8;;https://attacker.invalid\x07link\x1b]8;;\x07\u202e"
	m := New(context.Background(), &servicesStub{})
	m.width, m.height = 60, 20
	m.screen = indexingScreen
	m.indexingState.status = app.ImportAllStatus{
		Phase: app.ImportAllIssues, SourcesDiscovered: 1, SourcesCompleted: 1, SourcesFailed: 1,
		DiagnosticsTotal: 2, DiagnosticsOmitted: 1,
		Sources: []app.ImportAllSourceStatus{{
			ID: "source", Kind: "codex", Path: hostile, Origin: "default",
			Phase: app.ImportFailed, Failure: hostile, Complete: true,
		}},
		RecentDiagnostics: []app.ImportAllDiagnostic{{
			Code: hostile, Severity: model.SeverityWarning, Message: hostile, SourcePath: hostile,
		}},
	}
	content := m.View().Content
	if strings.Contains(content, "attacker.invalid") || strings.Contains(content, "\u202e") || !strings.Contains(content, "<U+202E>") {
		t.Fatalf("unsafe indexing view = %q", content)
	}
	if strings.Count(content, "\n") >= m.height {
		t.Fatalf("view has too many rows: %d for height %d", strings.Count(content, "\n")+1, m.height)
	}
}

func TestRequestFailuresAndUnavailableEvidence(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	m.sessionsState.loading = false
	m.sessionsState.err = errors.New("database unavailable")
	if got := m.View().Content; !strings.Contains(got, "Could not load sessions") {
		t.Fatalf("failure view = %q", got)
	}

	m.screen = timelineScreen
	m.timelineState.err = nil
	m.timelineState.page = app.TimelinePage{
		State:       app.EvidenceUnavailable,
		Diagnostics: app.DiagnosticSynopsis{Total: 3, Omitted: 2},
	}
	if got := m.View().Content; !strings.Contains(got, "Timeline evidence is unavailable") {
		t.Fatalf("unavailable timeline view = %q", got)
	}
}

func TestUnknownEvidenceInspectionStatesAreRenderedExplicitly(t *testing.T) {
	event := testEvent("event-1", 1)
	event.Kind = model.EventKindUnknown
	m := New(context.Background(), &servicesStub{})
	m.sessionsState.selected = "session-1"
	m.detailState.detail = app.EventDetail{
		State:   app.EvidenceComplete,
		Event:   event,
		Payload: model.UnknownData{Reason: model.UnknownUnsupportedRecordKind, OriginalKind: "future"},
	}

	tests := map[app.EvidenceState]struct {
		eventID model.EventID
		want    string
	}{
		app.EvidenceUnavailable: {eventID: event.ID, want: "Retained evidence for this event is no longer available."},
		app.EvidenceNotFound:    {want: "This event's retained evidence could not be located."},
	}
	for state, test := range tests {
		m.detailState.inspection = app.UnknownEvidenceInspection{
			State: state, EventID: test.eventID,
		}
		if got := strings.Join(m.detailContentLines(), "\n"); !strings.Contains(got, test.want) {
			t.Errorf("state %q detail = %q, want %q", state, got, test.want)
		}
	}
}
