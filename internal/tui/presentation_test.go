package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestResizeZeroNarrowAndQuit(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	if got := m.View(); !got.AltScreen || got.Content == "" {
		t.Fatalf("zero-size view = %#v", got)
	}
	m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: 24, Height: 6})
	view := m.View()
	if strings.Count(view.Content, "\n") >= 6 {
		t.Fatalf("narrow view rows = %d", strings.Count(view.Content, "\n")+1)
	}
	for _, line := range strings.Split(view.Content, "\n") {
		if width := ansi.StringWidth(line); width > 24 {
			t.Fatalf("narrow line width = %d: %q", width, line)
		}
	}
	_, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("q did not request exit")
	}
}

func TestDynamicTextSanitizesBeforeStyling(t *testing.T) {
	got := sanitizeLines([]string{"safe\x1b]8;;https://attacker.invalid\x07label\x1b]8;;\x07\u202e"})
	if want := "safelabel<U+202E>"; len(got) != 1 || got[0] != want {
		t.Fatalf("sanitizeLines() = %q, want %q", got, want)
	}
}

func TestViewportStoresClampedOffsetAcrossOverscrollAndResize(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	m.screen = detailScreen
	m.sessionsState.selected = "session-1"
	m.width, m.height = 24, 8
	m.detailState.detail = app.EventDetail{
		State: app.EvidenceComplete,
		Event: model.EventSummary{
			ID: "event-1", SessionID: "session-1", Sequence: 1, Kind: model.EventKindMessage, Summary: "long event",
		},
		Payload: model.MessageData{Role: model.MessageRoleAssistant, Text: strings.Repeat("evidence ", 200)},
	}
	m.syncViewports()
	for range 500 {
		m.moveScroll(1)
	}
	if m.detailState.viewport.PastBottom() || !m.detailState.viewport.AtBottom() {
		t.Fatalf("overscroll offset = %d, viewport was not clamped", m.detailState.viewport.YOffset())
	}
	bottom := m.detailState.viewport.YOffset()
	if bottom == 0 {
		t.Fatal("long detail did not produce a scrollable viewport")
	}
	m.moveScroll(-1)
	if got := m.detailState.viewport.YOffset(); got != bottom-1 {
		t.Fatalf("one upward input moved offset to %d, want %d", got, bottom-1)
	}

	updated, _ := updateModel(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	if updated.detailState.viewport.PastBottom() {
		t.Fatalf("resize retained out-of-range offset %d", updated.detailState.viewport.YOffset())
	}
}

func TestHelpOverlayAndBoundaryNavigation(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	m.sessionsState.loading = false
	m.sessionsState.page = app.SessionPage{Sessions: []app.SessionSummary{
		testSession("one"), testSession("two"), testSession("three"),
	}}
	m.restoreSessionSelection()

	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'G', Text: "G"})
	if m.sessionsState.cursor != 2 || m.sessionsState.selected != "three" {
		t.Fatalf("G selection = %d/%q", m.sessionsState.cursor, m.sessionsState.selected)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.sessionsState.cursor != 0 || m.sessionsState.selected != "one" {
		t.Fatalf("g selection = %d/%q", m.sessionsState.cursor, m.sessionsState.selected)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '?', Text: "?"})
	if !m.helpOpen || !strings.Contains(m.View().Content, "Keyboard help") {
		t.Fatal("help overlay did not open")
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.helpOpen || m.screen != sessionsScreen {
		t.Fatal("Esc did not dismiss help before navigating")
	}
}

