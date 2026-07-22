package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/sanitization"
)

// ImportProgressMsg delivers one shared application progress snapshot.
type ImportProgressMsg struct{ Progress app.ImportProgress }

// ImportProgressClosedMsg reports that a subscription reached its terminal
// state or was independently detached.
type ImportProgressClosedMsg struct{}

// WaitForImportProgress adapts an application subscription to Bubble Tea. It
// observes shared work and contains no import orchestration.
func WaitForImportProgress(subscription *app.ImportSubscription) tea.Cmd {
	return func() tea.Msg {
		progress, ok := <-subscription.Updates()
		if !ok {
			return ImportProgressClosedMsg{}
		}
		return ImportProgressMsg{Progress: progress}
	}
}

// Model is the initial AgentSession terminal interface.
type Model struct {
	width  int
	height int
}

// New creates the initial terminal model.
func New() Model {
	return Model{}
}

// Init satisfies tea.Model.
func (Model) Init() tea.Cmd {
	return nil
}

// Update handles terminal resize and exit events.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}
	return m, nil
}

// View renders the current terminal state.
func (m Model) View() tea.View {
	content := strings.Join([]string{
		"AgentSession",
		"",
		"No sessions have been indexed yet.",
		"Discovery and importing are coming next.",
		"",
		"Press q to quit.",
	}, "\n")

	if m.width > 0 && m.height > 0 {
		content = fmt.Sprintf("%s\n\nTerminal: %dx%d", content, m.width, m.height)
	}

	view := terminalView(content)
	view.AltScreen = true
	return view
}

// terminalView is the mandatory sanitization boundary for TUI content.
func terminalView(content string) tea.View {
	return tea.NewView(sanitization.Terminal(content))
}

// Run opens the interactive terminal interface.
func Run() error {
	_, err := tea.NewProgram(New()).Run()
	return err
}
