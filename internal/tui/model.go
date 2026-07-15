package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

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

	view := tea.NewView(content)
	view.AltScreen = true
	return view
}

// Run opens the interactive terminal interface.
func Run() error {
	_, err := tea.NewProgram(New()).Run()
	return err
}