func TestSessionsDashboardResponsiveLayoutsAndUnavailableMetrics(t *testing.T) {
	activity := time.Date(2026, 7, 25, 12, 30, 0, 0, time.FixedZone("test", 3*60*60))
	session := testSession("session-with-a-long-identifier")
	session.Title = ""
	session.Preview = "first user request with enough detail to identify the work"
	session.AgentName = "codex"
	session.LastActivityAt = &activity
	m := New(context.Background(), &servicesStub{})
	m.sessionsState.loading = false
	m.sessionsState.overviewLoading = false
	m.sessionsState.overview = app.LibraryOverview{Sessions: 12, Events: 345, Agents: 3, IssueSessions: 2}
	m.sessionsState.page = app.SessionPage{State: app.EvidenceComplete, Sessions: []app.SessionSummary{session}}
	m.restoreSessionSelection()

	tests := []struct {
		name   string
		width  int
		height int
		want   []string
	}{
		{name: "wide", width: 120, height: 30, want: []string{"LAST ACTIVITY ↓", "SESSION / PREVIEW", "CODEX", "2026-07-25 09:30 UTC", "┌"}},
		{name: "medium", width: 90, height: 30, want: []string{"LAST ACTIVITY ↓", "CODEX", "Sessions", "Evidence issues"}},
		{name: "narrow", width: 60, height: 30, want: []string{"Sessions 12 · Events 345 · Agents 3", "CODEX", "first user request"}},
		{name: "short", width: 120, height: 14, want: []string{"Sessions 12 · Events 345 · Agents 3", "CODEX", "first user request"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copy := m
			copy.width, copy.height = test.width, test.height
			content := ansi.Strip(copy.View().Content)
			for _, want := range test.want {
				if !strings.Contains(content, want) {
					t.Fatalf("view missing %q:\n%s", want, content)
				}
			}
			for _, line := range strings.Split(content, "\n") {
				if ansi.StringWidth(line) > test.width {
					t.Fatalf("line width %d exceeds %d: %q", ansi.StringWidth(line), test.width, line)
				}
			}
		})
	}

	m.sessionsState.overviewErr = errors.New("aggregate read failed")
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "— unavailable") || !strings.Contains(content, "first user request") {
		t.Fatalf("overview failure disturbed table or lacked explicit unavailable label:\n%s", content)
	}
}

func TestSessionsDashboardSanitizesHostileFieldsAndRestoresSelection(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	m.width, m.height = 120, 30
	m.sessionsState.loading = false
	m.sessionsState.overviewLoading = false
	m.sessionsState.selected = "safe-two"
	m.sessionsState.page = app.SessionPage{Sessions: []app.SessionSummary{
		{ID: "safe-one", Preview: "\x1b]8;;https://attacker.invalid\aopen\x1b]8;;\a", AgentName: "\x1b[31mcodex", State: app.EvidenceComplete},
		{ID: "safe-two", Title: "selected", AgentName: "claude", State: app.EvidenceComplete},
	}}
	m.restoreSessionSelection()
	if m.sessionsState.cursor != 1 {
		t.Fatalf("restored cursor = %d, want 1", m.sessionsState.cursor)
	}
	content := m.View().Content
	if strings.Contains(content, "attacker.invalid") || strings.Contains(content, "\x1b]8;") {
		t.Fatalf("hostile terminal content survived sanitization: %q", content)
	}
	if !strings.Contains(ansi.Strip(content), "> 202") && !strings.Contains(ansi.Strip(content), "> —") {
		t.Fatalf("focused row marker missing: %q", ansi.Strip(content))
	}
}

func TestSearchScreenSanitizesResults(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	m.screen = searchScreen
	m.searchState.page = app.SearchPage{
		State:        app.EvidenceComplete,
		Availability: app.SearchAvailability{State: app.EvidenceComplete, Sessions: 1, Usable: 1},
		Results: []app.SearchResult{{
			SessionID: "session", EventID: "event", Kind: model.EventKindMessage,
			Summary: "\x1b]8;;https://attacker.invalid\aopen\x1b]8;;\a",
			Snippet: "\x1b[31mhostile\x1b[0m",
		}},
	}
	content := m.View().Content
	if strings.Contains(content, "attacker.invalid") || strings.Contains(content, "\x1b]8;") || strings.Contains(content, "\x1b[31mhostile") {
		t.Fatalf("hostile search result survived terminal sanitization: %q", content)
	}
}
