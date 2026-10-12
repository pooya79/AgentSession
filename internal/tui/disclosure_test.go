package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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

func TestSessionOverviewShowsEachRecordedTextOnce(t *testing.T) {
	m := New(t.Context(), &servicesStub{})
	m.sessionsState.loading, m.sessionsState.overviewLoading = false, false
	m.width, m.height = 120, 32
	session := testSession("one")
	session.Title, session.Preview = "Fix retry timeout", "Fix retry timeout"
	m.sessionsState.page = app.SessionPage{Sessions: []app.SessionSummary{session}}
	m.restoreSessionSelection()
	view := ansi.Strip(m.View().Content)
	if strings.Count(view, session.Title) != 1 || strings.Contains(view, "[FOCUS]") || strings.Contains(view, "Events 0") {
		t.Fatalf("session overview repeats context or administrative labels:\n%s", view)
	}
	if !strings.Contains(view, "Outcome: Unknown") || !strings.Contains(view, "Enter open") || !strings.Contains(view, "/ search") {
		t.Fatalf("overview lost its meaning or primary actions:\n%s", view)
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
	if view := m.View().Content; !strings.Contains(view, "Repeated failures") || !strings.Contains(view, "not enough evidence") || strings.Contains(view, "assessment-internal-id") || strings.Contains(view, "FULL-EXPLANATION-END") {
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
	if view := m.View().Content; !strings.Contains(view, "FAIL TestTimeout") || strings.Contains(view, "Filters ·") || strings.Contains(view, "Session ·") || strings.Contains(view, "Match ·") || strings.Contains(view, "Search canonical evidence") {
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

func TestReaderChromeLeavesOnlyTheTargetAndPrimaryActions(t *testing.T) {
	m := readingModel(t)
	m.indexingState.status = app.ImportAllStatus{Phase: app.ImportAllUpToDate, SourcesDiscovered: 3, SourcesCompleted: 3}
	view := ansi.Strip(m.View().Content)
	footer := strings.Split(view, "\n")
	if strings.Contains(view, "[FOCUS]") || strings.Contains(view, "Index: complete") || strings.Count(footer[len(footer)-1], " · ") > 3 {
		t.Fatalf("reader chrome repeats background status or too many actions:\n%s", view)
	}
	m.indexingState.err = fmt.Errorf("cannot read index")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Index: ERROR") {
		t.Fatalf("simplification hides a background failure:\n%s", view)
	}
}

func TestAssessmentsListSharedEvidenceOnceAndStillOpenEveryEvent(t *testing.T) {
	m := readingModel(t)
	services := m.services.(*servicesStub)
	services.findings = app.FindingsPage{State: app.EvidenceComplete, Assessments: []model.Finding{
		{RuleID: "repeated_command_failures", State: model.FindingTriggered, Explanation: "The command failed repeatedly.", EventIDs: []model.EventID{"next", "long"}},
		{RuleID: "unresolved_failed_verification", State: model.FindingTriggered, Explanation: "No relevant successful rerun is recorded.", EventIDs: []model.EventID{"next"}},
	}}
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"})
	m, _ = updateModel(t, m, cmd())
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Evidence 1/2") || strings.Count(view, "#1 ·") != 1 || strings.Contains(view, "The command failed repeatedly.") {
		t.Fatalf("assessment overview repeats links or full explanations:\n%s", view)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: ']', Text: "]"})
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Evidence 2/2") || !strings.Contains(view, "> #0") {
		t.Fatalf("second unique reference is inaccessible:\n%s", view)
	}
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = cmd()
	if calls := services.timelineCalls; len(calls) == 0 || calls[len(calls)-1].FocusedEvent != "long" {
		t.Fatalf("the selected evidence did not open its timeline: %+v", calls)
	}
}

func TestCommandInspectionShowsCommandOnceAndOutputWithoutFieldIndentation(t *testing.T) {
	m := readingModel(t)
	services := m.services.(*servicesStub)
	exit := 1
	services.detail = app.EventDetail{State: app.EvidenceComplete, Event: testEvent("long", 0),
		Payload: model.CommandData{Command: "go test ./client", WorkingDirectory: "/fixture/project", ExitCode: &exit, Output: "FAIL TestTimeout\nretry_test.go:42: timed out"}}
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	m, _ = updateModel(t, m, cmd())
	view := ansi.Strip(m.View().Content)
	if strings.Count(view, "go test ./client") != 1 || strings.Contains(view, "Working directory:") || !strings.Contains(view, "\nFAIL TestTimeout") || !strings.Contains(view, "RECORDED FAILURE") {
		t.Fatalf("inspection repeats the command or crowds its output:\n%s", view)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Working directory: /fixture/project") || !strings.Contains(view, "Exit status: 1") {
		t.Fatalf("command metadata was lost:\n%s", view)
	}
}

