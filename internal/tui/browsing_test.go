package tui

import (
	"errors"
	"fmt"
	"image/color"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestSessionPreviewFollowsSelectionAndNarrowLayoutKeepsList(t *testing.T) {
	m := New(t.Context(), &servicesStub{})
	m.sessionsState.loading, m.sessionsState.overviewLoading = false, false
	one, two := testSession("one"), testSession("two")
	one.Title, one.Preview = "Fix retry timeout", "Bound retries without hiding errors."
	two.Title, two.Preview = "Document setup", "Explain local configuration."
	one.Summary = "Test failed; later build passed."
	one.State = app.EvidencePartial
	m.sessionsState.page = app.SessionPage{Sessions: []app.SessionSummary{one, two}}
	m.restoreSessionSelection()
	m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: 110, Height: 24})
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"[FOCUS] Sessions", "PREVIEW", one.Preview, one.Summary, "partial evidence", "Outcome: Unknown"} {
		if !strings.Contains(content, want) {
			t.Fatalf("wide browsing missing %q:\n%s", want, content)
		}
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.View().Content; !strings.Contains(got, two.Preview) || strings.Contains(got, one.Preview) {
		t.Fatalf("preview did not follow selection:\n%s", got)
	}
	for _, size := range [][2]int{{80, 24}, {54, 18}, {40, 12}, {120, 18}, {120, 12}} {
		m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		got := ansi.Strip(m.View().Content)
		if strings.Contains(got, "PREVIEW") || !strings.Contains(got, "> Document setup") || !strings.Contains(got, "? help") {
			t.Fatalf("single main view at %v:\n%s", size, got)
		}
	}
}

func TestSessionLabelsKeepUntrustedWhitespaceInsideTheirRows(t *testing.T) {
	m := New(t.Context(), &servicesStub{})
	m.sessionsState.loading, m.sessionsState.overviewLoading = false, false
	session := testSession("one")
	session.Title = "Fix\nretry\ttimeout"
	session.AgentName = "agent\nname"
	m.sessionsState.page = app.SessionPage{Sessions: []app.SessionSummary{session}}
	m.restoreSessionSelection()
	for _, width := range []int{80, 120} {
		m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: width, Height: 24})
		got := ansi.Strip(m.View().Content)
		if !strings.Contains(got, "> Fix retry timeout") || !strings.Contains(got, "AGENT NAME") {
			t.Fatalf("session metadata escaped its row at width %d:\n%s", width, got)
		}
	}
}

func TestInspectionKeepsPositionInsideOneLongPhysicalLine(t *testing.T) {
	m := readingModel(t)
	var text strings.Builder
	for i := range 2000 {
		fmt.Fprintf(&text, "word%04d ", i)
	}
	services := m.services.(*servicesStub)
	services.detail = app.EventDetail{State: app.EvidenceComplete, Event: testEvent("long", 0), Payload: model.CommandData{Output: text.String()}}
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	m, _ = updateModel(t, m, cmd())
	for range 4 {
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	anchor := regexp.MustCompile(`word\d{4}`).FindString(m.View().Content)
	if anchor == "" || anchor == "word0000" {
		t.Fatal("did not reach middle of physical line")
	}
	for _, message := range []tea.Msg{tea.KeyPressMsg{Code: 'w', Text: "w"}, tea.WindowSizeMsg{Width: 54, Height: 18}, tea.KeyPressMsg{Code: 'w', Text: "w"}} {
		m, _ = updateModel(t, m, message)
		if got := m.View().Content; !strings.Contains(got, anchor) {
			t.Fatalf("formatting lost %s inside one line:\n%s", anchor, got)
		}
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'w', Text: "w"})
	for range 10 {
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	}
	panAnchor := regexp.MustCompile(`word\d{4}`).FindString(m.View().Content)
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'w', Text: "w"})
	if got := m.View().Content; panAnchor == "" || !strings.Contains(got, panAnchor) {
		t.Fatalf("wrapping lost panned text %s:\n%s", panAnchor, got)
	}
}

