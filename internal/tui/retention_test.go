package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

func historyModel(t *testing.T, count int) (Model, *readingTimelineServices) {
	t.Helper()
	services := &readingTimelineServices{servicesStub: &servicesStub{}, payloads: make(map[model.EventID]model.NormalizedData)}
	for i := range count {
		e := testEvent(fmt.Sprintf("history-%03d", i), int64(i))
		services.events = append(services.events, e)
		services.payloads[e.ID] = model.MessageData{Text: fmt.Sprintf("HISTORY CONTENT %03d", i)}
	}
	m := New(t.Context(), services)
	m.screen = timelineScreen
	m.sessionsState.selected = "session-1"
	m.spinnerActive = true
	m, cmd := updateModel(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	m, _ = updateModel(t, m, cmd())
	return m, services
}

func historyKey(t *testing.T, m Model, key string) Model {
	t.Helper()
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: rune(key[0]), Text: key})
	if cmd != nil {
		m, _ = updateModel(t, m, cmd())
	}
	return m
}

func TestTimelineEvictionRevisitKeepsEveryEventReachable(t *testing.T) {
	m, services := historyModel(t, 400)
	for i := 0; i < 400; i++ {
		if i > 0 {
			m = historyKey(t, m, "]")
		}
		if got := m.View().Content; !strings.Contains(got, fmt.Sprintf("Target: history-%03d", i)) {
			t.Fatalf("forward event %d lost:\n%s", i, got)
		}
	}
	before := len(services.timelineCalls)
	for i := 398; i >= 0; i-- {
		m = historyKey(t, m, "[")
		if got := m.View().Content; !strings.Contains(got, fmt.Sprintf("Target: history-%03d", i)) {
			t.Fatalf("earlier event %d lost:\n%s", i, got)
		}
	}
	focused := false
	for _, request := range services.timelineCalls[before:] {
		if request.FocusedEvent != "" {
			focused = true
		}
		if request.Limit > 50 || !request.IncludePayloads {
			t.Fatalf("unbounded reload: %+v", request)
		}
	}
	if !focused {
		t.Fatal("earlier history was retained instead of reloaded through bounded focused reads")
	}
	for i := 1; i < 400; i++ {
		m = historyKey(t, m, "]")
		if got := m.View().Content; !strings.Contains(got, fmt.Sprintf("Target: history-%03d", i)) {
			t.Fatalf("revisited forward event %d lost:\n%s", i, got)
		}
	}
}

func TestTimelineContentSizeForcesReloadBelowEventCountBudget(t *testing.T) {
	m, services := historyModel(t, 12)
	for id := range services.payloads {
		services.payloads[id] = model.MessageData{Text: strings.Repeat("evidence ", 65536)}
	}
	m = historyKey(t, m, "r")
	for range 11 {
		m = historyKey(t, m, "]")
	}
	before := len(services.timelineCalls)
	for range 11 {
		m = historyKey(t, m, "[")
	}
	if len(services.timelineCalls) == before {
		t.Fatal("large payloads were retained across the whole history")
	}
	if got := m.View().Content; !strings.Contains(got, "Target: history-000") || !strings.Contains(got, "evidence") {
		t.Fatalf("large evidence lost on revisit:\n%s", got)
	}
}

func TestTimelineEvictedSelectionReadFailureRetriesWithoutLosingReading(t *testing.T) {
	m, services := historyModel(t, 350)
	for range 349 {
		m = historyKey(t, m, "]")
	}
	services.timelineErr = fmt.Errorf("local index temporarily unavailable")
	for range 200 {
		m = historyKey(t, m, "[")
	}
	failed := m.View().Content
	if !strings.Contains(failed, "temporarily unavailable") || !strings.Contains(failed, "HISTORY CONTENT") {
		t.Fatalf("failed reload hid usable evidence:\n%s", failed)
	}
	services.timelineErr = nil
	target := m.timelineState.selected
	m = historyKey(t, m, "r")
	if got := m.View().Content; !strings.Contains(got, "Target: "+string(target)) || strings.Contains(got, "temporarily unavailable") || strings.Contains(got, "Loading") {
		t.Fatalf("retry did not restore evicted target %s:\n%s", target, got)
	}
}

func TestTimelineReimportDuringEvictedReloadRemovesMissingOverlap(t *testing.T) {
	m, services := historyModel(t, 400)
	for range 399 {
		m = historyKey(t, m, "]")
	}
	// Remove an event inside a future focused reload window, retaining stable IDs
	// on either side. Keyset pages are current evidence, not a snapshot.
	services.events = append(services.events[:160], services.events[161:]...)
	for i := 398; i >= 150; i-- {
		if i == 160 {
			continue
		}
		m = historyKey(t, m, "[")
		if got := m.View().Content; !strings.Contains(got, fmt.Sprintf("Target: history-%03d", i)) {
			t.Fatalf("reimport kept a missing/duplicated event before %d:\n%s", i, got)
		}
	}
}

