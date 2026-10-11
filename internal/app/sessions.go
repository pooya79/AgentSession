package app

import (
	"context"
	"fmt"
	"time"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/storage"
)

// SessionSummary is the presentation-safe canonical evidence summary for one session.
type SessionSummary struct {
	ID             model.SessionID
	Title          string
	Summary        string
	StartedAt      *time.Time
	EndedAt        *time.Time
	LastActivityAt *time.Time
	SourceID       model.SourceID
	AgentName      string
	Preview        string
	EventCount     int64
	State          EvidenceState
	Diagnostics    DiagnosticSynopsis
	Interpretation InterpretationCoverage
	// Projections reports derived-data readiness separately from State, which
	// describes canonical evidence only.
	Projections ProjectionSummary
}

// LibraryOverview contains exact counts over committed canonical evidence.
// IssueSessions counts affected sessions rather than individual diagnostics.
type LibraryOverview struct {
	Sessions      int64
	Events        int64
	Agents        int64
	IssueSessions int64
}

// ListSessionsRequest carries an opaque cursor and bounded page size.
type ListSessionsRequest struct {
	Cursor string
	Limit  int
}

// SessionPage contains activity-ordered summaries and opaque navigation cursors.
type SessionPage struct {
	State          EvidenceState
	Sessions       []SessionSummary
	PreviousCursor string
	NextCursor     string
}

// LibraryOverview returns exact counts from committed canonical storage.
func (s *explorationService) LibraryOverview(ctx context.Context) (LibraryOverview, error) {
	if err := ctx.Err(); err != nil {
		return LibraryOverview{}, err
	}
	overview, err := s.reader.LibraryOverview(ctx)
	if err != nil {
		return LibraryOverview{}, fmt.Errorf("read library overview: %w", err)
	}
	return LibraryOverview{
		Sessions: overview.Sessions, Events: overview.Events,
		Agents: overview.Agents, IssueSessions: overview.IssueSessions,
	}, nil
}

// ListSessions returns a bounded keyset page ordered by canonical last activity.
func (s *explorationService) ListSessions(ctx context.Context, request ListSessionsRequest) (SessionPage, error) {
	if err := ctx.Err(); err != nil {
		return SessionPage{}, err
	}
	limit, err := pageLimit(request.Limit)
	if err != nil {
		return SessionPage{}, err
	}
	var after *storage.SessionCursor
	backward := false
	if request.Cursor != "" {
		cursor, err := decodeSessionCursor(request.Cursor)
		if err != nil {
			return SessionPage{}, err
		}
		after = &cursor
		backward = cursor.Before
	}
	rows, more, err := s.reader.ListSessions(ctx, after, limit)
	if err != nil {
		return SessionPage{}, fmt.Errorf("list imported sessions: %w", err)
	}
	page := SessionPage{State: EvidenceComplete, Sessions: make([]SessionSummary, 0, len(rows))}
	for _, row := range rows {
		diagnostics, err := s.reader.Diagnostics(ctx, row.ID, nil, DiagnosticSynopsisMax)
		if err != nil {
			return SessionPage{}, fmt.Errorf("list imported sessions: diagnostics for %q: %w", row.ID, err)
		}
		synopsis := diagnosticSynopsis(diagnostics)
		coverage, err := s.interpretationCoverage(ctx, row.ID)
		if err != nil {
			return SessionPage{}, fmt.Errorf("list imported sessions: interpretation coverage for %q: %w", row.ID, err)
		}
		state := EvidenceComplete
		if synopsis.Total > 0 {
			state = EvidencePartial
			page.State = EvidencePartial
		}
		page.Sessions = append(page.Sessions, SessionSummary{
			ID: row.ID, Title: row.Title, Summary: row.Summary, StartedAt: row.StartedAt, EndedAt: row.EndedAt,
			LastActivityAt: row.LastActivityAt, SourceID: row.SourceID, AgentName: row.AgentName,
			Preview:    sessionPreview(row.Summary, row.FirstUserMessage),
			EventCount: row.EventCount, State: state, Diagnostics: synopsis, Interpretation: coverage,
		})
	}
	if len(rows) > 0 && request.Cursor != "" && (!backward || more) {
		first := rows[0]
		page.PreviousCursor, err = encodeSessionCursor(first, true)
		if err != nil {
			return SessionPage{}, err
		}
	}
	if len(rows) > 0 && (more || backward) {
		last := rows[len(rows)-1]
		page.NextCursor, err = encodeSessionCursor(last, false)
		if err != nil {
			return SessionPage{}, err
		}
	}
	return page, nil
}

// sessionPreview prefers an adapter-provided summary and falls back to the
// first user message. Whitespace normalization keeps previews compact and
// deterministic across terminal and web presentation layers.
func sessionPreview(summary, firstUserMessage string) string {
	preview := normalizeDisplayText(summary)
	if preview == "" {
		preview = normalizeDisplayText(firstUserMessage)
	}
	runes := []rune(preview)
	if len(runes) <= SessionPreviewMaxRunes {
		return preview
	}
	return string(runes[:SessionPreviewMaxRunes-1]) + "…"
}
