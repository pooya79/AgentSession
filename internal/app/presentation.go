package app

import (
	"strings"

	"github.com/pooya79/AgentSession/internal/model"
)

// SummaryCategoryTitle returns the shared presentation label for recorded
// summary evidence. Unknown categories remain distinguishable from context.
func SummaryCategoryTitle(category model.SummaryCategory) string {
	switch category {
	case model.SummaryCategoryReasoning:
		return "Reasoning"
	case model.SummaryCategoryContext:
		return "Conversation context"
	case model.SummaryCategoryPlan:
		return "Plan update"
	case model.SummaryCategorySummary:
		return "Session summary"
	default:
		return "Recorded summary"
	}
}

// SearchResultDisplayTitle derives one normalized session label for every
// presentation layer.
func SearchResultDisplayTitle(result SearchResult) string {
	for _, candidate := range []string{result.Title, result.Preview, string(result.SessionID)} {
		if value := normalizeDisplayText(candidate); value != "" {
			return value
		}
	}
	return "Untitled session"
}

// SearchResultDisplayPreview normalizes a result preview for display and title
// comparison.
func SearchResultDisplayPreview(result SearchResult) string {
	return normalizeDisplayText(result.Preview)
}

// SearchResultAgentLabel supplies the shared fallback for absent agent metadata.
func SearchResultAgentLabel(result SearchResult) string {
	if value := normalizeDisplayText(result.AgentName); value != "" {
		return value
	}
	return "AGENT UNREPORTED"
}

// SearchResultMatchSummary supplies the shared fallback for absent match
// summaries.
func SearchResultMatchSummary(result SearchResult) string {
	if value := normalizeDisplayText(result.BestMatchSummary); value != "" {
		return value
	}
	return "Matching evidence"
}

func normalizeDisplayText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
