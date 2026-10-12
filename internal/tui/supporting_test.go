package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestProjectionSelectionAndLongDiagnosticsRemainReachable(t *testing.T) {
	status := app.ProjectionStatus{State: app.EvidenceComplete}
	for i := range 8 {
		status.Projections = append(status.Projections, app.ProjectionState{
			Kind: fmt.Sprintf("projection-%d", i), Status: app.ProjectionStatusFailed, BuildAvailable: true,
			Diagnostic: &app.ProjectionDiagnostic{Code: "build_failed", Summary: strings.Repeat("explanation ", 35) + fmt.Sprintf("END-%d", i)},
		})
	}
	services := &servicesStub{projections: status}
	m := New(t.Context(), services)
	m.screen, m.sessionsState.selected = timelineScreen, "session-1"
	m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	m, _ = updateModel(t, m, cmd())
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'G', Text: "G"})
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "> projection-7") || !strings.Contains(got, "[FOCUS]") {
		t.Fatalf("last projection is not visibly actionable:\n%s", got)
	}
	seen := ""
	for range 20 {
		seen += ansi.Strip(m.View().Content)
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	if !strings.Contains(seen, "END-7") {
		t.Fatalf("selected diagnostic tail cannot be read:\n%s", seen)
	}
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'b', Text: "b"})
	if cmd == nil {
		t.Fatal("selected rebuild unavailable")
	}
	_ = cmd()
	if len(services.rebuildCalls) != 1 || services.rebuildCalls[0] != "projection-7" {
		t.Fatalf("wrong rebuild scope: %v", services.rebuildCalls)
	}
}

func TestSupportingHelpOwnsFocusAndRestoresReading(t *testing.T) {
	m := readingModel(t)
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	m, _ = updateModel(t, m, detailLoadedMsg{generation: m.requestGeneration, detail: app.EventDetail{
		State: app.EvidencePartial, Event: testEvent("long", 1),
		Payload: model.MessageData{Role: model.MessageRoleAssistant, Text: strings.Repeat("reading evidence\n", 60)},
	}})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	before := ansi.Strip(m.View().Content)
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '?', Text: "?"})
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "[FOCUS] Help") || !strings.Contains(strings.Split(view, "\n")[m.height-1], "Esc close") {
		t.Fatalf("help advertises underlying controls:\n%s", view)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'G', Text: "G"})
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "Ctrl-C") {
		t.Fatalf("long help cannot reach final controls:\n%s", got)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := ansi.Strip(m.View().Content); got != before {
		t.Fatalf("help changed inspection position:\nbefore %s\nafter %s", before, got)
	}
}

func TestCanceledSupportingReadsOfferRetryWithoutSuccess(t *testing.T) {
	for _, screen := range []screen{detailScreen, findingsScreen, projectionsScreen} {
		t.Run(fmt.Sprint(screen), func(t *testing.T) {
			m := New(t.Context(), &servicesStub{})
			m.screen, m.sessionsState.selected = screen, "session-1"
			var response tea.Msg
			switch screen {
			case detailScreen:
				response = detailLoadedMsg{generation: m.requestGeneration, err: context.Canceled}
			case findingsScreen:
				response = findingsLoadedMsg{generation: m.requestGeneration, err: context.Canceled}
			case projectionsScreen:
				response = projectionStatusMsg{generation: m.projectionsState.generation, err: context.Canceled}
			}
			m, _ = updateModel(t, m, response)
			got := ansi.Strip(m.View().Content)
			if !strings.Contains(strings.ToLower(got), "canceled") || !strings.Contains(got, "r to retry") || strings.Contains(got, "Canonical evidence remains available") {
				t.Fatalf("cancellation lost its retry state:\n%s", got)
			}
		})
	}
}