func TestTimelineFocusedHistoryScrollsBothDirectionsWithoutJump(t *testing.T) {
	_, services := historyModel(t, 400)
	page, err := services.Timeline(t.Context(), app.TimelineRequest{SessionID: "session-1", FocusedEvent: "history-250", Limit: 50, IncludePayloads: true})
	if err != nil {
		t.Fatal(err)
	}
	m := New(t.Context(), services)
	m.screen = timelineScreen
	m.sessionsState.selected = "session-1"
	m.spinnerActive = true
	m, _ = updateModel(t, m, timelineLoadedMsg{generation: m.requestGeneration, page: page})
	m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	// A pending earlier read is visible and leaves the existing reading anchor.
	before := m.View().Content
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if cmd == nil || !strings.Contains(m.View().Content, "Loading local history") {
		t.Fatalf("earlier read has no loading feedback:\n%s", m.View().Content)
	}
	m, _ = updateModel(t, m, cmd())
	if !strings.Contains(before, "HISTORY CONTENT 201") || !strings.Contains(m.View().Content, "HISTORY CONTENT 201") {
		t.Fatalf("earlier reload jumped reading:\n%s", m.View().Content)
	}
	for range 80 {
		m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
		if cmd != nil {
			m, _ = updateModel(t, m, cmd())
		}
	}
	if got := m.View().Content; !strings.Contains(got, "HISTORY CONTENT 000") || strings.Contains(got, "Loading") {
		t.Fatalf("start is unreachable or stuck:\n%s", got)
	}
	for range 130 {
		m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
		if cmd != nil {
			m, _ = updateModel(t, m, cmd())
		}
	}
	if got := m.View().Content; !strings.Contains(got, "HISTORY CONTENT 399") || !strings.Contains(got, "End of timeline") {
		t.Fatalf("end is unreachable:\n%s", got)
	}
}

func TestTimelineIndexedEvictionPreservesDisclosureAndRevalidatesAnchor(t *testing.T) {
	service, path, _ := timelineIndex(t, 400)
	m := indexedTimelineModel(t, service)
	m = historyKey(t, m, "r")
	for range 7 {
		m = historyKey(t, m, "]")
	}
	target := m.timelineState.selected
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		m, _ = updateModel(t, m, cmd())
	}
	if !strings.Contains(m.View().Content, "EVIDENCE-00007") {
		t.Fatalf("technical disclosure missing:\n%s", m.View().Content)
	}
	for range 392 {
		m = historyKey(t, m, "]")
	}
	for range 392 {
		m = historyKey(t, m, "[")
	}
	m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: 60, Height: 24})
	if got := m.View().Content; !strings.Contains(got, "EVIDENCE-00007") || !strings.Contains(got, "Enter to hide details") {
		t.Fatalf("eviction/resize lost disclosure:\n%s", got)
	}
	// Replace this sanitized fixture, then import through the real application.
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source = bytes.ReplaceAll(source, []byte("EVIDENCE-00007"), []byte("UPDATED-EVIDENCE-00007"))
	if err = os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = service.DiscoverAndImport(t.Context()); err != nil {
		t.Fatal(err)
	}
	m = historyKey(t, m, "r")
	if got := m.View().Content; !strings.Contains(got, "anchor changed") || !strings.Contains(got, "UPDATED-EVIDENCE-00007") {
		t.Fatalf("changed anchor silently restored:\n%s", got)
	}
	// Remove the same native-identity event without changing surrounding IDs.
	records := bytes.Split(source, []byte("\n"))
	source = nil
	for _, record := range records {
		if len(record) > 0 && !bytes.Contains(record, []byte("UPDATED-EVIDENCE-00007")) {
			source = append(source, record...)
			source = append(source, '\n')
		}
	}
	if err = os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = service.DiscoverAndImport(t.Context()); err != nil {
		t.Fatal(err)
	}
	m = historyKey(t, m, "r")
	if got := m.View().Content; !strings.Contains(got, "anchor removed") || strings.Contains(got, string(target)) {
		t.Fatalf("removed anchor was claimed available:\n%s", got)
	}
}

