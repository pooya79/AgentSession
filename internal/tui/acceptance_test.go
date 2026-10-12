package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/discovery"
)

// Drive the combined browse/read/inspect/return journey through production
// services. Focused tests cover the delayed/failing response permutations.
func TestCompleteTUIJourneyWithTemporaryIndex(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "rollout.jsonl")
	contents, err := os.ReadFile("../../docs/design/tui-89/fixtures/rollout-retry.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, contents, 0600); err != nil {
		t.Fatal(err)
	}
	inputs := discovery.Inputs{FileSystem: discovery.OSFileSystem{}, HomeDir: root, WorkingDir: root, GOOS: "linux",
		ExplicitPaths: []discovery.ConfiguredPath{{Kind: discovery.SourceCodex, Path: source}}}
	paths := app.PathInputs{GOOS: "linux", HomeDir: root, WorkingDir: root}
	service, err := app.OpenRuntime(t.Context(), app.RuntimeConfig{
		DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "config"), PathInputs: &paths, DiscoveryInputs: &inputs,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if _, err := service.DiscoverAndImport(t.Context()); err != nil {
		t.Fatal(err)
	}
	m := New(t.Context(), service)
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
		// Animation and periodic observation have separate lifecycle tests.
		switch message.(type) {
		case spinner.TickMsg, pollImportMsg, pollProjectionsMsg:
			return
		}
		var next tea.Cmd
		m, next = updateModel(t, m, message)
		drain(next)
	}
	send := func(msg tea.Msg) {
		t.Helper()
		var cmd tea.Cmd
		m, cmd = updateModel(t, m, msg)
		drain(cmd)
	}
	key := func(code rune, text string) { send(tea.KeyPressMsg{Code: code, Text: text}) }
	want := func(parts ...string) {
		t.Helper()
		view := m.View()
		if view.MouseMode != tea.MouseModeNone {
			t.Fatal("mouse capture prevents normal terminal selection")
		}
		for _, part := range parts {
			if !strings.Contains(view.Content, part) {
				t.Fatalf("missing %q in journey frame:\n%s", part, view.Content)
			}
		}
	}
	send(tea.WindowSizeMsg{Width: 120, Height: 32})
	key('r', "r")
	want("Sessions", "PREVIEW", "Fix request timeout")
	key(tea.KeyEnter, "")
	send(tea.WindowSizeMsg{Width: 80, Height: 24})
	key(']', "]")
	key(tea.KeyPgDown, "")
	want("10. Recorded evidence")
	key('?', "?")
	want("Help")
	key(tea.KeyEscape, "")
	want("10. Recorded evidence")
	key(']', "]")
	key(']', "]")
	key(tea.KeyEnter, "")
	want("RECORDED FAILURE", "TestRetryTimeout")
	key('d', "d")
	want("Event", "TestRetryTimeout")
	key('w', "w")
	for range 16 {
		key(tea.KeyRight, "")
	}
	want("timeout=3000ms")
	key(tea.KeyEscape, "")
	want("RECORDED FAILURE", "TestRetryTimeout")
	key('/', "/")
	send(tea.PasteMsg{Content: "go tst"})
	key(tea.KeyLeft, "")
	key(tea.KeyLeft, "")
	key('e', "e")
	key(tea.KeyEnter, "")
	want("Query: go test", "matching event")
	key(tea.KeyEnter, "")
	want("Timeline")
	key(tea.KeyEscape, "")
	want("Query: go test")
	key(tea.KeyEscape, "")
	want("RECORDED FAILURE", "TestRetryTimeout")
	key('f', "f")
	want("Assessments", "unresolved_failed_verification")
	key(tea.KeyEnter, "")
	want("Timeline")
	key(tea.KeyEscape, "")
	want("Assessments")
	key(tea.KeyEscape, "")
	want("RECORDED FAILURE", "TestRetryTimeout")
	for range 3 {
		key(']', "]")
	}
	key('d', "d")
	want("Press u")
	key('u', "u")
	key('G', "G")
	want("raw stays hidden", "redactions")
	key(tea.KeyEscape, "")
	key('x', "x")
	want("Projections", "search")
	key(tea.KeyEscape, "")
	want("Timeline")
	key('g', "g")
	for _, size := range []tea.WindowSizeMsg{{Width: 54, Height: 18}, {Width: 32, Height: 10}, {Width: 80, Height: 24}} {
		send(size)
		if size.Width < 40 {
			want("Resize to at least 40×12")
		} else {
			want("Fix request timeout")
		}
	}
	key('r', "r")
	want("Fix request timeout")
	key(tea.KeyEscape, "")
	want("Sessions", "Fix request timeout")
	key('i', "i")
	want("Indexing")
	key(tea.KeyEscape, "")
	want("Sessions", "Fix request timeout")
	after, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(after, contents) {
		t.Fatalf("inspection modified source: %v", err)
	}
}
