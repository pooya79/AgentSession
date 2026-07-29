package search

import (
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/model"
)

func TestDocumentFromEventIndexesOnlyEligibleCanonicalFields(t *testing.T) {
	t.Parallel()
	event := model.Event{
		ID: "event", SessionID: "session", Sequence: 1, Kind: model.EventKindCommand,
		Summary:        strings.Repeat("é", 1500),
		SearchableText: "raw-searchable-sentinel",
		Data: model.CommandData{
			Command: strings.Repeat("oversized-command-sentinel", 1024),
			Output:  "eligible-output",
		},
	}
	document := documentFromEvent(event, "1", 2)
	if len(document.Summary) > 2*1024 || !strings.Contains(document.Content, "eligible-output") {
		t.Fatalf("document bounds/content = %#v", document)
	}
	for _, excluded := range []string{"oversized-command-sentinel", "raw-searchable-sentinel"} {
		if strings.Contains(document.Content, excluded) || strings.Contains(document.CommandText, excluded) {
			t.Fatalf("excluded sentinel %q was indexed", excluded)
		}
	}

	event.Kind = model.EventKindToolResult
	event.Data = model.ToolResultData{ToolName: "shell", Output: "before\x00nul-sentinel"}
	document = documentFromEvent(event, "1", 2)
	if strings.Contains(document.Content, "nul-sentinel") {
		t.Fatal("NUL-bearing field was partially indexed")
	}
}
