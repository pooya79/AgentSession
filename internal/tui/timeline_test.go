package tui

import (
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestTimelineCardRendererCoversCanonicalKindsAndSanitizes(t *testing.T) {
	trueValue := true
	exit := 1
	line := int64(3)
	count := int64(0)
	hostile := "safe\x1b]8;;https://attacker.invalid\x07link\x1b]8;;\x07\u202e"
	tests := []struct {
		name    string
		kind    model.EventKind
		payload model.NormalizedData
		want    []string
	}{
		{"message", model.EventKindMessage, model.MessageData{Role: model.MessageRoleAssistant, Text: hostile}, []string{"╭─ Assistant", "<U+202E>"}},
		{"summary", model.EventKindSummary, model.SummaryData{Text: "recorded summary"}, []string{"Summary: recorded summary"}},
		{"tool call valid json", model.EventKindToolCall, model.ToolCallData{ToolName: "read", CallID: "call", Input: `{"path":"x"}`}, []string{"Tool: read", `"path": "x"`}},
		{"tool call invalid json", model.EventKindToolCall, model.ToolCallData{Input: "{invalid"}, []string{"Input: {invalid"}},
		{"tool result", model.EventKindToolResult, model.ToolResultData{ToolName: "read", IsError: &trueValue, Output: "result"}, []string{"Error: true", "Output: result"}},
		{"command", model.EventKindCommand, model.CommandData{Command: "go test", ExitCode: &exit, Output: "failed"}, []string{"Command: go test", "Exit status: 1"}},
		{"patch", model.EventKindPatch, model.PatchData{Paths: []string{"a.go"}, Text: "-old\n+new"}, []string{"Paths: a.go", "-old", "+new"}},
		{"file read", model.EventKindFileRead, model.FileReadData{Path: "a.go", StartLine: &line}, []string{"Path: a.go", "Lines: from 3"}},
		{"file mutation", model.EventKindFileMutation, model.FileMutationData{Operation: model.FileMutationRename, Path: "b.go", PreviousPath: "a.go"}, []string{"Operation: rename", "Renamed from: a.go"}},
		{"usage", model.EventKindUsage, model.UsageData{InputTokens: &count}, []string{"input 0", "output unreported"}},
		{"error", model.EventKindError, model.ErrorData{Code: "E_TEST", Message: "broken"}, []string{"Code: E_TEST", "Error: broken"}},
		{"unknown", model.EventKindUnknown, model.UnknownData{Reason: model.UnknownUnsupportedRecordKind, OriginalKind: "future"}, []string{"Reason: unsupported_record_kind", "Press u"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := model.EventSummary{ID: "event", Sequence: 2, Kind: test.kind}
			rendered := strings.Join(timelineCardLines(event, test.payload, 80, true, true, app.UnknownEvidenceInspection{}, false, nil), "\n")
			for _, want := range test.want {
				if !strings.Contains(rendered, want) {
					t.Errorf("rendered card %q does not contain %q", rendered, want)
				}
			}
			if strings.Contains(rendered, "attacker.invalid") || strings.Contains(rendered, "\u202e") {
				t.Errorf("rendered card contains terminal control content: %q", rendered)
			}
		})
	}
}

func TestTimelineActivitiesHideDetailsUntilExpanded(t *testing.T) {
	event := model.EventSummary{ID: "event", Sequence: 1, Kind: model.EventKindSummary}
	payload := model.SummaryData{Text: strings.Repeat("界", 120)}
	collapsed := timelineCardLines(event, payload, 20, true, false, app.UnknownEvidenceInspection{}, false, nil)
	expanded := timelineCardLines(event, payload, 20, true, true, app.UnknownEvidenceInspection{}, false, nil)
	if len(collapsed) >= len(expanded) || strings.Contains(strings.Join(collapsed, "\n"), "界") ||
		!strings.Contains(strings.Join(collapsed, "\n"), "Conversation context") {
		t.Fatalf("collapsed=%q\nexpanded=%q", collapsed, expanded)
	}
}

func TestTimelineMessagesUseConversationLayoutWithoutEventMetadata(t *testing.T) {
	user := model.EventSummary{ID: "user", Sequence: 41, Kind: model.EventKindMessage}
	assistant := model.EventSummary{ID: "assistant", Sequence: 42, Kind: model.EventKindMessage}
	userLines := strings.Join(timelineCardLines(
		user, model.MessageData{Role: model.MessageRoleUser, Text: "question"}, 60, true, false,
		app.UnknownEvidenceInspection{}, false, nil,
	), "\n")
	assistantLines := strings.Join(timelineCardLines(
		assistant, model.MessageData{Role: model.MessageRoleAssistant, Text: "answer"}, 60, false, false,
		app.UnknownEvidenceInspection{}, false, nil,
	), "\n")
	if !strings.Contains(userLines, "╭─ You") || !strings.Contains(assistantLines, "╭─ Assistant") {
		t.Fatalf("user=%q\nassistant=%q", userLines, assistantLines)
	}
	for _, rendered := range []string{userLines, assistantLines} {
		if strings.Contains(rendered, "#41") || strings.Contains(rendered, "#42") || strings.Contains(rendered, "message ·") {
			t.Fatalf("conversation exposed event-log metadata: %q", rendered)
		}
	}
}
