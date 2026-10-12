package tui

import (
	"context"
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/app/apptest"
	"github.com/pooya79/AgentSession/internal/model"
)

// Observe the public service request made by the real terminal consumer.
type findingsNavigationServices struct {
	app.Services
	timelineCalls []app.TimelineRequest
}

func (s *findingsNavigationServices) Timeline(ctx context.Context, request app.TimelineRequest) (app.TimelinePage, error) {
	s.timelineCalls = append(s.timelineCalls, request)
	return s.Services.Timeline(ctx, request)
}

func TestImportedStreamedAssessmentAndEvidenceNavigation(t *testing.T) {
	fixture := apptest.NewFixture(t, "streamed_commands.jsonl")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessions, err := fixture.Services.ListSessions(ctx, app.ListSessionsRequest{})
	if err != nil || len(sessions.Sessions) != 1 {
		t.Fatalf("sessions = %#v, %v", sessions, err)
	}
	session := sessions.Sessions[0].ID
	findings, err := fixture.Services.SessionFindings(ctx, session)
	if err != nil || len(findings.Assessments) != 3 || len(findings.Assessments[0].EventIDs) == 0 {
		t.Fatalf("supporting evidence = %#v, %v", findings, err)
	}
	services := &findingsNavigationServices{Services: fixture.Services}
	var m tea.Model = New(ctx, services)
	var drain func(tea.Cmd)
	drain = func(cmd tea.Cmd) {
		t.Helper()
		if cmd == nil {
			return
		}
		message := cmd()
		if batch, ok := message.(tea.BatchMsg); ok {
			for _, child := range batch {
				drain(child)
			}
			return
		}
		// Animation timing is unrelated to service reads and navigation.
		if _, ok := message.(spinner.TickMsg); ok {
			return
		}
		var next tea.Cmd
		m, next = m.Update(message)
		drain(next)
	}
	send := func(message tea.Msg) {
		t.Helper()
		var cmd tea.Cmd
		m, cmd = m.Update(message)
		drain(cmd)
	}
	send(tea.WindowSizeMsg{Width: 240, Height: 120})
	drain(m.Init())
	send(tea.KeyPressMsg{Code: tea.KeyEnter})
	send(tea.KeyPressMsg{Code: 'f', Text: "f"})
	send(tea.KeyPressMsg{Code: 'v', Text: "v"})
	view := m.View().Content
	for _, want := range []string{"triggered", "Invocations 4", "failures 2", "recovered 1", "uncertainties 1", "Missing, conflicting, unsupported, or oversized evidence", "unresolved_failed_verification", "unresolved 0", "tests 4", "static checks 0", "builds 0", "missing_verification"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in imported assessment: %s", want, view)
		}
	}
	var references []model.EventID
	seenReferences := map[model.EventID]bool{}
	for _, assessment := range findings.Assessments {
		for _, id := range assessment.EventIDs {
			if !seenReferences[id] {
				references = append(references, id)
				seenReferences[id] = true
			}
		}
	}
	for i, id := range references {
		if i > 0 {
			send(tea.KeyPressMsg{Code: 'f', Text: "f"})
		}
		for range i {
			send(tea.KeyPressMsg{Code: ']', Text: "]"})
		}
		before := len(services.timelineCalls)
		send(tea.KeyPressMsg{Code: tea.KeyEnter})
		if len(services.timelineCalls) != before+1 {
			t.Fatal("evidence navigation did not request timeline context")
		}
		request := services.timelineCalls[before]
		if request.SessionID != session || request.FocusedEvent != id {
			t.Fatalf("wrong supporting context: %#v", request)
		}
		if !strings.Contains(m.View().Content, "Timeline") {
			t.Fatalf("evidence timeline not rendered: %s", m.View().Content)
		}
	}
}

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

func TestMissingVerificationRendering(t *testing.T) {
	fixture := apptest.NewFixture(t, "changes.jsonl")
	page, err := fixture.Services.SessionFindings(context.Background(), "confirmed-changes")
	if err != nil {
		t.Fatal(err)
	}
	m := New(context.Background(), fixture.Services)
	m.screen = findingsScreen
	m.sessionsState.selected = "confirmed-changes"
	m.findingsState.page = page
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	m.width, m.height = 240, 120
	view := m.View().Content
	for _, want := range []string{"missing_verification · not_triggered", "Confirmed paths 1", "applicable changes 1", "verified changes 1", "missing changes 0", "failed completed check counts as verification performed", "actual file participation"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in change assessment: %s", want, view)
		}
	}
	if strings.Index(view, "unresolved_failed_verification") > strings.Index(view, "missing_verification") {
		t.Fatal("triggered assessment must appear first")
	}
}

func TestOpenCodeAssessmentRendering(t *testing.T) {
	fixture := apptest.NewOpenCodeFixture(t, "findings_evidence_v1.sql")
	sessions, err := fixture.Services.ListSessions(context.Background(), app.ListSessionsRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions.Sessions {
		if session.Title != "durable" {
			continue
		}
		page, err := fixture.Services.SessionFindings(context.Background(), session.ID)
		if err != nil {
			t.Fatal(err)
		}
		m := New(context.Background(), fixture.Services)
		m.screen = findingsScreen
		m.sessionsState.selected = session.ID
		m.findingsState.page = page
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
		m.width, m.height = 240, 120
		view := m.View().Content
		for _, want := range []string{"repeated_command_failures", "unresolved_failed_verification", "missing_verification", "recovered 1", "unresolved 1", "missing changes 1"} {
			if !strings.Contains(view, want) {
				t.Fatalf("missing %q in %s", want, view)
			}
		}
		return
	}
	t.Fatal("durable session missing")
}

func TestMoveConfigurationAssessmentRendering(t *testing.T) {
	fixture := apptest.NewFixture(t, "moves_configuration.jsonl")
	page, err := fixture.Services.SessionFindings(context.Background(), "move-configuration")
	if err != nil {
		t.Fatal(err)
	}
	m := New(context.Background(), fixture.Services)
	m.screen = findingsScreen
	m.sessionsState.selected = "move-configuration"
	m.findingsState.page = page
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	m.width, m.height = 240, 120
	view := m.View().Content
	for _, want := range []string{"Move origin scopes: 1 verified, 0 missing, 0 uncertain", "destination scopes: 1 verified, 0 missing, 0 uncertain", "2 module configuration scopes evaluated", "Workspace membership and nested module relationships remain uncertain", "actual file participation"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in scope assessment: %s", want, view)
		}
	}
}