func TestTimelineRenderedContentBudgetReloadsShortLineEvidence(t *testing.T) {
	m, services := historyModel(t, 12)
	for id := range services.payloads {
		services.payloads[id] = model.MessageData{Text: strings.Repeat("x\n", 30000)}
	}
	m = historyKey(t, m, "r")
	for range 11 {
		m = historyKey(t, m, "]")
	}
	before := len(services.timelineCalls)
	for range 11 {
		m = historyKey(t, m, "[")
	}
	if len(services.timelineCalls) == before {
		t.Fatal("rendered short-line evidence was all retained despite its content size")
	}
	if got := m.View().Content; !strings.Contains(got, "Target: history-000") || !strings.Contains(got, "x") {
		t.Fatalf("short-line evidence lost:\n%s", got)
	}
}

type gatedHistoryServices struct {
	*readingTimelineServices
	entered chan context.Context
	release chan struct{}
}

func (s *gatedHistoryServices) Timeline(ctx context.Context, request app.TimelineRequest) (app.TimelinePage, error) {
	page, err := s.readingTimelineServices.Timeline(ctx, request)
	if request.FocusedEvent != "" {
		s.entered <- ctx
		<-s.release
	}
	return page, err
}

func TestTimelineDelayedEvictedReloadCanBeCanceledAndRetried(t *testing.T) {
	m, services := historyModel(t, 400)
	for range 399 {
		m = historyKey(t, m, "]")
	}
	gated := &gatedHistoryServices{readingTimelineServices: services, entered: make(chan context.Context, 10), release: make(chan struct{})}
	m.services = gated
	var cmd tea.Cmd
	for range 200 {
		m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: '[', Text: "["})
		if cmd != nil {
			break
		}
	}
	if cmd == nil {
		t.Fatal("no evicted-history read")
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	ctx := <-gated.entered
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"})
	if ctx.Err() != context.Canceled {
		t.Fatal("leaving history did not cancel its read")
	}
	m, _ = updateModel(t, m, cmd())
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	before := m.View().Content
	close(gated.release)
	m, _ = updateModel(t, m, <-result)
	if got := m.View().Content; got != before {
		t.Fatalf("late history reply changed reading:\n%s", got)
	}
	target := m.timelineState.selected
	m = historyKey(t, m, "r")
	if got := m.View().Content; !strings.Contains(got, "Target: "+string(target)) || strings.Contains(got, "Loading local history") {
		t.Fatalf("canceled reload could not retry:\n%s", got)
	}
}

func TestTimelineTextScrollCanEvictTheUnchangedActionTarget(t *testing.T) {
	m, _ := historyModel(t, 350)
	for range 1600 {
		m = historyKey(t, m, "j")
	}
	if got := m.View().Content; !strings.Contains(got, "HISTORY CONTENT 349") || !strings.Contains(got, "Target: history-000") {
		t.Fatalf("text scrolling jumped back to the evicted action target:\n%s", got)
	}
}

func TestTimelineTextScrollCrossesAnOversizedEventWithoutTruncation(t *testing.T) {
	m, services := historyModel(t, 3)
	services.payloads["history-001"] = model.MessageData{Text: "LARGE START\n" + strings.Repeat("x", 3<<20) + "\nLARGE END"}
	m = historyKey(t, m, "r")
	seenStart, seenEnd := strings.Contains(m.View().Content, "LARGE START"), false
	for step := range 2300 {
		var cmd tea.Cmd
		m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
		if cmd != nil {
			m, _ = updateModel(t, m, cmd())
		}
		frame := m.View().Content
		seenStart = seenStart || strings.Contains(frame, "LARGE START")
		seenEnd = seenEnd || strings.Contains(frame, "LARGE END")
		if step == 8 && !seenStart {
			t.Fatal("oversized event never entered the scrollable window")
		}
		if strings.Contains(frame, "HISTORY CONTENT 002") {
			if !seenStart || !seenEnd {
				t.Fatal("oversized evidence was skipped or truncated")
			}
			return
		}
	}
	t.Fatal("scrolling could not cross the oversized event")
}

func TestTimelineEventJumpAcrossPreviouslyUnvisitedEarlierHistory(t *testing.T) {
	_, services := historyModel(t, 400)
	page, err := services.Timeline(t.Context(), app.TimelineRequest{SessionID: "session-1", FocusedEvent: "history-250", Limit: 50, IncludePayloads: true})
	if err != nil {
		t.Fatal(err)
	}
	m := New(t.Context(), services)
	m.screen = timelineScreen
	m.sessionsState.selected = "session-1"
	m.spinnerActive = true
	m, _ = updateModel(t, m, timelineLoadedMsg{generation: m.requestGeneration, page: page})
	m = historyKey(t, m, "[")
	if got := m.View().Content; !strings.Contains(got, "Target: history-200") {
		t.Fatalf("earlier event jump stopped at the loaded boundary:\n%s", got)
	}
}

