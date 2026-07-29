package app

import "github.com/pooya79/AgentSession/internal/model"

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
