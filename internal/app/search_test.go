package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/search"
)

type searchRepositoryStub struct {
	rows       search.Rows
	err        error
	lastQuery  search.Query
	lastCursor *search.Cursor
	lastLimit  int
}

func (s *searchRepositoryStub) Search(_ context.Context, query search.Query, cursor *search.Cursor, limit int) (search.Rows, error) {
	s.lastQuery = query
	s.lastCursor = cursor
	s.lastLimit = limit
	return s.rows, s.err
}

func TestSearchMapsSessionResultsAndBindsCursorToQuery(t *testing.T) {
	t.Parallel()
	activity := "2026-07-28T12:00:00Z"
	repository := &searchRepositoryStub{rows: search.Rows{
		Items: []search.Row{{
			SessionID: "session-a", Title: "A session", SessionSummary: "  useful\nsummary ",
			FirstUserMessage: "ignored fallback", AgentName: "codex", LastActivity: activity,
			EventCount: 8, MatchCount: 3, BestMatchSummary: "Matching command",
			Snippet: "alpha result", Rank: -1.25,
		}},
		More: true,
		Availability: search.Availability{
			Sessions: 2, Usable: 1, Pending: 1, Generation: "generation",
		},
	}}
	service, err := NewSearcher(repository)
	if err != nil {
		t.Fatal(err)
	}

	page, err := service.Search(context.Background(), SearchRequest{Query: "alpha", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.State != EvidencePartial || len(page.Results) != 1 || page.NextCursor == "" {
		t.Fatalf("Search() page = %#v", page)
	}
	result := page.Results[0]
	if result.SessionID != "session-a" || result.Title != "A session" ||
		result.Preview != "useful summary" || result.AgentName != "codex" ||
		result.EventCount != 8 || result.MatchCount != 3 ||
		result.BestMatchSummary != "Matching command" || result.Snippet != "alpha result" ||
		result.LastActivityAt == nil || result.LastActivityAt.Format(time.RFC3339) != activity {
		t.Fatalf("Search() result = %#v", result)
	}

	repository.rows = search.Rows{Availability: search.Availability{
		Sessions: 2, Usable: 1, Pending: 1, Generation: "generation",
	}}
	if _, err := service.Search(context.Background(), SearchRequest{
		Query: "alpha", Cursor: page.NextCursor, Limit: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if repository.lastCursor == nil || repository.lastCursor.SessionID != "session-a" ||
		!repository.lastCursor.Ranked || repository.lastCursor.Rank != -1.25 ||
		repository.lastCursor.LastActivity != activity {
		t.Fatalf("decoded cursor = %#v", repository.lastCursor)
	}
	if _, err := service.Search(context.Background(), SearchRequest{
		Query: "different", Cursor: page.NextCursor, Limit: 10,
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("query-mismatched cursor error = %v", err)
	}

	repository.rows = search.Rows{
		Items: []search.Row{{SessionID: "session-a", LastActivity: activity}},
		More:  true,
		Availability: search.Availability{
			Sessions: 1, Usable: 1, Generation: "generation",
		},
	}
	unrankedPage, err := service.Search(context.Background(), SearchRequest{Query: "kind:message", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	repository.rows = search.Rows{Availability: repository.rows.Availability}
	if _, err := service.Search(context.Background(), SearchRequest{
		Query: "kind:message", Cursor: unrankedPage.NextCursor, Limit: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if repository.lastCursor == nil || repository.lastCursor.SessionID != "session-a" ||
		repository.lastCursor.Ranked || repository.lastCursor.LastActivity != activity {
		t.Fatalf("decoded unranked cursor = %#v", repository.lastCursor)
	}

	for _, test := range []struct {
		name         string
		scope        string
		lastActivity string
	}{
		{name: "event scoped", scope: "events", lastActivity: activity},
		{name: "malformed last activity", scope: "sessions", lastActivity: "not-a-time"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cursor := testSearchCursor(t, searchCursorEnvelope{
				Version: 1, Scope: test.scope, QueryHash: hashSearchQuery("kind:message"),
				Generation: "generation", LastActivity: test.lastActivity, SessionID: "session-a",
			})
			if _, err := service.Search(context.Background(), SearchRequest{
				Query: "kind:message", Cursor: cursor, Limit: 10,
			}); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("tampered cursor error = %v", err)
			}
		})
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

func testSearchCursor(t *testing.T, envelope searchCursorEnvelope) string {
	t.Helper()
	value, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

func TestSearchRejectsInvalidRepositoryActivity(t *testing.T) {
	t.Parallel()
	repository := &searchRepositoryStub{rows: search.Rows{
		Items:        []search.Row{{SessionID: "session-a", LastActivity: "not-a-time"}},
		Availability: search.Availability{Sessions: 1, Usable: 1, Generation: "generation"},
	}}
	service, _ := NewSearcher(repository)
	if _, err := service.Search(context.Background(), SearchRequest{}); err == nil {
		t.Fatal("Search() error = nil")
	}
}
