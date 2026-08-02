package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pooya79/AgentSession/internal/search"
)

var _ search.Repository = (*ImportStore)(nil)

const (
	searchRecencyBoostMaximum = 0.35
	searchRecencyWindowDays   = 90.0
)

// Search queries current ready search projections and returns a bounded page.
func (s *ImportStore) Search(ctx context.Context, query search.Query, cursor *search.Cursor, limit int) (search.Rows, error) {
	availability, err := s.searchAvailability(ctx)
	if err != nil {
		return search.Rows{}, err
	}
	result := search.Rows{Availability: availability}
	if availability.Usable == 0 {
		return result, nil
	}
	if cursor != nil && cursor.Generation != availability.Generation {
		return result, &search.ValidationError{Code: "stale_cursor", Message: "Search results changed; start again from the first page."}
	}
	sqlText, args := buildSearchSQL(query, cursor, limit+1)
	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return result, fmt.Errorf("sqlite search: query projection: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row search.Row
		var lastActivity sql.NullString
		if err := rows.Scan(
			&row.SessionID, &row.Title, &row.SessionSummary, &row.FirstUserMessage,
			&row.AgentName, &lastActivity, &row.EventCount, &row.MatchCount,
			&row.BestMatchSummary, &row.Snippet, &row.Rank,
		); err != nil {
			return result, fmt.Errorf("sqlite search: scan result: %w", err)
		}
		row.LastActivity = lastActivity.String
		result.Items = append(result.Items, row)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("sqlite search: iterate results: %w", err)
	}
	if len(result.Items) > limit {
		result.More = true
		result.Items = result.Items[:limit]
	}
	if cursor != nil && cursor.Before {
		slices.Reverse(result.Items)
	}
	return result, nil
}

