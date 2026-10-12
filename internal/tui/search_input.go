package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pooya79/AgentSession/internal/sanitization"
)

// editSearch owns key input before any feature shortcuts are considered.
func (m Model) editSearch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	runes := []rune(m.searchState.query)
	pos := clamp(m.searchState.queryCursor, 0, len(runes))
	switch msg.String() {
	case "ctrl+c":
		m.cancelRequest()
		m.stopObservation()
		m.stopProjectionObservation()
		return m, tea.Quit
	case "esc":
		m.searchState.editing = false
	case "enter":
		m.searchState.editing = false
		return m, m.runSearch("")
	case "left":
		pos = max(0, pos-1)
	case "right":
		pos = min(len(runes), pos+1)
	case "home", "ctrl+a":
		pos = 0
	case "end", "ctrl+e":
		pos = len(runes)
	case "backspace", "ctrl+h":
		if pos > 0 {
			runes = append(runes[:pos-1], runes[pos:]...)
			pos--
		}
	case "delete", "ctrl+d":
		if pos < len(runes) {
			runes = append(runes[:pos], runes[pos+1:]...)
		}
	default:
		m.insertSearchText(msg.Text)
		return m, nil
	}
	if query := string(runes); query != m.searchState.query {
		m.searchState.query = query
		m.searchState.dirty = true
	}
	m.searchState.queryCursor = pos
	return m, nil
}

func (m *Model) insertSearchText(text string) {
	// Bracketed paste remains one input message, including shortcut characters.
	// The query is a single line; normalize pasted whitespace without parsing it.
	text = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(text)
	text = sanitization.Terminal(strings.ToValidUTF8(text, "\uFFFD"))
	if text == "" {
		return
	}
	runes := []rune(m.searchState.query)
	pos := clamp(m.searchState.queryCursor, 0, len(runes))
	m.searchState.query = string(runes[:pos]) + text + string(runes[pos:])
	m.searchState.queryCursor = pos + len([]rune(text))
	m.searchState.dirty = true
}

// searchPrompt scrolls the single-line editor by display cells so the cursor
// remains visible even for long queries, Unicode, and compact terminals.
func (m Model) searchPrompt() string {
	if !m.searchState.editing {
		return "Query: " + m.searchState.query
	}
	runes := []rune(m.searchState.query)
	pos := clamp(m.searchState.queryCursor, 0, len(runes))
	width := max(1, m.renderWidth()-8)
	left := string(runes[:pos])
	if cells := ansi.StringWidth(left); cells >= width {
		// Reserve cells for the hidden-prefix marker and the cursor.
		left = "…" + ansi.TruncateLeft(left, cells-(width-2), "")
	}
	return "Query: " + left + "█" + ansi.Truncate(string(runes[pos:]), max(0, width-ansi.StringWidth(left)-1), "")
}
