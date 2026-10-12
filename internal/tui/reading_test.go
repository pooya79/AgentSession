package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

type readingTimelineServices struct {
	*servicesStub
	events   []model.EventSummary
	payloads map[model.EventID]model.NormalizedData
}

func (s *readingTimelineServices) Timeline(ctx context.Context, request app.TimelineRequest) (app.TimelinePage, error) {
	if err := ctx.Err(); err != nil {
		return app.TimelinePage{}, err
	}
	s.timelineCalls = append(s.timelineCalls, request)
	if s.timelineErr != nil {
		return app.TimelinePage{}, s.timelineErr
	}
	start, end := 0, 0
	if request.FocusedEvent != "" {
		found := false
		for i, event := range s.events {
			if event.ID == request.FocusedEvent {
				end = i + 1
				start = max(0, end-request.Limit)
				found = true
				break
			}
		}
		if !found {
			return app.TimelinePage{State: app.EvidenceNotFound}, nil
		}
	} else {
		if request.Cursor != "" {
			start, _ = strconv.Atoi(request.Cursor)
		}
		end = min(len(s.events), start+request.Limit)
	}
	page := app.TimelinePage{State: app.EvidenceComplete, Events: s.events[start:end], Payloads: make(map[model.EventID]model.NormalizedData)}
	for _, event := range page.Events {
		page.Payloads[event.ID] = s.payloads[event.ID]
	}
	if end < len(s.events) {
		page.NextCursor = strconv.Itoa(end)
	}
	return page, nil
}

func readingModel(t *testing.T) Model {
	t.Helper()
	m := New(t.Context(), &servicesStub{})
	m.screen = timelineScreen
	m.sessionsState.selected = "session-1"
	event := testEvent("long", 0)
	var text strings.Builder
	for i := range 100 {
		fmt.Fprintf(&text, "line-%03d evidence\n", i)
	}
	m.timelineState.page = app.TimelinePage{State: app.EvidenceComplete,
		Events: []model.EventSummary{event, testEvent("next", 1)},
		Payloads: map[model.EventID]model.NormalizedData{
			event.ID: model.MessageData{Role: model.MessageRoleUser, Text: text.String()},
			"next":   model.MessageData{Role: model.MessageRoleAssistant, Text: "NEXT EVENT"},
		},
	}
	m.timelineState.selected = event.ID
	m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	return m
}

func TestTimelinePageScrollReachesLongMessageEnd(t *testing.T) {
	m := readingModel(t)
	for range 12 {
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
		if strings.Contains(m.View().Content, "NEXT EVENT") {
			return
		}
	}
	t.Fatalf("page scrolling never reached the end:\n%s", m.View().Content)
}

func TestTimelineTextScrollAndEventNavigationHaveSeparateTargets(t *testing.T) {
	m := readingModel(t)
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyDown}, {Code: 'j', Text: "j"}} {
		m, _ = updateModel(t, m, key)
	}
	if got := m.View().Content; !strings.Contains(got, "line-002") || strings.Contains(got, "line-000") || strings.Contains(got, "NEXT EVENT") {
		t.Fatalf("text scroll jumped events:\n%s", got)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: ']', Text: "]"})
	if got := m.View().Content; !strings.Contains(got, "NEXT EVENT") || !strings.Contains(got, "Target: #1") {
		t.Fatalf("event jump or visible action target missing:\n%s", got)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '[', Text: "["})
	if got := m.View().Content; !strings.Contains(got, "line-000") || !strings.Contains(got, "Target: #0") {
		t.Fatalf("previous event did not reveal its start:\n%s", got)
	}
}

