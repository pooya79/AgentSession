package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestSearchEditsAtCursorAndKeepsShortcutsAsText(t *testing.T) {
	services := &servicesStub{}
	m := New(context.Background(), services)
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	for _, key := range []tea.KeyPressMsg{
		{Text: "go tst", Code: 'g'}, {Code: tea.KeyLeft}, {Code: tea.KeyLeft},
		{Text: "e", Code: 'e'}, {Code: tea.KeyHome}, {Text: "X", Code: 'X'},
		{Code: tea.KeyBackspace}, {Code: tea.KeyDelete}, {Text: "g", Code: 'g'},
		{Code: tea.KeyEnd}, {Text: " q/?[]rix", Code: ' '},
	} {
		m, _ = updateModel(t, m, key)
	}
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "Query: go test q/?[]rix█") {
		t.Fatalf("cursor editing or shortcut ownership failed:\n%s", got)
	}
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	applySearchCommand(t, &m, cmd)
	if len(services.searchCalls) != 1 || services.searchCalls[0].Query != "go test q/?[]rix" {
		t.Fatalf("shared search request = %+v", services.searchCalls)
	}
}

func TestSearchPasteIsTextAndCursorStaysVisible(t *testing.T) {
	services := &servicesStub{}
	m := New(context.Background(), services)
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	m, _ = updateModel(t, m, tea.PasteMsg{Content: "界 café q/?[]rix\r\nkind:command\x1b]8;;https://hostile.invalid\a\x1b]8;;\a"})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyDelete})
	m, _ = updateModel(t, m, tea.PasteMsg{Content: " "})
	if len(services.searchCalls) != 0 || m.screen != searchScreen || m.helpOpen {
		t.Fatal("paste invoked a feature shortcut")
	}
	for _, size := range [][2]int{{120, 32}, {80, 24}, {54, 18}, {40, 12}} {
		m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
		m, _ = updateModel(t, m, tea.PasteMsg{Content: strings.Repeat("界", 80) + "TAIL"})
		view := m.View()
		if !strings.Contains(ansi.Strip(view.Content), "TAIL█") || strings.Contains(view.Content, "hostile.invalid") {
			t.Fatalf("unsafe or inaccessible query at %v:\n%s", size, view.Content)
		}
		if view.MouseMode != tea.MouseModeNone {
			t.Fatal("search captured the mouse")
		}
		for _, line := range strings.Split(ansi.Strip(view.Content), "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("query exceeds terminal width: %q", line)
			}
		}
	}
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	applySearchCommand(t, &m, cmd)
	if len(services.searchCalls) != 1 || !strings.HasPrefix(services.searchCalls[0].Query, "界 café q/?[]rix kind:command") {
		t.Fatalf("paste was changed or treated as shortcuts: %+v", services.searchCalls)
	}
}

func TestSearchEditedQueryCannotOpenOrPageOldResults(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	m.screen = searchScreen
	m.searchState.page = app.SearchPage{State: app.EvidenceComplete, Results: []app.SearchResult{{SessionID: "old", Title: "OLD RESULT"}}, NextCursor: "old-next"}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	m, _ = updateModel(t, m, tea.PasteMsg{Content: "new query"})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := m.View().Content; strings.Contains(got, "OLD RESULT") || !strings.Contains(got, "Query changed") {
		t.Fatalf("old results presented as current:\n%s", got)
	}
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cmd != nil {
		t.Fatal("edited query used old pagination")
	}
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.screen != searchScreen || cmd == nil || !strings.Contains(m.View().Content, "Searching") {
		t.Fatal("Enter did not submit the edited query")
	}
}

func TestSearchCanceledResponseExplainsStateAndRetries(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, errors.New("index read failed")} {
		t.Run(failure.Error(), func(t *testing.T) {
			m := New(context.Background(), &servicesStub{})
			m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
			m, _ = updateModel(t, m, tea.PasteMsg{Content: "kind:command q"})
			m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			m, _ = updateModel(t, m, searchLoadedMsg{generation: m.requestGeneration, err: failure})
			got := m.View().Content
			if strings.Contains(got, "No matching sessions") || strings.Contains(got, "Searching…") || !strings.Contains(got, "retry") {
				t.Fatalf("failure collapsed into empty or stuck search:\n%s", got)
			}
			m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
			applySearchCommand(t, &m, cmd)
			if strings.Contains(m.View().Content, "Searching…") || !strings.Contains(m.View().Content, "No matching sessions") {
				t.Fatalf("retry failed:\n%s", m.View().Content)
			}
		})
	}
}

