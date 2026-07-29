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
	lines := []string{
		"Search canonical evidence",
		prompt,
		"Filters · session: kind: after: before: file: tool: command:",
	}
	if m.searchState.loading {
		return append(lines, "", m.spinner.View()+" Searching…")
	}
	if m.searchState.err != nil {
		return append(lines, "", "Search could not be completed: "+m.searchState.err.Error())
	}
	lines = append(lines, searchAvailabilityLine(m.searchState.page))
	if len(m.searchState.page.Results) == 0 {
		return append(lines, "", "No matching sessions.")
	}

	lines = append(lines, "")
	compact := m.renderWidth() < 72 || m.height > 0 && m.height < 18
	cards := make([][]string, len(m.searchState.page.Results))
	for index, result := range m.searchState.page.Results {
		cards[index] = searchResultCard(result, index == m.searchState.cursor, compact, m.renderWidth())
	}

	available := max(1, m.contentHeight()-len(lines)-1)
	start, end := searchCardWindow(cards, m.searchState.cursor, available)
	remaining := available
	for index := start; index < end && remaining > 0; index++ {
		card := cards[index]
		if len(card) > remaining {
			card = card[:remaining]
		}
		lines = append(lines, card...)
		remaining -= len(card)
	}
	lines = append(lines, searchResultFooter(
		m.searchState.cursor,
		len(m.searchState.page.Results),
		m.searchState.page.PreviousCursor != "",
		m.searchState.page.NextCursor != "",
	))
	return lines
}

func searchAvailabilityLine(page app.SearchPage) string {
	availability := page.Availability
	switch page.State {
	case app.EvidenceUnavailable:
		return "Unavailable · no imported session has a current ready search index"
	case app.EvidencePartial:
		return fmt.Sprintf("Partial · %d/%d sessions searchable · %d unavailable",
			availability.Usable, availability.Sessions, max(int64(0), availability.Sessions-availability.Usable))
	default:
		if availability.Sessions == 0 {
			return "Complete · import sessions to create searchable evidence"
		}
		return fmt.Sprintf("Complete · %d sessions searchable", availability.Usable)
	}
}

func searchResultCard(result app.SearchResult, selected, compact bool, width int) []string {
	marker := "  "
	if selected {
		marker = "> "
	}
	title := searchResultTitle(result)
	agent := strings.ToUpper(searchInline(result.AgentName))
	if agent == "" {
		agent = "AGENT UNREPORTED"
	}
	metadata := fmt.Sprintf("%s · %s · %s / %s",
		formatActivity(result.LastActivityAt), agent,
		searchCount(result.MatchCount, "matching event"),
		searchCount(result.EventCount, "event"))

	summary := searchInline(result.BestMatchSummary)
	snippet := searchInline(result.Snippet)
	if summary == "" {
		summary = "Matching evidence"
	}
	if compact {
		match := summary
		if snippet != "" && snippet != summary {
			match += " — " + snippet
		}
		return []string{
			marker + truncateCell(title, max(1, width-2)),
			"  " + truncateCell(metadata, max(1, width-2)),
			"  Match · " + truncateCell(match, max(1, width-10)),
		}
	}

	lines := []string{
		marker + truncateCell(title, max(1, width-2)),
		"  " + truncateCell(metadata, max(1, width-2)),
	}
	preview := searchInline(result.Preview)
	if preview != "" && preview != title {
		lines = append(lines, "  Session · "+truncateCell(preview, max(1, width-12)))
	}
	lines = append(lines, "  Match · "+truncateCell(summary, max(1, width-10)))
	if snippet != "" && snippet != summary {
		lines = append(lines, "  "+truncateCell(snippet, max(1, width-2)))
	}
	return lines
}

func searchResultTitle(result app.SearchResult) string {
	for _, candidate := range []string{result.Title, result.Preview, string(result.SessionID)} {
		if value := searchInline(candidate); value != "" {
			return value
		}
	}
	return "Untitled session"
}

func searchInline(value string) string {
	value = sanitization.Terminal(strings.ToValidUTF8(value, "\uFFFD"))
	return strings.Join(strings.Fields(value), " ")
}

// searchCardWindow returns a height-bounded, selection-centered card range.
// Individual cards remain indivisible unless the selected card alone is taller
// than the available terminal rows.
func searchCardWindow(cards [][]string, selected, available int) (int, int) {
	if len(cards) == 0 {
		return 0, 0
	}
	selected = clamp(selected, 0, len(cards)-1)
	available = max(1, available)
	start, end := selected, selected+1
	used := len(cards[selected])
	for used < available {
		forwardDistance := end - selected
		backwardDistance := selected - start
		forwardFirst := start == 0 || forwardDistance <= backwardDistance
		added := false
		if forwardFirst {
			if end < len(cards) && used+len(cards[end]) <= available {
				used += len(cards[end])
				end++
				added = true
			} else if start > 0 && used+len(cards[start-1]) <= available {
				start--
				used += len(cards[start])
				added = true
			}
		} else {
			if start > 0 && used+len(cards[start-1]) <= available {
				start--
				used += len(cards[start])
				added = true
			} else if end < len(cards) && used+len(cards[end]) <= available {
				used += len(cards[end])
				end++
				added = true
			}
		}
		if !added {
			break
		}
	}
	return start, end
}

func searchResultFooter(selected, total int, previous, next bool) string {
	position := clamp(selected, 0, max(0, total-1)) + 1
	parts := []string{fmt.Sprintf("Result %d/%d", position, total)}
	if previous {
		parts = append(parts, "previous page")
	}
	if next {
		parts = append(parts, "next page")
	}
	return strings.Join(parts, " · ")
}

func searchCount(count int64, noun string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}
