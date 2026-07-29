package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/app"
)

func TestSearchPageEscapesResultsAndShowsSafeValidation(t *testing.T) {
	t.Parallel()
	handler := NewHandler(servicesStub{search: func(_ context.Context, request app.SearchRequest) (app.SearchPage, error) {
		if request.Query == "bad" {
			return app.SearchPage{}, &app.SearchValidationError{Code: "invalid_quote", Message: "Search query contains an unterminated quote."}
		}
		return app.SearchPage{
			State:        app.EvidenceComplete,
			Availability: app.SearchAvailability{State: app.EvidenceComplete, Sessions: 1, Usable: 1},
			Results: []app.SearchResult{{
				SessionID: "session", Title: `<script>alert("title")</script>`,
				AgentName: `<img src=x onerror=alert("agent")>`, EventCount: 4, MatchCount: 2,
				BestMatchSummary: `<script>alert("summary")</script>`,
				Snippet:          `<img src=x onerror=alert("snippet")>`,
			}},
		}, nil
	}})
	request := httptest.NewRequest(http.MethodGet, "/search?q=alpha", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, "<script>") || strings.Contains(body, "<img") ||
		!strings.Contains(body, "&lt;script&gt;") || !strings.Contains(body, "&lt;img") ||
		!strings.Contains(body, `href="/sessions/session"`) || !strings.Contains(body, "2 matching events") ||
		response.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("search response %d:\n%s", response.Code, body)
	}

	request = httptest.NewRequest(http.MethodGet, "/search?q=bad", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "unterminated quote") {
		t.Fatalf("validation response %d: %s", response.Code, response.Body.String())
	}
}

func TestSearchBoundaryValidatesQueryShape(t *testing.T) {
	t.Parallel()
	var requests []app.SearchRequest
	handler := NewHandler(servicesStub{search: func(_ context.Context, request app.SearchRequest) (app.SearchPage, error) {
		requests = append(requests, request)
		return app.SearchPage{State: app.EvidenceComplete}, nil
	}})

	for _, target := range []string{"/search", "/search?q=", "/search?q=alpha&cursor=opaque"} {
		response := request(t, handler, http.MethodGet, target, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d", target, response.Code, http.StatusOK)
		}
	}
	if len(requests) != 3 ||
		requests[0].Query != "" || requests[0].Cursor != "" ||
		requests[1].Query != "" || requests[1].Cursor != "" ||
		requests[2].Query != "alpha" || requests[2].Cursor != "opaque" {
		t.Fatalf("search requests = %#v", requests)
	}
	for _, request := range requests {
		if request.Limit != defaultPageLimit {
			t.Fatalf("search limit = %d, want %d", request.Limit, defaultPageLimit)
		}
	}

	for _, target := range []string{
		"/search?unknown=value",
		"/search?q=one&q=two",
		"/search?cursor=one&cursor=two",
		"/search?cursor=",
	} {
		response := request(t, handler, http.MethodGet, target, nil, nil)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want %d", target, response.Code, http.StatusBadRequest)
		}
	}
	if len(requests) != 3 {
		t.Fatalf("invalid requests reached search service: %#v", requests)
	}
}

func TestSearchBoundaryMapsUnavailableAndServiceErrors(t *testing.T) {
	t.Parallel()
	response := request(t, NewHandler(nil), http.MethodGet, "/search", nil, nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable services status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}

	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "not found", err: app.ErrSourceNotFound, status: http.StatusNotFound},
		{name: "internal", err: errors.New("sensitive storage failure"), status: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(servicesStub{search: func(context.Context, app.SearchRequest) (app.SearchPage, error) {
				return app.SearchPage{}, test.err
			}})
			response := request(t, handler, http.MethodGet, "/search?q=evidence", nil, nil)
			if response.Code != test.status || strings.Contains(response.Body.String(), test.err.Error()) {
				t.Fatalf("response = %d %q, want status %d without cause", response.Code, response.Body.String(), test.status)
			}
		})
	}
}