func TestUnknownInspectionIsExplicitUnavailableAndClearedOnNavigation(t *testing.T) {
	services := &servicesStub{inspection: app.UnknownEvidenceInspection{State: app.EvidenceNotFound}}
	m := readingModel(t)
	m.services = services
	event := testEvent("unknown", 2)
	event.Kind = model.EventKindUnknown
	m.timelineState.page = app.TimelinePage{State: app.EvidencePartial, Events: []model.EventSummary{event}, Payloads: map[model.EventID]model.NormalizedData{
		event.ID: model.UnknownData{Reason: model.UnknownUnsupportedRecordKind, OriginalKind: "future"},
	}}
	m.timelineState.selected = event.ID
	m.invalidateTimelineRender()
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'u', Text: "u"})
	m, _ = updateModel(t, m, cmd())
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "could not be located") {
		t.Fatalf("not-found raw inspection looks like no request:\n%s", got)
	}
	services.inspection = app.UnknownEvidenceInspection{State: app.EvidenceComplete, EventID: event.ID,
		Text: "EXPLICIT-REDACTED-RECORD\x1b]52;c;unsafe\a", ReturnedSize: 24, OriginalSize: 100, RedactionCount: 1, Truncated: true}
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'u', Text: "u"})
	m, _ = updateModel(t, m, cmd())
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "EXPLICIT-REDACTED-RECORD") || !strings.Contains(got, "truncated true") || strings.Contains(got, "unsafe") {
		t.Fatalf("raw inspection missing bounded/safe context:\n%s", got)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}) // Leave query editing.
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := ansi.Strip(m.View().Content); strings.Contains(got, "EXPLICIT-REDACTED-RECORD") || !strings.Contains(got, "Press u") {
		t.Fatalf("raw record survived navigation:\n%s", got)
	}
}

func TestSupportingStatesAndControlsAtAllUsableSizes(t *testing.T) {
	for _, size := range [][2]int{{120, 32}, {80, 24}, {54, 18}, {40, 12}} {
		for _, screen := range []screen{findingsScreen, detailScreen, indexingScreen, projectionsScreen} {
			t.Run(fmt.Sprintf("%d-%dx%d", screen, size[0], size[1]), func(t *testing.T) {
				m := New(t.Context(), &servicesStub{})
				m.screen = screen
				m.sessionsState.selected = "session-1"
				m.indexingState.status = app.ImportAllStatus{Phase: app.ImportAllUpToDate}
				m.indexingState.err = errors.New("AgentSession import observation failed")
				m.detailState.detail = app.EventDetail{State: app.EvidenceUnavailable, Event: testEvent("event-1", 1)}
				m.findingsState.page = app.FindingsPage{State: app.EvidenceUnavailable, Status: "running"}
				m.projectionsState.status = app.ProjectionStatus{State: app.EvidenceComplete, Projections: []app.ProjectionState{{Kind: "findings", Status: app.ProjectionStatusFailed, Stale: true, BuildAvailable: true}}}
				m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				got := ansi.Strip(m.View().Content)
				if !strings.Contains(got, "[FOCUS]") {
					t.Fatalf("supporting screen has no focus:\n%s", got)
				}
				for _, line := range strings.Split(got, "\n") {
					if ansi.StringWidth(line) > size[0] {
						t.Fatalf("line exceeds terminal: %q", line)
					}
				}
				if screen == indexingScreen && strings.Contains(got, "completed successfully") {
					t.Fatalf("observation failure displayed as success:\n%s", got)
				}
				footer := strings.Split(got, "\n")[size[1]-1]
				if !strings.Contains(footer, "Esc") || !strings.Contains(footer, "?") {
					t.Fatalf("return/help controls clipped: %q", footer)
				}
			})
		}
	}
}

