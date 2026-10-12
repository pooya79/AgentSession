package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestInspectionStartsWithEvidenceAndDisclosesMetadataOnRequest(t *testing.T) {
	m := readingModel(t)
	services := m.services.(*servicesStub)
	exit := 1
	services.detail = app.EventDetail{State: app.EvidenceComplete, Event: testEvent("long", 0),
		Payload: model.CommandData{InvocationID: "private-call-id", Phase: model.CommandCompleted,
			Command: "go test ./client", ExitCode: &exit, Output: "FAIL TestTimeout"}}
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	m, _ = updateModel(t, m, cmd())
	view := m.View().Content
	if !strings.Contains(view, "FAIL TestTimeout") || strings.Contains(view, "Event metadata") || strings.Contains(view, "Invocation ID") || strings.Contains(view, "Phase:") {
		t.Fatalf("inspection overwhelms evidence with metadata:\n%s", view)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if view := m.View().Content; !strings.Contains(view, "Event metadata") || !strings.Contains(view, "private-call-id") {
		t.Fatalf("metadata is not explicitly reachable:\n%s", view)
	}
}

func TestOperationalScreensKeepIdentitiesAndCountersBehindDisclosure(t *testing.T) {
	for _, test := range []struct {
		name   string
		screen screen
	}{{"projections", projectionsScreen}, {"indexing", indexingScreen}} {
		t.Run(test.name, func(t *testing.T) {
			screen := test.screen
			m := New(t.Context(), &servicesStub{})
			m.screen = screen
			m.width, m.height = 80, 24
			m.projectionsState.status = app.ProjectionStatus{State: app.EvidenceComplete,
				Projections: []app.ProjectionState{{Kind: "search", Status: app.ProjectionStatusReady, BuildAvailable: true, Usable: true, AttemptCount: 3}}}
			m.indexingState.status = app.ImportAllStatus{Phase: app.ImportAllUpToDate, SourcesDiscovered: 1, SourcesCompleted: 1,
				Sources: []app.ImportAllSourceStatus{{ID: "source-internal-id", Path: "/sanitized/sources/session.jsonl"}}}
			view := m.View().Content
			if strings.Contains(view, "attempts 3") || strings.Contains(view, "source-internal-id") || strings.Contains(view, "/sanitized/sources/") {
				t.Fatalf("operational overview leaks distracting details:\n%s", view)
			}
			m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
			view = m.View().Content
			if screen == projectionsScreen && !strings.Contains(view, "attempts 3") || screen == indexingScreen && !strings.Contains(view, "source-internal-id") {
				t.Fatalf("operational details are inaccessible:\n%s", view)
			}
		})
	}
}

func TestAssessmentsStartWithAnOverviewAndKeepFullExplanationsReachable(t *testing.T) {
	m := readingModel(t)
	services := m.services.(*servicesStub)
	services.findings = app.FindingsPage{State: app.EvidenceComplete, Assessments: []model.Finding{{
		ID: "assessment-internal-id", RuleID: "repeated_command_failures", State: model.FindingInsufficientEvidence,
		Explanation: strings.Repeat("The recorded result is uncertain. ", 12) + "FULL-EXPLANATION-END",
		EventIDs:    []model.EventID{"next"},
	}}}
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"})
	m, _ = updateModel(t, m, cmd())
	if view := m.View().Content; !strings.Contains(view, "Repeated command failures") || !strings.Contains(view, "insufficient evidence") || strings.Contains(view, "assessment-internal-id") || strings.Contains(view, "FULL-EXPLANATION-END") {
		t.Fatalf("assessment does not start with a readable overview:\n%s", view)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if view := m.View().Content; !strings.Contains(view, "FULL-EXPLANATION-END") {
		t.Fatalf("full explanation is not reachable:\n%s", view)
	}
}

func TestTimelineTargetUsesEventPositionInsteadOfInternalIdentity(t *testing.T) {
	m := readingModel(t)
	if view := m.View().Content; !strings.Contains(view, "Target: #0") || strings.Contains(view, "Target: long") {
		t.Fatalf("reader target exposes an internal identity instead of its visible position:\n%s", view)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: ']', Text: "]"})
	if view := m.View().Content; !strings.Contains(view, "Target: #1") {
		t.Fatalf("target did not follow event navigation:\n%s", view)
	}
}

func TestSessionBrowsingLeadsWithRecordedPreviewAndDisclosesCoverage(t *testing.T) {
	m := New(t.Context(), &servicesStub{})
	m.sessionsState.loading, m.sessionsState.overviewLoading = false, false
	m.width, m.height = 120, 32
	session := testSession("one")
	session.Title, session.Preview = "Fix retry timeout", "Bound retries without hiding errors."
	m.sessionsState.page = app.SessionPage{Sessions: []app.SessionSummary{session}}
	m.restoreSessionSelection()
	if view := m.View().Content; !strings.Contains(view, session.Preview) || strings.Contains(view, "Agents 0") || strings.Contains(view, "Interpretation:") {
		t.Fatalf("browsing does not lead with recorded context:\n%s", view)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	if view := m.View().Content; !strings.Contains(view, "Agents 0") || !strings.Contains(view, "Interpretation:") {
		t.Fatalf("coverage details cannot be disclosed:\n%s", view)
	}
}

func TestSearchShowsMatchesBeforeFiltersAndKeepsDetailsReachable(t *testing.T) {
	m := New(t.Context(), &servicesStub{})
	m.screen, m.width, m.height = searchScreen, 80, 24
	m.searchState.page = app.SearchPage{State: app.EvidenceComplete, Results: []app.SearchResult{{
		SessionID: "one", Title: "Fix retry timeout", Preview: "Recorded session preview", Snippet: "FAIL TestTimeout",
	}}}
	if view := m.View().Content; !strings.Contains(view, "FAIL TestTimeout") || strings.Contains(view, "Filters ·") || strings.Contains(view, "Session ·") {
		t.Fatalf("search does not lead with matches:\n%s", view)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	if view := m.View().Content; !strings.Contains(view, "Filters ·") || !strings.Contains(view, "Recorded session preview") {
		t.Fatalf("search details are inaccessible:\n%s", view)
	}
}

func TestHelpShowsActionsForTheCurrentScreen(t *testing.T) {
	m := readingModel(t)
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '?', Text: "?"})
	if view := m.View().Content; !strings.Contains(view, "expand/collapse") || strings.Contains(view, "Search editing") || strings.Contains(view, "rebuild all") {
		t.Fatalf("reader help mixes unrelated screen actions:\n%s", view)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	m, _ = updateModel(t, m, cmd())
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '?', Text: "?"})
	if view := m.View().Content; !strings.Contains(view, "wrapping") || !strings.Contains(view, "metadata") || strings.Contains(view, "rebuild all") {
		t.Fatalf("inspection help does not focus on reading:\n%s", view)
	}
}
