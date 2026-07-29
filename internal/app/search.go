package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/search"
)

// SearchValidationError is a presentation-safe invalid request with a stable
// code. It unwraps to ErrInvalidRequest for shared error handling.
type SearchValidationError struct {
	Code    string
	Message string
}

func (e *SearchValidationError) Error() string { return e.Message }
func (e *SearchValidationError) Unwrap() error { return ErrInvalidRequest }

// SearchRequest describes a bounded search page and its opaque continuation
// cursor. An empty cursor requests the first page.
type SearchRequest struct {
	Query  string
	Cursor string
	Limit  int
}

// SearchAvailability summarizes how much imported canonical evidence has a
// current, queryable search projection.
type SearchAvailability struct {
	State         EvidenceState
	Sessions      int64
	Usable        int64
	Pending       int64
	Running       int64
	Failed        int64
	Stale         int64
	Unimplemented int64
}

// SearchResult is a presentation-safe summary of one session containing
// matching canonical evidence. LastActivityAt is nil when activity is unknown.
type SearchResult struct {
	SessionID        model.SessionID
	Title            string
	AgentName        string
	Preview          string
	LastActivityAt   *time.Time
	EventCount       int64
	MatchCount       int64
	BestMatchSummary string
	Snippet          string
}

// SearchResultDisplayTitle derives one normalized session label for every
// presentation layer.
func SearchResultDisplayTitle(result SearchResult) string {
	for _, candidate := range []string{result.Title, result.Preview, string(result.SessionID)} {
		if value := normalizeDisplayText(candidate); value != "" {
			return value
		}
	}
	return "Untitled session"
}

// SearchResultDisplayPreview normalizes a result preview for display and title
// comparison.
func SearchResultDisplayPreview(result SearchResult) string {
	return normalizeDisplayText(result.Preview)
}

// SearchResultAgentLabel supplies the shared fallback for absent agent metadata.
func SearchResultAgentLabel(result SearchResult) string {
	if value := normalizeDisplayText(result.AgentName); value != "" {
		return value
	}
	return "AGENT UNREPORTED"
}

// SearchResultMatchSummary supplies the shared fallback for absent match
// summaries.
func SearchResultMatchSummary(result SearchResult) string {
	if value := normalizeDisplayText(result.BestMatchSummary); value != "" {
		return value
	}
	return "Matching evidence"
}

func normalizeDisplayText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// SearchPage contains one bounded result page, navigation cursors, and the
// projection availability that qualifies the returned evidence.
type SearchPage struct {
	State          EvidenceState
	Results        []SearchResult
	NextCursor     string
	PreviousCursor string
	Availability   SearchAvailability
}

// Searcher exposes lifecycle-aware search over canonical session evidence.
type Searcher interface {
	// Search validates the query and returns a bounded result page.
	Search(context.Context, SearchRequest) (SearchPage, error)
}

type searchService struct{ repository search.Repository }

// NewSearcher constructs the shared application search service.
func NewSearcher(repository search.Repository) (Searcher, error) {
	if repository == nil {
		return nil, errors.New("application searcher: repository is required")
	}
	return &searchService{repository: repository}, nil
}