func TestDetailLoadingCanceledBySearchCanRetryAndRejectLateResponse(t *testing.T) {
	m := readingModel(t)
	m, initial := updateModel(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	if initial == nil || !strings.Contains(ansi.Strip(m.View().Content), "Loading normalized payload") {
		t.Fatalf("detail request has no loading feedback:\n%s", m.View().Content)
	}
	generation := m.requestGeneration
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	m, _ = updateModel(t, m, detailLoadedMsg{generation: generation, detail: app.EventDetail{State: app.EvidenceComplete, Event: testEvent("late", 0)}})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	got := ansi.Strip(m.View().Content)
	if !strings.Contains(got, "canceled") || !strings.Contains(got, "r to retry") || strings.Contains(got, "late") {
		t.Fatalf("canceled initial inspection cannot recover:\n%s", got)
	}
	m, retry := updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if retry == nil || !strings.Contains(ansi.Strip(m.View().Content), "Loading normalized payload") {
		t.Fatalf("detail retry is stuck:\n%s", m.View().Content)
	}
}

func TestAssessmentReferenceSelectionRevealsTheActualTarget(t *testing.T) {
	var refs []model.EventID
	for i := range 80 {
		refs = append(refs, model.EventID(fmt.Sprintf("supporting-%02d", i)))
	}
	m := readingModel(t)
	services := m.services.(*servicesStub)
	services.findings = app.FindingsPage{State: app.EvidenceComplete, Assessments: []model.Finding{{Explanation: "Recorded failure remains unresolved.", EventIDs: refs}}}
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"})
	m, _ = updateModel(t, m, cmd())
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: ']', Text: "]"})
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "> Supporting event 2") {
		t.Fatalf("selected reference is not visible:\n%s", got)
	}
	before := ansi.Strip(m.View().Content)
	services.timeline = app.TimelinePage{State: app.EvidenceComplete, Events: []model.EventSummary{testEvent("supporting-01", 1)}}
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = updateModel(t, m, cmd())
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := ansi.Strip(m.View().Content); got != before {
		t.Fatalf("return from supporting timeline changed reference context:\n%s", got)
	}
}

func TestProjectionActionSupersedesStatusReadAndPreservesTargetOnRefresh(t *testing.T) {
	services := &servicesStub{projections: app.ProjectionStatus{State: app.EvidenceComplete, Projections: []app.ProjectionState{
		{Kind: "findings", Status: app.ProjectionStatusReady, BuildAvailable: true},
		{Kind: "search", Status: app.ProjectionStatusFailed, BuildAvailable: true},
	}}}
	m := readingModel(t)
	m.services = services
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	m, _ = updateModel(t, m, cmd())
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'j', Text: "j"})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	oldGeneration := m.projectionsState.generation
	m, action := updateModel(t, m, tea.KeyPressMsg{Code: 'b', Text: "b"})
	m, _ = updateModel(t, m, projectionStatusMsg{generation: oldGeneration, status: app.ProjectionStatus{State: app.EvidenceNotFound}})
	if got := ansi.Strip(m.View().Content); strings.Contains(got, "no longer available") {
		t.Fatalf("superseded read replaced pending action:\n%s", got)
	}
	if action == nil {
		t.Fatal("action was not admitted")
	}
	m, _ = updateModel(t, m, projectionStatusMsg{generation: m.projectionsState.generation, status: app.ProjectionStatus{State: app.EvidenceComplete, Projections: []app.ProjectionState{
		{Kind: "search", Status: app.ProjectionStatusReady, BuildAvailable: true},
		{Kind: "findings", Status: app.ProjectionStatusReady, BuildAvailable: true},
	}}})
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'b', Text: "b"})
	_ = cmd()
	if got := services.rebuildCalls; len(got) != 1 || got[0] != "search" {
		t.Fatalf("refresh changed the action target: %v", got)
	}
}

