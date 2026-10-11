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

func TestSearchResultDisplayFieldsNormalizeAndFallback(t *testing.T) {
	t.Parallel()

	result := SearchResult{
		SessionID:        "session-a",
		Title:            "  A\n session ",
		Preview:          " ignored\tpreview ",
		AgentName:        "  codex\ncli ",
		BestMatchSummary: " matching\tcommand ",
	}
	if got := SearchResultDisplayTitle(result); got != "A session" {
		t.Fatalf("display title = %q", got)
	}
	if got := SearchResultDisplayPreview(result); got != "ignored preview" {
		t.Fatalf("display preview = %q", got)
	}
	if got := SearchResultAgentLabel(result); got != "codex cli" {
		t.Fatalf("agent label = %q", got)
	}
	if got := SearchResultMatchSummary(result); got != "matching command" {
		t.Fatalf("match summary = %q", got)
	}

	result.Title, result.Preview, result.AgentName, result.BestMatchSummary = " \n", "\t", "", ""
	if got := SearchResultDisplayTitle(result); got != "session-a" {
		t.Fatalf("fallback display title = %q", got)
	}
	if got := SearchResultAgentLabel(result); got != "AGENT UNREPORTED" {
		t.Fatalf("fallback agent label = %q", got)
	}
	if got := SearchResultMatchSummary(result); got != "Matching evidence" {
		t.Fatalf("fallback match summary = %q", got)
	}
}