func (s *searchService) Search(ctx context.Context, request SearchRequest) (SearchPage, error) {
	if err := ctx.Err(); err != nil {
		return SearchPage{}, err
	}
	limit, err := pageLimit(request.Limit)
	if err != nil {
		return SearchPage{}, err
	}
	parsed, err := search.Parse(request.Query)
	if err != nil {
		return SearchPage{}, mapSearchValidation(err)
	}
	queryHash := hashSearchQuery(request.Query)
	var cursor *search.Cursor
	if request.Cursor != "" {
		decoded, err := decodeSearchCursor(request.Cursor, queryHash, parsed.HasText())
		if err != nil {
			return SearchPage{}, err
		}
		cursor = &decoded
	}
	rows, err := s.repository.Search(ctx, parsed, cursor, limit)
	if err != nil {
		return SearchPage{}, mapSearchValidation(err)
	}
	page := SearchPage{
		State:   EvidenceComplete,
		Results: make([]SearchResult, 0, len(rows.Items)),
		Availability: SearchAvailability{
			Sessions: rows.Availability.Sessions, Usable: rows.Availability.Usable,
			Pending: rows.Availability.Pending, Running: rows.Availability.Running,
			Failed: rows.Availability.Failed, Stale: rows.Availability.Stale,
			Unimplemented: rows.Availability.Unimplemented,
		},
	}
	switch {
	case rows.Availability.Sessions > 0 && rows.Availability.Usable == 0:
		page.State = EvidenceUnavailable
	case rows.Availability.Usable < rows.Availability.Sessions:
		page.State = EvidencePartial
	}
	page.Availability.State = page.State
	for _, row := range rows.Items {
		var lastActivity *time.Time
		if row.LastActivity != "" {
			parsedTime, parseErr := time.Parse(time.RFC3339Nano, row.LastActivity)
			if parseErr != nil {
				return SearchPage{}, fmt.Errorf("search result contains invalid last activity: %w", parseErr)
			}
			lastActivity = &parsedTime
		}
		page.Results = append(page.Results, SearchResult{
			SessionID: row.SessionID, Title: row.Title, AgentName: row.AgentName,
			Preview:        sessionPreview(row.SessionSummary, row.FirstUserMessage),
			LastActivityAt: lastActivity, EventCount: row.EventCount, MatchCount: row.MatchCount,
			BestMatchSummary: row.BestMatchSummary,
			Snippet:          truncateUTF8Bytes(row.Snippet, 2*1024),
		})
	}
	if len(rows.Items) == 0 {
		return page, nil
	}
	first, last := rows.Items[0], rows.Items[len(rows.Items)-1]
	if cursor == nil || !cursor.Before {
		if cursor != nil {
			page.PreviousCursor, err = encodeSearchCursor(first, true, rows.Availability.Generation, queryHash, parsed.HasText())
		}
		if err == nil && rows.More {
			page.NextCursor, err = encodeSearchCursor(last, false, rows.Availability.Generation, queryHash, parsed.HasText())
		}
	} else {
		if rows.More {
			page.PreviousCursor, err = encodeSearchCursor(first, true, rows.Availability.Generation, queryHash, parsed.HasText())
		}
		if err == nil {
			page.NextCursor, err = encodeSearchCursor(last, false, rows.Availability.Generation, queryHash, parsed.HasText())
		}
	}
	if err != nil {
		return SearchPage{}, err
	}
	return page, nil
}

type searchCursorEnvelope struct {
	Version      int             `json:"v"`
	Scope        string          `json:"scope"`
	QueryHash    string          `json:"q"`
	Generation   string          `json:"g"`
	Ranked       bool            `json:"ranked"`
	Rank         float64         `json:"rank,omitempty"`
	LastActivity string          `json:"last_activity,omitempty"`
	SessionID    model.SessionID `json:"session"`
	Before       bool            `json:"before,omitempty"`
}

func encodeSearchCursor(row search.Row, before bool, generation, queryHash string, ranked bool) (string, error) {
	value, err := json.Marshal(searchCursorEnvelope{
		Version: 1, Scope: "sessions", QueryHash: queryHash, Generation: generation, Ranked: ranked,
		Rank: row.Rank, LastActivity: row.LastActivity, SessionID: row.SessionID, Before: before,
	})
	if err != nil {
		return "", fmt.Errorf("encode search cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func decodeSearchCursor(value, queryHash string, ranked bool) (search.Cursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	var envelope searchCursorEnvelope
	if err != nil || json.Unmarshal(decoded, &envelope) != nil ||
		envelope.Version != 1 || envelope.Scope != "sessions" ||
		envelope.QueryHash != queryHash || envelope.Ranked != ranked ||
		envelope.Generation == "" || validateIdentifier("search cursor session", string(envelope.SessionID)) != nil {
		return search.Cursor{}, &SearchValidationError{Code: "invalid_cursor", Message: "Search cursor is invalid for this query."}
	}
	if envelope.LastActivity != "" {
		if _, err := time.Parse(time.RFC3339Nano, envelope.LastActivity); err != nil {
			return search.Cursor{}, &SearchValidationError{Code: "invalid_cursor", Message: "Search cursor is invalid for this query."}
		}
	}
	return search.Cursor{
		Rank: envelope.Rank, LastActivity: envelope.LastActivity, SessionID: envelope.SessionID,
		Before:     envelope.Before,
		Generation: envelope.Generation, Ranked: envelope.Ranked,
	}, nil
}

func hashSearchQuery(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func mapSearchValidation(err error) error {
	var validation *search.ValidationError
	if errors.As(err, &validation) {
		return &SearchValidationError{Code: validation.Code, Message: validation.Message}
	}
	return err
}
