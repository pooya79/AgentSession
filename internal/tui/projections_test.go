package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/pooya79/AgentSession/internal/app"
)

func TestProjectionPanelControlsPollingConfirmationAndSafeDiagnostics(t *testing.T) {
	hostile := "unsafe\x1b]8;;https://attacker.invalid\x07link\x1b]8;;\x07"
	status := app.ProjectionStatus{
		State: app.EvidenceComplete, SessionID: "session-1", Active: true,
		Summary: app.ProjectionSummary{Pending: 1},
		Projections: []app.ProjectionState{{
			Kind: "search", Status: app.ProjectionStatusPending, TargetVersion: "1", TargetRevision: 2,
			BuildAvailable: true,
			Diagnostic:     &app.ProjectionDiagnostic{Code: hostile, Summary: hostile},
		}},
	}
	services := &servicesStub{projections: status, projectionAction: app.ProjectionAction{
		State: app.EvidenceComplete, Active: true, Status: status,
	}}
	m := New(context.Background(), services)
	m.screen, m.sessionsState.selected = timelineScreen, "session-1"

	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.screen != projectionsScreen || cmd == nil {
		t.Fatalf("open projections = screen %d cmd %v", m.screen, cmd != nil)
	}
	m, poll := updateModel(t, m, cmd().(projectionStatusMsg))
	if poll == nil || !strings.Contains(m.View().Content, "Canonical evidence remains available") {
		t.Fatalf("projection status did not render or poll: %q", m.View().Content)
	}
	if strings.Contains(m.View().Content, "attacker.invalid") {
		t.Fatalf("projection diagnostic was not terminal-safe: %q", m.View().Content)
	}

	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'b', Text: "b"})
	if cmd == nil {
		t.Fatal("rebuild selected returned no command")
	}
	_ = cmd()
	if len(services.rebuildCalls) != 1 || services.rebuildCalls[0] != "search" {
		t.Fatalf("selected rebuild calls = %v", services.rebuildCalls)
	}

	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 't', Text: "t"})
	if cmd == nil {
		t.Fatal("retry returned no command")
	}
	_ = cmd()
	if services.retryCalls != 1 {
		t.Fatalf("retry calls = %d", services.retryCalls)
	}

	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if !m.projectionsState.confirmAll || !strings.Contains(m.View().Content, "Confirm [y]") {
		t.Fatalf("rebuild-all confirmation = %v, view %q", m.projectionsState.confirmAll, m.View().Content)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.projectionsState.confirmAll || len(services.rebuildCalls) != 1 {
		t.Fatal("declining rebuild-all started work")
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	_ = cmd()
	if got := services.rebuildCalls[len(services.rebuildCalls)-1]; got != app.ProjectionKindAll {
		t.Fatalf("confirmed rebuild kind = %q", got)
	}

	generation := m.projectionsState.generation
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.screen != timelineScreen || m.projectionsState.cancel != nil {
		t.Fatal("leaving projection panel did not stop observation")
	}
	updated, staleCmd := updateModel(t, m, pollProjectionsMsg{generation: generation})
	if staleCmd != nil || updated.screen != timelineScreen {
		t.Fatal("stale projection poll revived observation")
	}
}

func TestProjectionActionNotFoundUpdatesPanelWithoutPolling(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	m.screen = projectionsScreen
	m.projectionsState.generation = 3
	m.projectionsState.status = app.ProjectionStatus{
		State:  app.EvidenceComplete,
		Active: true,
		Projections: []app.ProjectionState{{
			Kind: "search", Status: app.ProjectionStatusRunning,
		}},
	}

	updated, cmd := updateModel(t, m, projectionActionMsg{
		generation: 3,
		action:     app.ProjectionAction{State: app.EvidenceNotFound},
	})
	if cmd != nil {
		t.Fatal("not-found projection action scheduled polling")
	}
	if updated.projectionsState.status.State != app.EvidenceNotFound || updated.projectionsState.status.Active {
		t.Fatalf("projection status = %#v", updated.projectionsState.status)
	}
	if got := updated.View().Content; !strings.Contains(got, "no longer available") {
		t.Fatalf("not-found projection view = %q", got)
	}
}

func TestUnimplementedProjectionActionsAreHonestAndDisabled(t *testing.T) {
	services := &servicesStub{}
	m := New(context.Background(), services)
	m.screen = projectionsScreen
	m.sessionsState.selected = "session-1"
	m.projectionsState.status = app.ProjectionStatus{
		State: app.EvidenceComplete,
		Summary: app.ProjectionSummary{
			Pending: 1, Unimplemented: 1,
		},
		Projections: []app.ProjectionState{{
			Kind: "search", Status: app.ProjectionStatusPending, BuildAvailable: false,
		}},
	}
	if got := m.View().Content; !strings.Contains(got, "not implemented in this build") {
		t.Fatalf("unimplemented projection view = %q", got)
	}
	for _, key := range []tea.KeyPressMsg{{Code: 'b', Text: "b"}, {Code: 't', Text: "t"}, {Code: 'a', Text: "a"}} {
		var cmd tea.Cmd
		m, cmd = updateModel(t, m, key)
		if cmd != nil {
			t.Fatalf("key %q admitted unavailable projection work", key.String())
		}
	}
	if len(services.rebuildCalls) != 0 || services.retryCalls != 0 || m.projectionsState.confirmAll {
		t.Fatalf("unimplemented actions changed work: rebuilds %v retries %d confirm %v",
			services.rebuildCalls, services.retryCalls, m.projectionsState.confirmAll)
	}
	if !strings.Contains(m.View().Content, "disabled") {
		t.Fatalf("disabled action feedback = %q", m.View().Content)
	}
}