func TestTimelineResizeKeepsPositionWithinMessage(t *testing.T) {
	m := readingModel(t)
	var text strings.Builder
	for i := range 1000 {
		fmt.Fprintf(&text, "word%04d ", i)
	}
	m.timelineState.page.Payloads["long"] = model.MessageData{Role: model.MessageRoleUser, Text: text.String()}
	m.invalidateTimelineRender()
	for range 3 {
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	anchor := regexp.MustCompile(`word\d{4}`).FindString(m.View().Content)
	if anchor == "" {
		t.Fatal("no reading anchor visible")
	}
	m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	if got := m.View().Content; !strings.Contains(got, anchor) {
		t.Fatalf("resize lost %s:\n%s", anchor, got)
	}
}

func TestTimelineReturnsFromTemporaryNavigation(t *testing.T) {
	for _, route := range []string{"help", "findings", "search", "search-result", "finding-evidence", "detail"} {
		t.Run(route, func(t *testing.T) {
			m := readingModel(t)
			m.timelineState.page.Payloads["next"] = model.SummaryData{Text: "EXPANDED DETAILS"}
			m.invalidateTimelineRender()
			for _, key := range []tea.KeyPressMsg{{Code: ']', Text: "]"}, {Code: tea.KeyEnter}, {Code: '[', Text: "["}, {Code: tea.KeyPgDown}, {Code: tea.KeyPgDown}} {
				m, _ = updateModel(t, m, key)
			}
			anchor := regexp.MustCompile(`line-\d{3}`).FindString(m.View().Content)
			services := m.services.(*servicesStub)
			originalPage := m.timelineState.page
			switch route {
			case "help":
				m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '?', Text: "?"})
			case "findings", "finding-evidence":
				services.findings = app.FindingsPage{State: app.EvidenceComplete, Assessments: []model.Finding{{EventIDs: []model.EventID{"next"}}}}
				var cmd tea.Cmd
				m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"})
				m, _ = updateModel(t, m, cmd())
				if route == "finding-evidence" {
					services.timeline = app.TimelinePage{State: app.EvidenceComplete, Events: []model.EventSummary{testEvent("next", 1)}}
					m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
					m, _ = updateModel(t, m, cmd())
					m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
				}
			case "search", "search-result":
				m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
				m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}) // leave editing
				if route == "search-result" {
					m.searchState.page = app.SearchPage{Results: []app.SearchResult{{SessionID: "other"}}}
					services.timeline = app.TimelinePage{State: app.EvidenceComplete, Events: []model.EventSummary{testEvent("other-event", 0)}}
					var cmd tea.Cmd
					m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
					m, _ = updateModel(t, m, cmd())
					m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
				}
			case "detail":
				m.screen = detailScreen
			}
			services.timeline = originalPage
			var returnCmd tea.Cmd
			m, returnCmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
			if returnCmd != nil {
				m, _ = updateModel(t, m, returnCmd())
			}
			if got := m.View().Content; anchor == "" || !strings.Contains(got, anchor) || !strings.Contains(got, "Target: #0") || !strings.Contains(got, "session session-1") {
				t.Fatalf("%s return lost reading context %q:\n%s", route, anchor, got)
			}
			m, _ = updateModel(t, m, tea.KeyPressMsg{Code: ']', Text: "]"})
			if got := m.View().Content; !strings.Contains(got, "EXPANDED DETAILS") {
				t.Fatalf("expansion lost:\n%s", got)
			}
		})
	}
}

func TestTimelineRefreshPreservesOrExplainsReadingAnchor(t *testing.T) {
	for _, scenario := range []string{"unchanged", "changed", "removed", "failure"} {
		t.Run(scenario, func(t *testing.T) {
			m := readingModel(t)
			for range 2 {
				m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
			}
			anchor := regexp.MustCompile(`line-\d{3}`).FindString(m.View().Content)
			services := m.services.(*servicesStub)
			services.timeline = app.TimelinePage{State: app.EvidenceComplete,
				Events:   append([]model.EventSummary(nil), m.timelineState.page.Events...),
				Payloads: map[model.EventID]model.NormalizedData{"long": m.timelineState.page.Payloads["long"], "next": m.timelineState.page.Payloads["next"]},
			}
			switch scenario {
			case "changed":
				services.timeline.Payloads["long"] = model.MessageData{Text: "REPLACED EVIDENCE"}
			case "removed":
				services.timeline.Events = services.timeline.Events[1:]
			case "failure":
				services.timelineErr = errors.New("temporary read failure")
			}
			m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
			if got := m.View().Content; !strings.Contains(got, anchor) {
				t.Fatalf("refresh discarded usable evidence in flight:\n%s", got)
			}
			m, _ = updateModel(t, m, cmd())
			got := m.View().Content
			switch scenario {
			case "unchanged":
				if !strings.Contains(got, anchor) {
					t.Fatalf("refresh lost %s:\n%s", anchor, got)
				}
			case "changed", "removed":
				if !strings.Contains(got, "Reading anchor "+scenario) {
					t.Fatalf("no honest recovery explanation:\n%s", got)
				}
			case "failure":
				if !strings.Contains(got, anchor) || !strings.Contains(got, "temporary read failure") {
					t.Fatalf("failure lost evidence or error:\n%s", got)
				}
				services.timelineErr = nil
				m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
				m, _ = updateModel(t, m, cmd())
				if !strings.Contains(m.View().Content, anchor) {
					t.Fatal("retry lost reading anchor")
				}
			}
		})
	}
}