func applySearchCommand(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			applySearchCommand(t, m, child)
		}
		return
	}
	// Spinner scheduling is independent of the service response under test.
	if _, ok := msg.(searchLoadedMsg); ok {
		*m, _ = updateModel(t, *m, msg)
	}
}

type searchResponseServices struct {
	servicesStub
	respond func(context.Context, app.SearchRequest) (app.SearchPage, error)
}

func (s *searchResponseServices) Search(ctx context.Context, req app.SearchRequest) (app.SearchPage, error) {
	return s.respond(ctx, req)
}

func searchReply(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("missing search command")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			if reply := searchReply(t, child); reply != nil {
				return reply
			}
		}
	}
	if _, ok := msg.(searchLoadedMsg); ok {
		return msg
	}
	return nil
}

func TestSearchSupersededRepliesCannotReplaceCurrentResults(t *testing.T) {
	var requests []app.SearchRequest
	var contexts []context.Context
	services := &searchResponseServices{respond: func(ctx context.Context, req app.SearchRequest) (app.SearchPage, error) {
		requests = append(requests, req)
		contexts = append(contexts, ctx)
		return app.SearchPage{State: app.EvidenceComplete, Results: []app.SearchResult{{SessionID: model.SessionID(req.Query), Title: req.Query}}, NextCursor: "next", PreviousCursor: "previous"}, nil
	}}
	m := New(context.Background(), services)
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	m, _ = updateModel(t, m, tea.PasteMsg{Content: "OLD QUERY"})
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	oldReply := searchReply(t, cmd) // Delay delivery, including after cancellation.
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	if contexts[0].Err() != context.Canceled {
		t.Fatal("editing did not cancel the pending search")
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := m.View().Content; !strings.Contains(got, "Search canceled") || strings.Contains(got, "No matching sessions") {
		t.Fatalf("cancellation was not explained:\n%s", got)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	for range len("OLD QUERY") {
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	m, _ = updateModel(t, m, tea.PasteMsg{Content: "CURRENT QUERY"})
	m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = updateModel(t, m, oldReply)
	if got := m.View().Content; !strings.Contains(got, "Searching") || strings.Contains(got, "OLD QUERY") {
		t.Fatalf("late reply replaced pending search:\n%s", got)
	}
	m, _ = updateModel(t, m, searchReply(t, cmd))
	m, _ = updateModel(t, m, oldReply)
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "> CURRENT QUERY") || strings.Contains(got, "OLD QUERY") {
		t.Fatalf("late reply replaced current results:\n%s", got)
	}
	for _, key := range []rune{'n', 'p'} {
		m, cmd = updateModel(t, m, tea.KeyPressMsg{Code: key, Text: string(key)})
		m, _ = updateModel(t, m, searchReply(t, cmd))
	}
	if len(requests) != 4 || requests[2].Cursor != "next" || requests[3].Cursor != "previous" || requests[3].Query != "CURRENT QUERY" || requests[3].Limit != app.DefaultPageSize {
		t.Fatalf("shared-service paging changed: %+v", requests)
	}
	// Paste outside the editor is ignored rather than routed as shortcuts.
	m, _ = updateModel(t, m, tea.PasteMsg{Content: "q?[/rix"})
	if m.screen != searchScreen || m.helpOpen || m.searchState.editing {
		t.Fatal("paste outside editor activated shortcuts")
	}
}

func TestSearchResultReturnRestoresQueryAndSelection(t *testing.T) {
	services := &servicesStub{timeline: app.TimelinePage{State: app.EvidenceNotFound}}
	m := New(context.Background(), services)
	m.screen = searchScreen
	m.searchState.query = "kind:command"
	m.searchState.page = app.SearchPage{State: app.EvidenceComplete, Results: []app.SearchResult{{SessionID: "first", Title: "FIRST"}, {SessionID: "removed", Title: "REMOVED"}}}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	// Production timeline command is returned directly once the spinner is active.
	var deliver func(tea.Cmd)
	deliver = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, child := range batch {
				deliver(child)
			}
			return
		}
		if _, ok := msg.(timelineLoadedMsg); ok {
			m, _ = updateModel(t, m, msg)
		}
	}
	deliver(cmd)
	if got := m.View().Content; !strings.Contains(got, "no longer available") {
		t.Fatalf("missing result was not explained:\n%s", got)
	}
	m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: 54, Height: 18})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "Query: kind:command") || !strings.Contains(got, "> REMOVED") || !strings.Contains(got, "Result 2/2") {
		t.Fatalf("return lost search context:\n%s", got)
	}
	if len(services.timelineCalls) != 1 || services.timelineCalls[0].SessionID != "removed" {
		t.Fatalf("opened wrong session: %+v", services.timelineCalls)
	}
}

