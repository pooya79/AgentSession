package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestTimelineComponentsCoverCanonicalPayloads(t *testing.T) {
	failed := true
	exit := 2
	line := int64(4)
	zero := int64(0)
	hostile := `<script data-raw="secret">alert(1)</script>`
	tests := []struct {
		name    string
		payload model.NormalizedData
		want    []string
	}{
		{"user message", model.MessageData{Role: model.MessageRoleUser, Text: hostile}, []string{"message-user", "You", "&lt;script"}},
		{"assistant message", model.MessageData{Role: model.MessageRoleAssistant, Text: "answer"}, []string{"message-assistant", "Assistant", "answer"}},
		{"system message", model.MessageData{Role: model.MessageRoleSystem, Text: "instruction"}, []string{"message-system", "System"}},
		{"other message", model.MessageData{Role: model.MessageRoleOther, Text: "peer"}, []string{"message-other", "Participant"}},
		{"unknown role", model.MessageData{Role: model.MessageRoleUnknown, Text: "text"}, []string{"message-unknown", "role unreported"}},
		{"reasoning", model.SummaryData{Category: model.SummaryCategoryReasoning, Text: "consider evidence"}, []string{"summary-reasoning", "Reasoning", "<details"}},
		{"context", model.SummaryData{Category: model.SummaryCategoryContext, Text: "compact"}, []string{"Conversation context", "summary-preview"}},
		{"plan", model.SummaryData{Category: model.SummaryCategoryPlan, Text: "next"}, []string{"Plan update"}},
		{"summary", model.SummaryData{Category: model.SummaryCategorySummary, Text: "done"}, []string{"Session summary"}},
		{"valid tool JSON", model.ToolCallData{ToolName: "read", CallID: "c1", Input: `{"path":"x"}`}, []string{"requested", "json-input", "&#34;path&#34;: &#34;x&#34;", "Raw tool input"}},
		{"invalid tool JSON", model.ToolCallData{ToolName: "read", Input: "{bad"}, []string{"Raw tool input", "{bad"}},
		{"failed tool", model.ToolResultData{ToolName: "read", IsError: &failed, Output: "no"}, []string{"Tool result", "failed", "Tool output"}},
		{"failed command", model.CommandData{Command: "go test", WorkingDirectory: "/repo", ExitCode: &exit, Output: "failure"}, []string{"go test", "exited 2", "/repo", "Command output"}},
		{"patch", model.PatchData{Paths: []string{"a.go", "b.go"}, Text: "@@ -1 +1 @@\n-old\n+new\n neutral"}, []string{"a.go and 1 more", "patch-hunk", "patch-delete", "patch-add", "patch-neutral"}},
		{"file read", model.FileReadData{Path: "a.go", StartLine: &line}, []string{"Read file", "a.go", "from line 4"}},
		{"file rename", model.FileMutationData{Operation: model.FileMutationRename, Path: "b.go", PreviousPath: "a.go"}, []string{"rename file", "Renamed from"}},
		{"usage", model.UsageData{InputTokens: &zero}, []string{"Token usage", ">0<", "unreported"}},
		{"error", model.ErrorData{Code: "E_TEST", Message: hostile}, []string{"error-card", "E_TEST", "&lt;script"}},
		{"unknown", model.UnknownData{Reason: model.UnknownUnsupportedRecordKind, OriginalKind: "future"}, []string{"Unknown event", "unsupported_record_kind", "Inspect retained Unknown evidence", `name="csrf"`}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			id := model.EventID("event-" + string(rune('a'+index)))
			event := model.EventSummary{ID: id, SessionID: "session", Sequence: int64(index), Kind: payloadKind(test.payload)}
			page := app.TimelinePage{
				State: app.EvidenceComplete, Events: []model.EventSummary{event},
				Payloads: map[model.EventID]model.NormalizedData{id: test.payload},
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			response := httptest.NewRecorder()
			render(response, req, http.StatusOK, eventRows("session", page, nil, "token"))
			body := response.Body.String()
			for _, want := range test.want {
				if !strings.Contains(body, want) {
					t.Errorf("body missing %q: %q", want, body)
				}
			}
			if strings.Contains(body, `<script data-raw`) || strings.Contains(body, "secret\">") {
				t.Errorf("hostile HTML was not escaped: %q", body)
			}
		})
	}
}

func TestTimelineMissingPayloadAndContinuationAreHonestAndBounded(t *testing.T) {
	eventID := model.EventID("event")
	var requests []app.TimelineRequest
	handler := NewHandler(servicesStub{
		timeline: func(_ context.Context, request app.TimelineRequest) (app.TimelinePage, error) {
			requests = append(requests, request)
			return app.TimelinePage{
				State: app.EvidenceComplete,
				Events: []model.EventSummary{{
					ID: eventID, SessionID: request.SessionID, Kind: model.EventKindMessage, Summary: "not complete content",
				}},
				NextCursor: "next",
			}, nil
		},
	})
	page := request(t, handler, http.MethodGet, "/sessions/session?limit=200", nil, nil)
	body := page.Body.String()
	for _, want := range []string{
		"Evidence unavailable", "summary is not a substitute", `rel="next"`,
		`hx-trigger="revealed"`, "Load more events",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("initial body missing %q: %q", want, body)
		}
	}
	if strings.Contains(body, "not complete content") {
		t.Fatalf("event summary was rendered as payload: %q", body)
	}
	fragment := request(t, handler, http.MethodGet, "/sessions/session/fragments/events?cursor=opaque&limit=200", nil, nil)
	if fragment.Code != http.StatusOK || len(requests) != 2 {
		t.Fatalf("fragment status/requests = %d/%#v", fragment.Code, requests)
	}
	for _, got := range requests {
		if !got.IncludePayloads || got.Limit != app.DefaultPageSize {
			t.Fatalf("timeline request is not bounded with payloads: %#v", got)
		}
	}
	if requests[1].Cursor != "opaque" {
		t.Fatalf("fragment cursor = %q", requests[1].Cursor)
	}
}

func payloadKind(payload model.NormalizedData) model.EventKind {
	switch payload.(type) {
	case model.MessageData:
		return model.EventKindMessage
	case model.SummaryData:
		return model.EventKindSummary
	case model.ToolCallData:
		return model.EventKindToolCall
	case model.ToolResultData:
		return model.EventKindToolResult
	case model.CommandData:
		return model.EventKindCommand
	case model.PatchData:
		return model.EventKindPatch
	case model.FileReadData:
		return model.EventKindFileRead
	case model.FileMutationData:
		return model.EventKindFileMutation
	case model.UsageData:
		return model.EventKindUsage
	case model.ErrorData:
		return model.EventKindError
	default:
		return model.EventKindUnknown
	}
}
