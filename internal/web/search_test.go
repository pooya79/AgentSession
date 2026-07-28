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

func TestSearchPageEscapesResultsAndShowsSafeValidation(t *testing.T) {
	t.Parallel()
	resultID := model.EventID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	handler := NewHandler(servicesStub{search: func(_ context.Context, request app.SearchRequest) (app.SearchPage, error) {
		if request.Query == "bad" {
			return app.SearchPage{}, &app.SearchValidationError{Code: "invalid_quote", Message: "Search query contains an unterminated quote."}
		}
		return app.SearchPage{
			State:        app.EvidenceComplete,
			Availability: app.SearchAvailability{State: app.EvidenceComplete, Sessions: 1, Usable: 1},
			Results: []app.SearchResult{{
				SessionID: "session", EventID: resultID, Kind: model.EventKindMessage,
				Summary: `<script>alert("summary")</script>`,
				Snippet: `<img src=x onerror=alert("snippet")>`,
			}},
		}, nil
	}})
	request := httptest.NewRequest(http.MethodGet, "/search?q=alpha", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, "<script>") || strings.Contains(body, "<img") ||
		!strings.Contains(body, "&lt;script&gt;") || !strings.Contains(body, "&lt;img") {
		t.Fatalf("search response %d:\n%s", response.Code, body)
	}

	request = httptest.NewRequest(http.MethodGet, "/search?q=bad", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "unterminated quote") {
		t.Fatalf("validation response %d: %s", response.Code, response.Body.String())
	}
}