func TestInspectionRefreshKeepsEvidenceAndVisibleFailure(t *testing.T) {
	m := readingModel(t)
	services := m.services.(*servicesStub)
	services.detail = app.EventDetail{State: app.EvidenceComplete, Event: testEvent("long", 0), Payload: m.timelineState.page.Payloads["long"]}
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	m, _ = updateModel(t, m, cmd())
	for range 3 {
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	anchor := regexp.MustCompile(`line-\d{3}`).FindString(m.View().Content)
	services.detailErr = errors.New("temporary detail failure")
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if got := m.View().Content; !strings.Contains(got, "Refreshing inspection") || !strings.Contains(got, anchor) {
		t.Fatalf("pending refresh lost state/evidence:\n%s", got)
	}
	m, _ = updateModel(t, m, cmd())
	if got := m.View().Content; !strings.Contains(got, "Inspection refresh failed") || !strings.Contains(got, "temporary detail failure") || !strings.Contains(got, anchor) {
		t.Fatalf("refresh failure hidden or evidence discarded:\n%s", got)
	}
}

func TestNormalizedInspectionWrapPanAndReturn(t *testing.T) {
	m := readingModel(t)
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	before := ansi.Strip(m.View().Content)
	services := m.services.(*servicesStub)
	event := testEvent("long", 0)
	event.Kind = model.EventKindCommand
	exit := 1
	services.detail = app.EventDetail{State: app.EvidenceComplete, Event: event,
		Payload: model.CommandData{Command: "go test ./...", ExitCode: &exit, Output: "    PREFIX" + strings.Repeat("x", 200) + "TAIL"}}
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	if cmd == nil {
		t.Fatal("d did not request normalized inspection")
	}
	m, _ = updateModel(t, m, cmd())
	got := ansi.Strip(m.View().Content)
	for _, want := range []string{"RECORDED FAILURE", "PREFIX", "TAIL", "Wrapped"} {
		if !strings.Contains(got, want) {
			t.Fatalf("inspection missing %q:\n%s", want, got)
		}
	}
	if len(services.detailCalls) != 1 || services.detailCalls[0].EventID != "long" {
		t.Fatalf("wrong detail request: %+v", services.detailCalls)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'w', Text: "w"})
	if got := m.View().Content; strings.Contains(got, "TAIL") || !strings.Contains(got, "Unwrapped") {
		t.Fatalf("unwrapped mode did not expose original lines:\n%s", got)
	}
	for range 50 {
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	}
	if got := m.View().Content; !strings.Contains(got, "TAIL") || !strings.Contains(got, "column") {
		t.Fatalf("pan cannot reach long-line suffix:\n%s", got)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := ansi.Strip(m.View().Content); got != before {
		t.Fatalf("inspection return changed reading context:\nbefore=%s\nafter=%s", before, got)
	}
}

func TestInspectionFormattingAndResizePreserveReadingText(t *testing.T) {
	m := readingModel(t)
	var text strings.Builder
	for i := range 80 {
		fmt.Fprintf(&text, "row-%03d %s\n", i, strings.Repeat("value ", 20))
	}
	services := m.services.(*servicesStub)
	services.detail = app.EventDetail{State: app.EvidenceComplete, Event: testEvent("long", 0), Payload: model.MessageData{Text: text.String()}}
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	if cmd == nil {
		t.Fatal("inspection not reachable")
	}
	m, _ = updateModel(t, m, cmd())
	for range 4 {
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	content := ansi.Strip(m.View().Content)
	start := strings.Index(content, "row-")
	if start < 0 {
		t.Fatalf("no readable row:\n%s", content)
	}
	anchor := content[start : start+7]
	for _, message := range []tea.Msg{tea.KeyPressMsg{Code: 'w', Text: "w"}, tea.WindowSizeMsg{Width: 54, Height: 18}, tea.KeyPressMsg{Code: 'w', Text: "w"}} {
		m, _ = updateModel(t, m, message)
		if got := m.View().Content; !strings.Contains(got, anchor) {
			t.Fatalf("format/resize lost %s:\n%s", anchor, got)
		}
	}
}

func TestMessageMarkdownKeepsStructureCodeAndSafeContent(t *testing.T) {
	m := readingModel(t)
	m.timelineState.page.Payloads["long"] = model.MessageData{Role: model.MessageRoleAssistant, Text: "## Retry policy\n\n- Keep the cause\n  1. Bound each retry\n\n```go\n    return \"" + strings.Repeat("x", 100) + "TAIL\"\n```\n[reference](https://example.invalid/doc)\n\x1b]52;c;secret\a\u202e"}
	m.invalidateTimelineRender()
	got := ansi.Strip(m.View().Content)
	for _, want := range []string{"Retry policy", "- Keep the cause", "  1. Bound each retry", "Code · go", "    return", "TAIL", "https://example.invalid/doc", "<U+202E>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted message missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "## Retry") || strings.Contains(got, "```go") || strings.Contains(got, "secret") {
		t.Fatalf("markup or unsafe terminal control survived:\n%s", got)
	}
}

func TestReadingSpaceAndInsufficientSize(t *testing.T) {
	m := readingModel(t)
	content := ansi.Strip(m.View().Content)
	if strings.Count(content, "evidence") < 17 {
		t.Fatalf("baseline timeline leaves too little reading space:\n%s", content)
	}
	for _, size := range [][2]int{{39, 24}, {80, 11}, {32, 10}} {
		m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		got := ansi.Strip(m.View().Content)
		if !strings.Contains(got, "Resize") || !strings.Contains(got, "40×12") || strings.Contains(got, "line-000") {
			t.Fatalf("missing deliberate resize state at %v:\n%s", size, got)
		}
	}
}

func TestTechnicalRowsAreCompactAndRecordedResultsStayExplicit(t *testing.T) {
	m := readingModel(t)
	exit := 1
	m.timelineState.page.Events = []model.EventSummary{testEvent("failed", 1), testEvent("pending", 2), testEvent("plan", 3)}
	m.timelineState.page.Payloads = map[model.EventID]model.NormalizedData{
		"failed":  model.CommandData{Command: "go test ./client", ExitCode: &exit, Output: "PRIVATE OUTPUT"},
		"pending": model.CommandData{Command: "go build ./client"},
		"plan":    model.SummaryData{Category: model.SummaryCategoryPlan, Text: "PRIVATE PLAN"},
	}
	m.timelineState.cursor, m.timelineState.selected = 0, "failed"
	m.invalidateTimelineRender()
	got := ansi.Strip(m.View().Content)
	for _, want := range []string{"> #1 +", "go test ./client", "RECORDED FAILURE", "RESULT UNAVAILABLE", "Plan update"} {
		if !strings.Contains(got, want) {
			t.Fatalf("collapsed evidence missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "PRIVATE") || strings.Contains(got, "Press Enter") {
		t.Fatalf("technical rows are not compact:\n%s", got)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.View().Content; !strings.Contains(got, "> #1 -") || !strings.Contains(got, "PRIVATE OUTPUT") {
		t.Fatalf("expanded state inaccessible:\n%s", got)
	}
}

func TestFocusAndEvidenceStatesSurviveThemesAndCompactResize(t *testing.T) {
	for _, background := range []color.Color{color.Black, color.White} {
		m := readingModel(t)
		exit := 1
		m.timelineState.page.Events = []model.EventSummary{testEvent("failure", 12)}
		m.timelineState.page.Payloads = map[model.EventID]model.NormalizedData{"failure": model.CommandData{Command: "go test", ExitCode: &exit}}
		m.timelineState.selected = "failure"
		m.indexingState.err = fmt.Errorf("import read failed")
		m.invalidateTimelineRender()
		m, _ = updateModel(t, m, tea.BackgroundColorMsg{Color: background})
		for _, size := range [][2]int{{120, 32}, {80, 24}, {54, 18}, {40, 12}} {
			m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			got := ansi.Strip(m.View().Content)
			for _, want := range []string{"> #12 +", "RECORDED FAILURE", "Target:", "Index: ERROR", "? help"} {
				if !strings.Contains(got, want) {
					t.Fatalf("color-free state missing %q at %v:\n%s", want, size, got)
				}
			}
			if len(strings.Split(got, "\n")) > size[1] {
				t.Fatal("height exceeded")
			}
			for _, line := range strings.Split(got, "\n") {
				if ansi.StringWidth(line) > size[0] {
					t.Fatalf("width exceeded: %q", line)
				}
			}
		}
	}
}

func TestMarkdownListContinuationsAndTabsStayIndented(t *testing.T) {
	m := readingModel(t)
	m.timelineState.page.Payloads["long"] = model.MessageData{Text: "12. " + strings.Repeat("listword ", 14) + "END\n\n```go\n\treturn " + strings.Repeat("x", 75) + "CODEEND\n```"}
	m.invalidateTimelineRender()
	m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: 40, Height: 24})
	got := ansi.Strip(m.View().Content)
	for _, want := range []string{"12. listword", "\n    listword", "    return", "CODEEND"} {
		if !strings.Contains(got, want) {
			t.Fatalf("indentation missing %q:\n%s", want, got)
		}
	}
}
