package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestFindingsNavigationAndTerminalSafety(t *testing.T) {
	id := model.EventID("evt_" + strings.Repeat("a", 64))
	services := &servicesStub{findings: app.FindingsPage{State: app.EvidenceComplete, Assessments: []model.Finding{{RuleID: "repeated_command_failures", RuleVersion: "1", State: model.FindingTriggered, Explanation: "recovery \x1b]52;c;unsafe\x07", EventIDs: []model.EventID{id}}}}}
	m := New(context.Background(), services)
	m.screen = timelineScreen
	m.sessionsState.selected = "session"
	next, cmd := m.handleKey("f")
	m = next.(Model)
	if cmd == nil || m.screen != findingsScreen {
		t.Fatal("assessment action unavailable")
	}
	// The command carries the shared-service result using the active generation.
	next, _ = m.Update(findingsLoadedMsg{generation: m.requestGeneration, page: services.findings})
	m = next.(Model)
	view := m.View().Content
	if strings.Contains(view, "\x1b]52") || !strings.Contains(view, "triggered") {
		t.Fatalf("unsafe/incomplete view: %q", view)
	}
	next, cmd = m.handleKey("enter")
	m = *next.(*Model)
	if cmd == nil || m.screen != timelineScreen {
		t.Fatal("evidence navigation unavailable")
	}
	cmd()
	if len(services.timelineCalls) != 1 || services.timelineCalls[0].SessionID != "session" || services.timelineCalls[0].FocusedEvent != id {
		t.Fatalf("wrong context: %#v", services.timelineCalls)
	}
}

func TestLeavingAssessmentReadDoesNotKeepSpinnerBusy(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	m.screen = searchScreen
	m.sessionsState.loading = false
	m.sessionsState.overviewLoading = false
	m.findingsState.loading = true
	next, _ := m.Update(findingsLoadedMsg{generation: m.requestGeneration, page: app.FindingsPage{State: app.EvidenceComplete}})
	m = next.(Model)
	if m.busy() {
		t.Fatal("obsolete assessment read kept presentation busy")
	}
	if len(m.findingsState.page.Assessments) != 0 {
		t.Fatal("obsolete response changed screen state")
	}
}
