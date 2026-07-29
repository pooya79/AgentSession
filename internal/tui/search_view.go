package tui

import (
	"fmt"
	"strings"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/sanitization"
)

// searchLines renders the current query, projection coverage, and bounded results.
func (m Model) searchLines() []string {
	prompt := "Query: " + m.searchState.query
	if m.searchState.editing {
		prompt += "█"
	}
	lines := []string{"Search canonical evidence", prompt}
	if m.searchState.loading {
		return append(lines, "", m.spinner.View()+" Searching…")
	}
	if m.searchState.err != nil {
		return append(lines, "", "Search could not be completed: "+m.searchState.err.Error())
	}
	availability := m.searchState.page.Availability
	switch m.searchState.page.State {
	case app.EvidenceUnavailable:
		lines = append(lines, "Unavailable · no session has a current ready search index")
	case app.EvidencePartial:
		lines = append(lines, fmt.Sprintf("Partial · %d/%d sessions searchable", availability.Usable, availability.Sessions))
	default:
		lines = append(lines, fmt.Sprintf("Complete · %d sessions searchable", availability.Usable))
	}
	if len(m.searchState.page.Results) == 0 {
		return append(lines, "", "No matching evidence.")
	}
	lines = append(lines, "")
	for index, result := range m.searchState.page.Results {
		prefix := "  "
		if index == m.searchState.cursor {
			prefix = "> "
		}
		snippet := strings.Join(strings.Fields(sanitization.Terminal(result.Snippet)), " ")
		lines = append(lines,
			fmt.Sprintf("%s%s · %s · sequence %d", prefix, result.Kind, result.SessionID, result.Sequence),
			"    "+sanitization.Terminal(result.Summary),
			"    "+snippet,
		)
	}
	return lines
}
