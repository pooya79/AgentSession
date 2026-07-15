package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestInitialView(t *testing.T) {
	view := New().View()
	if !strings.Contains(view.Content, "AgentSession") || !strings.Contains(view.Content, "No sessions") {
		t.Fatalf("initial view = %q, want project name and empty state", view.Content)
	}
	if !view.AltScreen {
		t.Fatal("initial view does not request the alternate screen")
	}
}

func TestQuitKeys(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: 'q', Text: "q"},
		{Code: 'c', Mod: tea.ModCtrl},
	} {
		_, cmd := New().Update(key)
		if cmd == nil {
			t.Errorf("Update(%q) command = nil, want quit", key.String())
		}
	}
}

func TestResize(t *testing.T) {
	updated, cmd := New().Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if cmd != nil {
		t.Fatalf("resize command = %v, want nil", cmd)
	}
	model, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model has type %T, want Model", updated)
	}
	if model.width != 120 || model.height != 40 {
		t.Fatalf("dimensions = %dx%d, want 120x40", model.width, model.height)
	}
}

func TestTerminalViewSanitizesContent(t *testing.T) {
	view := terminalView("safe\x1b]8;;https://attacker.invalid\x07label\x1b]8;;\x07\u202e")
	if got, want := view.Content, "safelabel<U+202E>"; got != want {
		t.Fatalf("terminalView() content = %q, want %q", got, want)
	}
}