func TestTimelineRefreshRetainsReadingBeyondFirstChunk(t *testing.T) {
	services := &readingTimelineServices{servicesStub: &servicesStub{}, payloads: make(map[model.EventID]model.NormalizedData)}
	for i := range 120 {
		event := testEvent(fmt.Sprintf("event-%03d", i), int64(i))
		services.events = append(services.events, event)
		services.payloads[event.ID] = model.MessageData{Text: fmt.Sprintf("event %d\n", i) + strings.Repeat("available evidence\n", 60)}
	}
	m := New(t.Context(), services)
	m.screen = timelineScreen
	m.sessionsState.selected = "session-1"
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	m, _ = updateModel(t, m, cmd())
	for range 119 {
		m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: ']', Text: "]"})
		if cmd != nil {
			m, _ = updateModel(t, m, cmd())
		}
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	before := m.View().Content
	if !strings.Contains(before, "Target: #119") {
		t.Fatalf("continuation setup failed:\n%s", before)
	}
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	m, _ = updateModel(t, m, cmd())
	if got := m.View().Content; !strings.Contains(got, "Target: #119") || !strings.Contains(got, "available evidence") || strings.Contains(got, "Reading anchor removed") {
		t.Fatalf("refresh lost continued context:\n%s", got)
	}
	for _, request := range services.timelineCalls {
		if request.Limit > app.MaximumPageSize || !request.IncludePayloads {
			t.Fatalf("unbounded or summary-only read: %#v", request)
		}
	}
}

func TestTimelineCanceledContinuationCanResumeAfterInspection(t *testing.T) {
	for _, key := range []string{"f", "u"} {
		t.Run(key, func(t *testing.T) {
			m := readingModel(t)
			m.timelineState.page.Events[0].Kind = model.EventKindUnknown
			m.timelineState.page.Events[1].Kind = model.EventKindUnknown
			m.timelineState.page.NextCursor = "continue"
			m, pending := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
			if pending == nil {
				t.Fatal("continuation did not start")
			}
			stale := pending()
			m, inspection := updateModel(t, m, tea.KeyPressMsg{Code: rune(key[0]), Text: key})
			if inspection == nil {
				t.Fatal("inspection did not start")
			}
			if key == "f" {
				m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
			}
			m, _ = updateModel(t, m, stale)
			m, retry := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
			if retry == nil {
				t.Fatalf("canceled continuation cannot resume:\n%s", m.View().Content)
			}
		})
	}
}

type delayedTimelineServices struct {
	*servicesStub
	entered chan context.Context
	release chan struct{}
}

func (s *delayedTimelineServices) Timeline(ctx context.Context, request app.TimelineRequest) (app.TimelinePage, error) {
	s.entered <- ctx
	<-s.release // Deliberately deliver a success even if cancellation was ignored.
	return app.TimelinePage{State: app.EvidenceComplete, Events: []model.EventSummary{testEvent("STALE EVIDENCE", 3)}}, nil
}

func TestTimelineDelayedContinuationCannotOverwriteInspectionOrBlockRetry(t *testing.T) {
	m := readingModel(t)
	m.timelineState.page.Events[1].Kind = model.EventKindUnknown
	m.timelineState.page.Payloads["next"] = model.UnknownData{OriginalKind: "future", Reason: model.UnknownUnsupportedRecordKind}
	m.timelineState.page.NextCursor = "continue"
	services := &delayedTimelineServices{servicesStub: &servicesStub{inspection: app.UnknownEvidenceInspection{State: app.EvidenceComplete, EventID: "next", Text: "FRESH RAW EVIDENCE"}}, entered: make(chan context.Context, 2), release: make(chan struct{})}
	m.services = services
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: ']', Text: "]"})
	completed := make(chan tea.Msg, 1)
	go func() { completed <- cmd() }()
	requestCtx := <-services.entered
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'u', Text: "u"})
	if requestCtx.Err() != context.Canceled {
		t.Fatal("superseded continuation context was not canceled")
	}
	m, _ = updateModel(t, m, cmd())
	close(services.release)
	m, _ = updateModel(t, m, <-completed)
	if got := m.View().Content; strings.Contains(got, "STALE EVIDENCE") {
		t.Fatalf("late result routed into current evidence:\n%s", got)
	}
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if got := m.View().Content; !strings.Contains(got, "FRESH RAW EVIDENCE") {
		t.Fatalf("requested evidence is not reachable by scrolling:\n%s", got)
	}
	if cmd == nil {
		t.Fatal("superseded continuation left retry blocked")
	}
}