func (s *ImportStore) searchAvailability(ctx context.Context) (search.Availability, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, p.status, p.target_version, p.target_revision,
		       p.ready_version, p.ready_revision
		FROM sessions s
		LEFT JOIN session_projection_states p
		  ON p.session_id = s.id AND p.kind = 'search'
		ORDER BY s.id
	`)
	if err != nil {
		return search.Availability{}, fmt.Errorf("sqlite search: read availability: %w", err)
	}
	defer rows.Close()
	hash := sha256.New()
	var availability search.Availability
	for rows.Next() {
		var sessionID string
		var status, targetVersion, readyVersion sql.NullString
		var targetRevision, readyRevision sql.NullInt64
		if err := rows.Scan(&sessionID, &status, &targetVersion, &targetRevision, &readyVersion, &readyRevision); err != nil {
			return availability, fmt.Errorf("sqlite search: scan availability: %w", err)
		}
		availability.Sessions++
		usable := status.String == "ready" && readyRevision.Valid &&
			targetVersion.String == readyVersion.String && targetRevision.Int64 == readyRevision.Int64
		if usable {
			availability.Usable++
			fmt.Fprintf(hash, "%s\x00%s\x00%d\n", sessionID, readyVersion.String, readyRevision.Int64)
		} else {
			switch status.String {
			case "pending":
				availability.Pending++
			case "running":
				availability.Running++
			case "failed":
				availability.Failed++
			}
			if readyRevision.Valid {
				availability.Stale++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return availability, fmt.Errorf("sqlite search: iterate availability: %w", err)
	}
	availability.Generation = hex.EncodeToString(hash.Sum(nil))
	return availability, nil
}

// buildSearchSQL translates an already validated, source-neutral query into
// parameterized SQL. User values are always returned separately in args.
func buildSearchSQL(query search.Query, cursor *search.Cursor, limit int) (string, []any) {
	ranked := query.HasText()
	var statement strings.Builder
	var args []any
	appendRawMatchesCTE(&statement, &args, query, ranked)
	appendSearchFilters(&statement, &args, query)
	appendBestMatchesCTEs(&statement, ranked)
	statement.WriteString(`
			SELECT b.session_id, s.title, s.summary, s.first_user_message,
			       s.adapter_name, s.last_activity_at, s.event_count, b.match_count,
			       b.match_summary, b.snippet, b.rank
			FROM best_matches b
			JOIN sessions s ON s.id = b.session_id
			WHERE 1 = 1
	`)
	appendSearchCursorPredicate(&statement, &args, cursor, ranked)
	appendSearchOrder(&statement, cursor, ranked)
	statement.WriteString(` LIMIT ?`)
	args = append(args, limit)
	return statement.String(), args
}

func appendRawMatchesCTE(statement *strings.Builder, args *[]any, query search.Query, ranked bool) {
	if ranked {
		appendRankedRawMatchesCTE(statement, args, query)
		return
	}
	appendUnrankedRawMatchesCTE(statement)
}

func appendRankedRawMatchesCTE(statement *strings.Builder, args *[]any, query search.Query) {
	statement.WriteString(`
		WITH corpus_anchor AS (
			SELECT MAX(julianday(d.timestamp)) AS timestamp_day
			FROM search_documents d
			JOIN session_projection_states p
			  ON p.session_id = d.session_id AND p.kind = 'search'
			 AND p.status = 'ready'
			 AND p.ready_version = p.target_version
			 AND p.ready_revision = p.target_revision
			 AND d.projection_version = p.ready_version
			 AND d.canonical_revision = p.ready_revision
			WHERE d.timestamp IS NOT NULL
		),
		raw_matches AS (
			SELECT d.session_id, d.event_id, d.sequence, d.timestamp,
			       d.summary AS match_summary,
			       substr(snippet(search_documents_fts, -1, '', '', ' … ', 24), 1, 512) AS snippet,
			       bm25(search_documents_fts) *
			       CASE
			         WHEN julianday(d.timestamp) IS NULL OR corpus_anchor.timestamp_day IS NULL THEN 1.0
			         ELSE 1.0 + ? * MAX(0.0, 1.0 -
			           MAX(0.0, corpus_anchor.timestamp_day - julianday(d.timestamp)) / ?)
			       END AS rank
			FROM search_documents_fts
			JOIN search_documents d ON d.rowid = search_documents_fts.rowid
			JOIN session_projection_states p
			  ON p.session_id = d.session_id AND p.kind = 'search'
			 AND p.status = 'ready'
			 AND p.ready_version = p.target_version
			 AND p.ready_revision = p.target_revision
			 AND d.projection_version = p.ready_version
			 AND d.canonical_revision = p.ready_revision
			CROSS JOIN corpus_anchor
			WHERE search_documents_fts MATCH ?
	`)
	*args = append(*args, searchRecencyBoostMaximum, searchRecencyWindowDays, ftsExpression(query.Text))
}

func appendUnrankedRawMatchesCTE(statement *strings.Builder) {
	statement.WriteString(`
		WITH raw_matches AS (
			SELECT d.session_id, d.event_id, d.sequence, d.timestamp,
			       d.summary AS match_summary, substr(d.content, 1, 512) AS snippet,
			       0.0 AS rank
			FROM search_documents d
			JOIN session_projection_states p
			  ON p.session_id = d.session_id AND p.kind = 'search'
			 AND p.status = 'ready'
			 AND p.ready_version = p.target_version
			 AND p.ready_revision = p.target_revision
			 AND d.projection_version = p.ready_version
			 AND d.canonical_revision = p.ready_revision
			WHERE 1 = 1
	`)
}

func appendBestMatchesCTEs(statement *strings.Builder, ranked bool) {
	statement.WriteString(`
			),
			ranked_matches AS (
				SELECT raw_matches.*,
				       COUNT(*) OVER (PARTITION BY session_id) AS match_count,
	`)
	if ranked {
		statement.WriteString(`
				       ROW_NUMBER() OVER (
				         PARTITION BY session_id ORDER BY rank ASC, event_id ASC
				       ) AS match_position
		`)
	} else {
		statement.WriteString(`
				       ROW_NUMBER() OVER (
				         PARTITION BY session_id
				         ORDER BY timestamp IS NULL ASC, timestamp DESC, sequence DESC, event_id ASC
				       ) AS match_position
		`)
	}
	statement.WriteString(`
				FROM raw_matches
			),
			best_matches AS (
				SELECT * FROM ranked_matches WHERE match_position = 1
			)
	`)
}

func appendSearchCursorPredicate(statement *strings.Builder, args *[]any, cursor *search.Cursor, ranked bool) {
	if cursor == nil {
		return
	}
	if !ranked {
		appendSessionCursor(statement, args, *cursor)
		return
	}
	operator := ">"
	if cursor.Before {
		operator = "<"
	}
	statement.WriteString(" AND (b.rank " + operator + " ? OR (b.rank = ? AND b.session_id " + operator + " ?))")
	*args = append(*args, cursor.Rank, cursor.Rank, cursor.SessionID)
}

func appendSearchOrder(statement *strings.Builder, cursor *search.Cursor, ranked bool) {
	if ranked {
		if cursor != nil && cursor.Before {
			statement.WriteString(` ORDER BY b.rank DESC, b.session_id DESC`)
		} else {
			statement.WriteString(` ORDER BY b.rank ASC, b.session_id ASC`)
		}
	} else if cursor != nil && cursor.Before {
		statement.WriteString(` ORDER BY s.last_activity_at ASC NULLS FIRST, b.session_id DESC`)
	} else {
		statement.WriteString(` ORDER BY s.last_activity_at DESC NULLS LAST, b.session_id ASC`)
	}
}

func appendSearchFilters(statement *strings.Builder, args *[]any, query search.Query) {
	appendIn := func(column string, values []string) {
		if len(values) == 0 {
			return
		}
		statement.WriteString(" AND " + column + " IN (" + placeholders(len(values)) + ")")
		for _, value := range values {
			*args = append(*args, value)
		}
	}
	sessions := make([]string, len(query.Sessions))
	for i, value := range query.Sessions {
		sessions[i] = string(value)
	}
	kinds := make([]string, len(query.Kinds))
	for i, value := range query.Kinds {
		kinds[i] = string(value)
	}
	appendIn("d.session_id", sessions)
	appendIn("d.kind", kinds)
	appendDateGroup(statement, args, ">", query.After)
	appendDateGroup(statement, args, "<", query.Before)
	if len(query.Files) > 0 {
		statement.WriteString(` AND EXISTS (
			SELECT 1 FROM search_document_files f WHERE f.document_rowid = d.rowid AND (`)
		for i, value := range query.Files {
			if i > 0 {
				statement.WriteString(" OR ")
			}
			statement.WriteString(`f.path = ? OR f.path LIKE ? ESCAPE '\'`)
			*args = append(*args, value, escapeLike(value)+"/%")
		}
		statement.WriteString("))")
	}
	appendIn("d.tool_name", query.Tools)
	if len(query.Commands) > 0 {
		statement.WriteString(" AND (")
		for i, value := range query.Commands {
			if i > 0 {
				statement.WriteString(" OR ")
			}
			statement.WriteString(`d.command_text LIKE ? ESCAPE '\'`)
			*args = append(*args, "%"+escapeLike(value)+"%")
		}
		statement.WriteString(")")
	}
}

func appendDateGroup(statement *strings.Builder, args *[]any, operator string, values []time.Time) {
	if len(values) == 0 {
		return
	}
	statement.WriteString(" AND d.timestamp IS NOT NULL AND (")
	for i, value := range values {
		if i > 0 {
			statement.WriteString(" OR ")
		}
		statement.WriteString("julianday(d.timestamp) " + operator + " julianday(?)")
		*args = append(*args, value.UTC().Format(time.RFC3339Nano))
	}
	statement.WriteString(")")
}

func appendSessionCursor(statement *strings.Builder, args *[]any, cursor search.Cursor) {
	if cursor.Before {
		if cursor.LastActivity == "" {
			statement.WriteString(` AND (s.last_activity_at IS NOT NULL OR
				(s.last_activity_at IS NULL AND b.session_id < ?))`)
			*args = append(*args, cursor.SessionID)
		} else {
			statement.WriteString(` AND (s.last_activity_at > ? OR
				(s.last_activity_at = ? AND b.session_id < ?))`)
			*args = append(*args, cursor.LastActivity, cursor.LastActivity, cursor.SessionID)
		}
		return
	}
	if cursor.LastActivity == "" {
		statement.WriteString(` AND s.last_activity_at IS NULL AND b.session_id > ?`)
		*args = append(*args, cursor.SessionID)
	} else {
		statement.WriteString(` AND (s.last_activity_at IS NULL OR s.last_activity_at < ? OR
			(s.last_activity_at = ? AND b.session_id > ?))`)
		*args = append(*args, cursor.LastActivity, cursor.LastActivity, cursor.SessionID)
	}
}

func ftsExpression(clauses []search.TextClause) string {
	values := make([]string, 0, len(clauses))
	for _, clause := range clauses {
		values = append(values, `"`+strings.ReplaceAll(clause.Value, `"`, `""`)+`"`)
	}
	return strings.Join(values, " AND ")
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}
