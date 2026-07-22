package tui

import (
	"context"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/importer"
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

func TestWaitForImportProgressAdaptsApplicationSubscription(t *testing.T) {
	release := make(chan struct{})
	manager, err := app.NewImportManager(func(_ context.Context, source importer.Source, observe importer.ProgressObserver) ([]importer.ImportResult, error) {
		<-release
		observe(importer.Progress{SourceID: source.ID, ActiveSourceID: source.ID, Phase: importer.PhaseImporting, RecordsProcessed: 7})
		return nil, nil
	}, app.ImportManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	source := importer.Source{ID: "tui-source", Open: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("")), nil
	}}
	subscription, _, err := manager.Request(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := WaitForImportProgress(subscription)().(ImportProgressMsg); !ok {
		t.Fatal("WaitForImportProgress() did not return ImportProgressMsg")
	}
	close(release)
	for {
		msg := WaitForImportProgress(subscription)()
		if closed, ok := msg.(ImportProgressClosedMsg); ok {
			_ = closed
			break
		}
		progress, ok := msg.(ImportProgressMsg)
		if !ok {
			t.Fatalf("progress message type = %T", msg)
		}
		if progress.Progress.Complete && progress.Progress.RecordsProcessed != 7 {
			t.Fatalf("terminal progress = %#v", progress.Progress)
		}
	}
}