func TestDerivedViewsUseReadableNamesAndKeepUnavailableBuildersExplicit(t *testing.T) {
	m := New(t.Context(), &servicesStub{})
	m.screen, m.width, m.height = projectionsScreen, 80, 24
	m.projectionsState.status = app.ProjectionStatus{State: app.EvidenceComplete, Projections: []app.ProjectionState{
		{Kind: "search", Status: app.ProjectionStatusReady, BuildAvailable: true},
		{Kind: "git_correlation", Status: app.ProjectionStatusPending},
	}}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Git links") || !strings.Contains(view, "not implemented") || strings.Contains(view, "git_correlation") || strings.Contains(view, "session ") {
		t.Fatalf("derived view overview exposes internal names or hides unavailability:\n%s", view)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "git_correlation") {
		t.Fatalf("full derived view identity is inaccessible:\n%s", view)
	}
}

func TestSessionOverviewKeepsSuccessfulBackgroundWorkQuiet(t *testing.T) {
	m := New(t.Context(), &servicesStub{})
	m.sessionsState.loading, m.sessionsState.overviewLoading = false, false
	m.sessionsState.page = app.SessionPage{Sessions: []app.SessionSummary{testSession("one")}}
	m.restoreSessionSelection()
	m.indexingState.status = app.ImportAllStatus{Phase: app.ImportAllUpToDate, SourcesDiscovered: 3, SourcesCompleted: 3}
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "CANONICAL INDEX") || strings.Contains(view, "COMPLETE") {
		t.Fatalf("successful background work dominates browsing:\n%s", view)
	}
	m.indexingState.status.Active = true
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Indexing") {
		t.Fatalf("active background work is invisible:\n%s", view)
	}
}

func TestConversationUsesPlainSpeakerLabelsAndKeepsMessageText(t *testing.T) {
	m := readingModel(t)
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "> You") || !strings.Contains(view, "line-000 evidence") || strings.ContainsAny(view, "╭╰") {
		t.Fatalf("conversation is obscured by decorative card chrome:\n%s", view)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: ']', Text: "]"})
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "> Assistant") || !strings.Contains(view, "NEXT EVENT") {
		t.Fatalf("speaker or content was lost while navigating:\n%s", view)
	}
}

func TestIndexingOverviewShowsProgressBeforeSourceInventory(t *testing.T) {
	m := New(t.Context(), &servicesStub{})
	m.screen, m.width, m.height = indexingScreen, 80, 24
	m.indexingState.status = app.ImportAllStatus{Phase: app.ImportAllUpToDate, SourcesDiscovered: 3, SourcesCompleted: 3,
		Sources: []app.ImportAllSourceStatus{{ID: "source", Path: "/fixture/rollout.jsonl", Phase: app.ImportCompleted}}}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "3/3 sources checked") || strings.Contains(view, "rollout.jsonl") {
		t.Fatalf("indexing starts with an inventory instead of progress:\n%s", view)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "/fixture/rollout.jsonl") {
		t.Fatalf("source inventory cannot be disclosed:\n%s", view)
	}
}

func TestUnknownInspectionOffersOneExplicitRawAction(t *testing.T) {
	m := readingModel(t)
	services := m.services.(*servicesStub)
	event := testEvent("long", 0)
	event.Kind = model.EventKindUnknown
	services.detail = app.EventDetail{State: app.EvidenceComplete, Event: event,
		Payload: model.UnknownData{Reason: model.UnknownUnsupportedRecordKind, OriginalKind: "future_activity"}}
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	m, _ = updateModel(t, m, cmd())
	view := ansi.Strip(m.View().Content)
	if strings.Count(view, "Press u") != 1 || !strings.Contains(view, "redacted, bounded") || strings.Contains(view, "Original kind:") {
		t.Fatalf("Unknown inspection repeats prompts and format metadata:\n%s", view)
	}
}
