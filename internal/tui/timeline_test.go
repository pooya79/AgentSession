package tui

import (
	"errors"
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
		name              string
		kind              model.EventKind
		payload           model.NormalizedData
		inspection        app.UnknownEvidenceInspection
		inspectionLoading bool
		inspectionErr     error
		want              []string
	}{
		{name: "message", kind: model.EventKindMessage, payload: model.MessageData{Role: model.MessageRoleAssistant, Text: hostile}, want: []string{"╭─ Assistant", "<U+202E>"}},
		{name: "summary", kind: model.EventKindSummary, payload: model.SummaryData{Text: "recorded summary"}, want: []string{"Summary: recorded summary"}},
		{name: "tool call valid json", kind: model.EventKindToolCall, payload: model.ToolCallData{ToolName: "read", CallID: "call", Input: `{"path":"x"}`}, want: []string{"Tool: read", `"path": "x"`}},
		{name: "tool call invalid json", kind: model.EventKindToolCall, payload: model.ToolCallData{Input: "{invalid"}, want: []string{"Input: {invalid"}},
		{name: "tool result", kind: model.EventKindToolResult, payload: model.ToolResultData{ToolName: "read", IsError: &trueValue, Output: "result"}, want: []string{"Error: true", "Output: result"}},
		{name: "command", kind: model.EventKindCommand, payload: model.CommandData{Command: "go test", ExitCode: &exit, Output: "failed"}, want: []string{"Command: go test", "Exit status: 1"}},
		{name: "patch", kind: model.EventKindPatch, payload: model.PatchData{Paths: []string{"a.go"}, Text: "-old\n+new"}, want: []string{"Paths: a.go", "-old", "+new"}},
		{name: "file read", kind: model.EventKindFileRead, payload: model.FileReadData{Path: "a.go", StartLine: &line}, want: []string{"Path: a.go", "Lines: from 3"}},
		{name: "file mutation", kind: model.EventKindFileMutation, payload: model.FileMutationData{Operation: model.FileMutationRename, Path: "b.go", PreviousPath: "a.go"}, want: []string{"Operation: rename", "Renamed from: a.go"}},
		{name: "usage", kind: model.EventKindUsage, payload: model.UsageData{InputTokens: &count}, want: []string{"input 0", "output unreported"}},
		{name: "error", kind: model.EventKindError, payload: model.ErrorData{Code: "E_TEST", Message: "broken"}, want: []string{"Code: E_TEST", "Error: broken"}},
		{name: "unknown", kind: model.EventKindUnknown, payload: model.UnknownData{Reason: model.UnknownUnsupportedRecordKind, OriginalKind: "future"}, want: []string{"Reason: unsupported_record_kind", "Press u"}},
		{
			name: "unknown inspection loading", kind: model.EventKindUnknown,
			payload:           model.UnknownData{Reason: model.UnknownUnsupportedRecordKind, OriginalKind: "future"},
			inspectionLoading: true, want: []string{"Loading bounded, redacted retained evidence"},
		},
		{
			name: "unknown inspection error", kind: model.EventKindUnknown,
			payload:       model.UnknownData{Reason: model.UnknownUnsupportedRecordKind, OriginalKind: "future"},
			inspectionErr: errors.New("inspection unavailable"), want: []string{"Could not inspect retained evidence: inspection unavailable"},
		},
		{
			name: "unknown inspection unavailable", kind: model.EventKindUnknown,
			payload:    model.UnknownData{Reason: model.UnknownUnsupportedRecordKind, OriginalKind: "future"},
			inspection: app.UnknownEvidenceInspection{State: app.EvidenceUnavailable, EventID: "event"},
			want:       []string{"Retained evidence for this event is unavailable"},
		},
		{
			name: "unknown inspection not found", kind: model.EventKindUnknown,
			payload:    model.UnknownData{Reason: model.UnknownUnsupportedRecordKind, OriginalKind: "future"},
			inspection: app.UnknownEvidenceInspection{State: app.EvidenceNotFound, EventID: "event"},
			want:       []string{"retained evidence could not be located"},
		},
		{name: "missing payload", kind: model.EventKindUnknown, want: []string{"Details unavailable."}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := model.EventSummary{ID: "event", Sequence: 2, Kind: test.kind}
			rendered := strings.Join(timelineCardLines(
				event, test.payload, 80, true, true,
				test.inspection, test.inspectionLoading, test.inspectionErr,
			), "\n")
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
		user, model.MessageData{Role: model.MessageRoleUser, Text: "question\nsecond line"}, 60, true, false,
		app.UnknownEvidenceInspection{}, false, nil,
	), "\n")
	assistantLines := strings.Join(timelineCardLines(
		assistant, model.MessageData{Role: model.MessageRoleAssistant, Text: "answer"}, 60, false, false,
		app.UnknownEvidenceInspection{}, false, nil,
	), "\n")
	if !strings.Contains(userLines, "╭─ You") || !strings.Contains(assistantLines, "╭─ Assistant") {
		t.Fatalf("user=%q\nassistant=%q", userLines, assistantLines)
	}
	for _, line := range []string{"question", "second line", "answer"} {
		if !strings.Contains("\n"+userLines+"\n"+assistantLines+"\n", "\n"+line+"\n") {
			t.Fatalf("message line %q has a copy-hostile prefix:\nuser=%q\nassistant=%q", line, userLines, assistantLines)
		}
	}
	for _, rendered := range []string{userLines, assistantLines} {
		if strings.Contains(rendered, "│") || strings.Contains(rendered, "#41") ||
			strings.Contains(rendered, "#42") || strings.Contains(rendered, "message ·") {
			t.Fatalf("conversation exposed event-log metadata: %q", rendered)
		}
	}
}

func TestTimelineViewportHeightTracksOptionalHeaderLines(t *testing.T) {
	m := New(t.Context(), nil)
	m.screen = timelineScreen
	m.width, m.height = 80, 24
	m.sessionsState.selected = "session"
	m.timelineState.err = errors.New("continuation unavailable")
	m.timelineState.page = app.TimelinePage{
		State: app.EvidencePartial,
		Events: []model.EventSummary{{
			ID: "event", SessionID: "session", Kind: model.EventKindMessage,
		}},
		Payloads: map[model.EventID]model.NormalizedData{
			"event": model.MessageData{Role: model.MessageRoleAssistant, Text: "answer"},
		},
		Diagnostics: app.DiagnosticSynopsis{Total: 1},
	}
	m.syncViewports()
	want := max(1, m.contentHeight()-len(m.timelineHeaderLines()))
	if got := m.timelineState.viewport.Height(); got != want {
		t.Fatalf("timeline viewport height = %d, want %d for %d header lines", got, want, len(m.timelineHeaderLines()))
	}
}

func TestSelectedMessageHeaderKeepsFocusedStyle(t *testing.T) {
	m := New(t.Context(), nil)
	line := "> ╭─ You"
	styled := m.styleLines([]string{"header", "status", line})
	if got, want := styled[2], m.theme.focused.Render(line); got != want {
		t.Fatalf("selected role header style = %q, want focused %q", got, want)
	}

	unselected := "  ╭─ Assistant"
	styled = m.styleLines([]string{"header", "status", unselected, "footer"})
	if got, want := styled[2], m.theme.info.Render(unselected); got != want {
		t.Fatalf("unselected assistant style = %q, want role style %q", got, want)
	}
}