func TestTimelineSavedHistoryCanInspectUnknownEvidenceAfterReload(t *testing.T) {
	m, services := historyModel(t, 200)
	services.events[0].Kind = model.EventKindUnknown
	services.payloads["history-000"] = model.UnknownData{OriginalKind: "future", Reason: model.UnknownUnsupportedRecordKind}
	services.inspection = app.UnknownEvidenceInspection{State: app.EvidenceComplete, EventID: "history-000", Text: "SANITIZED RAW EVIDENCE"}
	services.findings = app.FindingsPage{State: app.EvidenceComplete, Assessments: []model.Finding{{EventIDs: []model.EventID{"history-199"}}}}
	m = historyKey(t, m, "r")
	m = historyKey(t, m, "u")
	m = historyKey(t, m, "f")
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = updateModel(t, m, cmd())
	for range 2 {
		m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
		if cmd != nil {
			m, _ = updateModel(t, m, cmd())
		}
	}
	if got := m.View().Content; !strings.Contains(got, "Raw inspection evicted") || strings.Contains(got, "SANITIZED RAW EVIDENCE") {
		t.Fatalf("raw eviction/explicit-action state lost:\n%s", got)
	}
	m = historyKey(t, m, "u")
	if got := m.View().Content; !strings.Contains(got, "SANITIZED RAW EVIDENCE") {
		t.Fatalf("explicit inspection failed after saved history reload:\n%s", got)
	}
}

func TestTimelineEvictedTargetSurvivesRemovalOfItsPrefetchAnchor(t *testing.T) {
	m, services := historyModel(t, 400)
	for range 399 {
		m = historyKey(t, m, "]")
	}
	services.events = append(services.events[:25], services.events[26:]...)
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
	if cmd != nil {
		m, _ = updateModel(t, m, cmd())
	}
	if got := m.View().Content; !strings.Contains(got, "HISTORY CONTENT 000") || strings.Contains(got, "anchor removed") {
		t.Fatalf("available target was lost with its prefetch anchor:\n%s", got)
	}
}

func TestTimelineFocusedOverlapUpdatesHeadersWithUnchangedPayload(t *testing.T) {
	for _, payload := range []model.NormalizedData{model.CommandData{Command: "go test ./..."}, nil} {
		t.Run(fmt.Sprintf("%T", payload), func(t *testing.T) {
			m, services := historyModel(t, 3)
			services.payloads["history-000"] = payload
			m = historyKey(t, m, "r")
			_ = m.View()
			// The source preserves native IDs and payloads but updates the summary.
			// Copy the service page because its original slice was already returned.
			services.events = append([]model.EventSummary(nil), services.events...)
			for i := range services.events {
				services.events[i].Sequence += 10
			}
			services.events[0].Summary = "UPDATED SUMMARY"
			page, err := services.Timeline(t.Context(), app.TimelineRequest{SessionID: "session-1", FocusedEvent: "history-002", Limit: 50, IncludePayloads: true})
			if err != nil {
				t.Fatal(err)
			}
			m, _ = updateModel(t, m, timelineLoadedMsg{generation: m.requestGeneration, page: page, history: true})
			want := "#10 +"
			if payload == nil {
				want = "UPDATED SUMMARY"
			}
			if got := m.View().Content; !strings.Contains(got, want) {
				t.Fatalf("focused overlap retained an obsolete header, want %q:\n%s", want, got)
			}
		})
	}
}

func TestTimelineDelayedEvictedSelectionPreservesNewerTextScrolling(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{{Code: 'j', Text: "j"}, {Code: 'k', Text: "k"}, {Code: tea.KeyPgUp}, {Code: tea.KeyPgDown}} {
		t.Run(key.String(), func(t *testing.T) {
			m, services := historyModel(t, 400)
			for range 399 {
				m = historyKey(t, m, "]")
			}
			// Leave room below the reading position so removing the loading row
			// does not itself clamp the viewport at the end of the timeline.
			services.payloads["history-399"] = model.MessageData{Text: "HISTORY CONTENT 399" + strings.Repeat("\nTAIL EVIDENCE", 100)}
			m = historyKey(t, m, "r")
			m, pending := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
			if pending == nil {
				t.Fatal("no evicted selection read")
			}
			// Leave the local read pending while a newer scroll establishes intent.
			m, _ = updateModel(t, m, key)
			firstEvidence := regexp.MustCompile(`HISTORY CONTENT \d{3}|TAIL EVIDENCE`)
			before := firstEvidence.FindString(m.View().Content)
			if before == "" {
				t.Fatal("pending read hid retained evidence")
			}
			m, _ = updateModel(t, m, pending())
			if got := firstEvidence.FindString(m.View().Content); got != before {
				t.Fatalf("late selection read replaced newer scroll: before %q, after %q\n%s", before, got, m.View().Content)
			}
		})
	}
}