func TestSearchCoverageAndFocusAtSupportedSizes(t *testing.T) {
	for _, state := range []app.EvidenceState{app.EvidenceComplete, app.EvidencePartial, app.EvidenceUnavailable} {
		for _, size := range [][2]int{{120, 32}, {80, 24}, {54, 18}, {40, 12}} {
			for _, dark := range []bool{true, false} {
				m := New(context.Background(), &servicesStub{})
				m.screen = searchScreen
				m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				m.theme = newTheme(dark)
				m, _ = updateModel(t, m, searchLoadedMsg{generation: m.requestGeneration, page: app.SearchPage{State: state, Availability: app.SearchAvailability{Sessions: 4, Usable: 2}}})
				got := ansi.Strip(m.View().Content)
				if !strings.Contains(got, "[FOCUS] Results") {
					t.Fatalf("focus missing at %v:\n%s", size, got)
				}
				if state == app.EvidenceUnavailable && (strings.Contains(got, "No matching sessions") || !strings.Contains(got, "Unavailable")) {
					t.Fatalf("unavailable search claimed no matches at %v:\n%s", size, got)
				}
				if state == app.EvidencePartial && !strings.Contains(got, "Partial") {
					t.Fatalf("partial search lost coverage at %v:\n%s", size, got)
				}
			}
		}
	}
}

func TestSearchRefreshRetainsEvidenceAndReconcilesSelection(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	m.screen = searchScreen
	m.searchState.page = app.SearchPage{State: app.EvidenceComplete, Results: []app.SearchResult{{SessionID: "first", Title: "FIRST"}, {SessionID: "selected", Title: "SELECTED"}}}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "Searching") || !strings.Contains(got, "Previous results") || !strings.Contains(got, "> SELECTED") {
		t.Fatalf("refresh discarded evidence or presented it as current:\n%s", got)
	}
	m, _ = updateModel(t, m, searchLoadedMsg{generation: m.requestGeneration, err: errors.New("read failed")})
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "read failed") || !strings.Contains(got, "> SELECTED") {
		t.Fatalf("failed refresh discarded evidence:\n%s", got)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	m, _ = updateModel(t, m, searchLoadedMsg{generation: m.requestGeneration, page: app.SearchPage{State: app.EvidenceComplete, Results: []app.SearchResult{{SessionID: "selected", Title: "SELECTED"}, {SessionID: "first", Title: "FIRST"}}}})
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "> SELECTED") || !strings.Contains(got, "Result 1/2") {
		t.Fatalf("refresh lost stable selection:\n%s", got)
	}
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	m, _ = updateModel(t, m, searchLoadedMsg{generation: m.requestGeneration, page: app.SearchPage{State: app.EvidenceComplete, Results: []app.SearchResult{{SessionID: "first", Title: "FIRST"}}}})
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "Selected result changed or removed") || !strings.Contains(got, "> FIRST") {
		t.Fatalf("removed selection was silently replaced:\n%s", got)
	}
}

func TestSearchCompactRefreshKeepsFocusAndSelectedResult(t *testing.T) {
	for _, failure := range []error{nil, context.Canceled, errors.New("index read failed")} {
		m := New(context.Background(), &servicesStub{})
		m.screen = searchScreen
		m, _ = updateModel(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})
		m.searchState.page = app.SearchPage{State: app.EvidenceComplete, Results: []app.SearchResult{{SessionID: "selected", Title: "SELECTED"}}}
		m, _ = updateModel(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
		if failure != nil {
			m, _ = updateModel(t, m, searchLoadedMsg{generation: m.requestGeneration, err: failure})
		}
		got := ansi.Strip(m.View().Content)
		for _, want := range []string{"[FOCUS] Results", "> SELECTED", "Result 1/1", "Previous results"} {
			if !strings.Contains(got, want) {
				t.Fatalf("compact refresh %v lost %q:\n%s", failure, want, got)
			}
		}
	}
}

func TestSearchInitialEmptyQueryCanSubmitAfterLeavingEditing(t *testing.T) {
	services := &servicesStub{}
	m := New(context.Background(), services)
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	applySearchCommand(t, &m, cmd)
	if len(services.searchCalls) != 1 || services.searchCalls[0].Query != "" {
		t.Fatalf("Enter did not submit the advertised empty query: %+v", services.searchCalls)
	}
}
