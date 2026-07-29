package app

import (
	"testing"

	"github.com/pooya79/AgentSession/internal/model"
)

func TestSummaryCategoryTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		category model.SummaryCategory
		want     string
	}{
		{model.SummaryCategoryReasoning, "Reasoning"},
		{model.SummaryCategoryContext, "Conversation context"},
		{model.SummaryCategoryPlan, "Plan update"},
		{model.SummaryCategorySummary, "Session summary"},
		{model.SummaryCategory("future"), "Recorded summary"},
	}
	for _, test := range tests {
		if got := SummaryCategoryTitle(test.category); got != test.want {
			t.Errorf("SummaryCategoryTitle(%q) = %q, want %q", test.category, got, test.want)
		}
	}
}