func TestTimelineDisclosureAboveReadingAndContinuationPreserveContent(t *testing.T) {
	m := readingModel(t)
	tech := testEvent("technical", -1)
	tech.Kind = model.EventKindCommand
	m.timelineState.page.Events = append([]model.EventSummary{tech}, m.timelineState.page.Events...)
	m.timelineState.page.Payloads[tech.ID] = model.CommandData{Command: "go test", Output: strings.Repeat("technical output\n", 40)}
	m.timelineState.selected = tech.ID
	m.invalidateTimelineRender()
	for range 3 {
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	anchor := regexp.MustCompile(`line-\d{3}`).FindString(m.View().Content)
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.View().Content; anchor == "" || !strings.Contains(got, anchor) {
		t.Fatalf("disclosure above reading lost %s:\n%s", anchor, got)
	}
	m, _ = updateModel(t, m, timelineLoadedMsg{generation: m.requestGeneration, cursor: "continue", page: app.TimelinePage{State: app.EvidenceComplete, Events: []model.EventSummary{testEvent("later", 2)}}})
	if got := m.View().Content; !strings.Contains(got, anchor) {
		t.Fatalf("continuation lost %s:\n%s", anchor, got)
	}
}

func TestTimelineReopeningDisclosureRestoresPositionInsideDetails(t *testing.T) {
	m := readingModel(t)
	text := m.timelineState.page.Payloads["long"].(model.MessageData).Text
	m.timelineState.page.Payloads["long"] = model.SummaryData{Text: text}
	m.invalidateTimelineRender()
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	for range 3 {
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	anchor := regexp.MustCompile(`line-\d{3}`).FindString(m.View().Content)
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.View().Content; anchor == "" || !strings.Contains(got, anchor) {
		t.Fatalf("reopening details lost %s:\n%s", anchor, got)
	}
}

func TestTimelineReturnRetriesCanceledInitialLoad(t *testing.T) {
	services := &servicesStub{timeline: app.TimelinePage{State: app.EvidenceComplete, Events: []model.EventSummary{testEvent("loaded", 0)}}}
	m := New(t.Context(), services)
	m.sessionsState.loading = false
	m.sessionsState.page = app.SessionPage{Sessions: []app.SessionSummary{testSession("session-1")}}
	m, stale := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"})
	m, retry := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if retry == nil {
		t.Fatal("return did not retry canceled initial load")
	}
	m, _ = updateModel(t, m, stale())
	m, _ = updateModel(t, m, retry())
	if got := m.View().Content; !strings.Contains(got, "Target: #0") || strings.Contains(got, "Loading timeline events") {
		t.Fatalf("initial load recovery failed:\n%s", got)
	}
}

func TestFindingsFailedRefreshRetainsPreviousSnapshot(t *testing.T) {
	services := &servicesStub{findingsErr: errors.New("assessment read failed")}
	m := New(t.Context(), services)
	m.screen = findingsScreen
	m.findingsState.page = app.FindingsPage{State: app.EvidenceComplete, Assessments: []model.Finding{{Explanation: "RECORDED ASSESSMENT"}}}
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if got := m.View().Content; !strings.Contains(got, "RECORDED ASSESSMENT") || !strings.Contains(got, "Previous snapshot") {
		t.Fatalf("pending refresh discarded prior evidence:\n%s", got)
	}
	m, _ = updateModel(t, m, cmd())
	if got := m.View().Content; !strings.Contains(got, "RECORDED ASSESSMENT") || !strings.Contains(got, "assessment read failed") || !strings.Contains(got, "Previous snapshot") {
		t.Fatalf("failed refresh lost evidence or its age:\n%s", got)
	}
}

func TestTimelineBlankLinesRemainScrollableAndKeepPosition(t *testing.T) {
	m := readingModel(t)
	m.timelineState.page.Payloads["long"] = model.MessageData{Text: "BEFORE\n" + strings.Repeat("\n", 50) + "AFTER"}
	m.invalidateTimelineRender()
	for range 2 {
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if got := m.View().Content; strings.Contains(got, "AFTER") {
		t.Fatalf("line scrolling skipped whitespace content:\n%s", got)
	}
	before := m.View().Content
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '?', Text: "?"})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := m.View().Content; got != before {
		t.Fatalf("help return moved position within blank lines:\n%s", got)
	}
	for range 60 {
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if got := m.View().Content; !strings.Contains(got, "AFTER") {
		t.Fatalf("content after blank lines unreachable:\n%s", got)
	}
}

func TestFindingsRetentionIsSessionScopedAndCanceledRefreshStaysUnconfirmed(t *testing.T) {
	m := readingModel(t)
	services := m.services.(*servicesStub)
	services.findings = app.FindingsPage{State: app.EvidenceComplete, Assessments: []model.Finding{{Explanation: "SESSION A ASSESSMENT"}}}
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"})
	m, _ = updateModel(t, m, cmd())
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := m.View().Content; !strings.Contains(got, "SESSION A ASSESSMENT") || !strings.Contains(got, "Previous snapshot") {
		t.Fatalf("canceled refresh claimed confirmed freshness:\n%s", got)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m.searchState.page = app.SearchPage{Results: []app.SearchResult{{SessionID: "session-B"}}}
	services.timeline = app.TimelinePage{State: app.EvidenceComplete, Events: []model.EventSummary{testEvent("B-event", 0)}}
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = updateModel(t, m, cmd())
	services.findingsErr = errors.New("B read failed")
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"})
	if got := m.View().Content; strings.Contains(got, "SESSION A ASSESSMENT") {
		t.Fatalf("pending B read shows A evidence:\n%s", got)
	}
	m, _ = updateModel(t, m, cmd())
	if got := m.View().Content; strings.Contains(got, "SESSION A ASSESSMENT") || !strings.Contains(got, "B read failed") {
		t.Fatalf("failed B read shows A evidence or hides error:\n%s", got)
	}
}

func TestTimelineRefreshRevalidatesRawReadingAnchor(t *testing.T) {
	for _, scenario := range []string{"unchanged", "changed", "unavailable", "failure"} {
		t.Run(scenario, func(t *testing.T) {
			m := readingModel(t)
			m.timelineState.page.Events[0].Kind = model.EventKindUnknown
			m.timelineState.page.Payloads["long"] = model.UnknownData{OriginalKind: "future", Reason: model.UnknownUnsupportedRecordKind}
			m.invalidateTimelineRender()
			services := m.services.(*servicesStub)
			var raw strings.Builder
			for i := range 100 {
				fmt.Fprintf(&raw, "RAW%03d evidence\n", i)
			}
			services.inspection = app.UnknownEvidenceInspection{State: app.EvidenceComplete, EventID: "long", Text: raw.String()}
			m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'u', Text: "u"})
			m, _ = updateModel(t, m, cmd())
			for range 3 {
				m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
			}
			anchor := regexp.MustCompile(`RAW\d{3}`).FindString(m.View().Content)
			if anchor == "" {
				t.Fatal("no raw reading anchor visible")
			}
			services.timeline = m.timelineState.page
			switch scenario {
			case "changed":
				services.inspection.Text = "UPDATED RAW"
			case "unavailable":
				services.inspection = app.UnknownEvidenceInspection{State: app.EvidenceUnavailable, EventID: "long"}
			case "failure":
				services.inspectionErr = errors.New("raw read failed")
			}
			m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
			m, _ = updateModel(t, m, cmd())
			got := m.View().Content
			switch scenario {
			case "unchanged":
				if !strings.Contains(got, anchor) {
					t.Fatalf("refresh lost unchanged raw anchor %s:\n%s", anchor, got)
				}
			case "changed", "unavailable":
				if !strings.Contains(got, "Reading anchor changed") {
					t.Fatalf("raw change missing explanation:\n%s", got)
				}
			case "failure":
				if !strings.Contains(got, anchor) || !strings.Contains(got, "raw read failed") {
					t.Fatalf("raw failure lost prior evidence or error:\n%s", got)
				}
			}
		})
	}
}