func TestProjectionReturnFromSearchRestoresSessionAndSelectedScope(t *testing.T) {
	services := &servicesStub{projections: app.ProjectionStatus{State: app.EvidenceComplete, SessionID: "session-1", Projections: []app.ProjectionState{
		{Kind: "findings", Status: app.ProjectionStatusReady, BuildAvailable: true},
		{Kind: "search", Status: app.ProjectionStatusFailed, BuildAvailable: true},
	}}}
	m := readingModel(t)
	m.services = services
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	m, _ = updateModel(t, m, cmd())
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'j', Text: "j"})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m, _ = updateModel(t, m, searchLoadedMsg{generation: m.requestGeneration, page: app.SearchPage{State: app.EvidenceComplete, Results: []app.SearchResult{{SessionID: "session-2"}}}})
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = updateModel(t, m, cmd())
	services.projections = app.ProjectionStatus{State: app.EvidenceComplete, SessionID: "session-2", Projections: []app.ProjectionState{{Kind: "other", Status: app.ProjectionStatusPending}}}
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	m, _ = updateModel(t, m, cmd())
	for range 3 {
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	}
	got := ansi.Strip(m.View().Content)
	if !strings.Contains(got, "session session-1") || !strings.Contains(got, "[FOCUS] search") || strings.Contains(got, "> other") {
		t.Fatalf("nested projection visit changed saved scope:\n%s", got)
	}
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'b', Text: "b"})
	_ = cmd()
	if len(services.rebuildCalls) != 1 || services.rebuildCalls[0] != "search" {
		t.Fatalf("return rebuilt wrong scope: %v", services.rebuildCalls)
	}
}

func TestUnknownRawInspectionAvailableWhenNormalizedPayloadIsUnavailable(t *testing.T) {
	event := testEvent("unknown", 1)
	event.Kind = model.EventKindUnknown
	services := &servicesStub{detail: app.EventDetail{State: app.EvidenceUnavailable, Event: event},
		inspection: app.UnknownEvidenceInspection{State: app.EvidenceComplete, EventID: event.ID, Text: "BOUNDED REDACTED RAW"}}
	m := readingModel(t)
	m.services = services
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	m, _ = updateModel(t, m, cmd())
	if strings.Contains(m.View().Content, "BOUNDED REDACTED RAW") {
		t.Fatal("raw content became implicit")
	}
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'u', Text: "u"})
	m, _ = updateModel(t, m, cmd())
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'G', Text: "G"})
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "BOUNDED REDACTED RAW") || !strings.Contains(got, "unavailable evidence") {
		t.Fatalf("missing normalized payload hid explicit raw evidence:\n%s", got)
	}
}

func TestIncompleteCommandDetailsExplainMissingOutput(t *testing.T) {
	m := readingModel(t)
	event := testEvent("pending", 1)
	event.Kind = model.EventKindCommand
	m.services = &servicesStub{detail: app.EventDetail{State: app.EvidencePartial, Event: event,
		Payload: model.CommandData{Command: "go test ./...", Phase: model.CommandStarted}}}
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	m, _ = updateModel(t, m, cmd())
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "RESULT UNAVAILABLE") || !strings.Contains(got, "No output recorded") || !strings.Contains(got, "partial evidence") {
		t.Fatalf("incomplete command details resemble empty success:\n%s", got)
	}
}

func TestAssessmentRefreshPreservesOrExplainsSupportingSelection(t *testing.T) {
	m := readingModel(t)
	services := m.services.(*servicesStub)
	services.findings = app.FindingsPage{State: app.EvidenceComplete, Assessments: []model.Finding{{EventIDs: []model.EventID{"first", "second", "selected"}}}}
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"})
	m, _ = updateModel(t, m, cmd())
	for range 2 {
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: ']', Text: "]"})
	}
	services.findings.Assessments = []model.Finding{{EventIDs: []model.EventID{"selected", "first"}}}
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	m, _ = updateModel(t, m, cmd())
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "Evidence 1/2") {
		t.Fatalf("reordered reference lost its identity:\n%s", got)
	}
	services.findings.Assessments = []model.Finding{{EventIDs: []model.EventID{"remaining"}}}
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	m, _ = updateModel(t, m, cmd())
	got := ansi.Strip(m.View().Content)
	if !strings.Contains(got, "Evidence 1/1") || !strings.Contains(got, "reference changed or removed") {
		t.Fatalf("removed reference left a misleading target:\n%s", got)
	}
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = cmd()
	if len(services.timelineCalls) != 1 || services.timelineCalls[0].FocusedEvent != "remaining" {
		t.Fatalf("wrong supporting navigation after refresh: %v", services.timelineCalls)
	}
}
