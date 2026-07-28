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
		var timestamp sql.NullString
		if err := rows.Scan(&row.SessionID, &row.EventID, &row.Sequence, &timestamp, &row.Kind, &row.Summary, &row.Snippet, &row.Rank); err != nil {
			return result, fmt.Errorf("sqlite search: scan result: %w", err)
		}
		row.Timestamp = timestamp.String
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

func buildSearchSQL(query search.Query, cursor *search.Cursor, limit int) (string, []any) {
	ranked := query.HasText()
	var statement strings.Builder
	var args []any
	if ranked {
		statement.WriteString(`
			WITH matched AS (
				SELECT rowid, bm25(search_documents_fts) AS rank,
				       snippet(search_documents_fts, -1, '', '', ' … ', 24) AS snippet
				FROM search_documents_fts
				WHERE search_documents_fts MATCH ?
			)
			SELECT d.session_id, d.event_id, d.sequence, d.timestamp, d.kind,
			       d.summary, substr(m.snippet, 1, 512), m.rank
			FROM matched m
			JOIN search_documents d ON d.rowid = m.rowid
		`)
		args = append(args, ftsExpression(query.Text))
	} else {
		statement.WriteString(`
			SELECT d.session_id, d.event_id, d.sequence, d.timestamp, d.kind,
			       d.summary, substr(d.content, 1, 512), 0.0
			FROM search_documents d
		`)
	}
	statement.WriteString(`
		JOIN session_projection_states p
		  ON p.session_id = d.session_id AND p.kind = 'search'
		 AND p.status = 'ready'
		 AND p.ready_version = p.target_version
		 AND p.ready_revision = p.target_revision
		 AND d.projection_version = p.ready_version
		 AND d.canonical_revision = p.ready_revision
		WHERE 1 = 1
	`)
	appendSearchFilters(&statement, &args, query)
	if cursor != nil {
		if ranked {
			operator := ">"
			if cursor.Before {
				operator = "<"
			}
			statement.WriteString(" AND (m.rank " + operator + " ? OR (m.rank = ? AND d.event_id " + operator + " ?))")
			args = append(args, cursor.Rank, cursor.Rank, cursor.EventID)
		} else {
			appendFilterCursor(&statement, &args, *cursor)
		}
	}
	if ranked {
		if cursor != nil && cursor.Before {
			statement.WriteString(` ORDER BY m.rank DESC, d.event_id DESC`)
		} else {
			statement.WriteString(` ORDER BY m.rank ASC, d.event_id ASC`)
		}
	} else if cursor != nil && cursor.Before {
		statement.WriteString(` ORDER BY d.timestamp IS NULL DESC, julianday(d.timestamp) ASC, d.session_id DESC, d.sequence DESC, d.event_id DESC`)
	} else {
		statement.WriteString(` ORDER BY d.timestamp IS NULL ASC, julianday(d.timestamp) DESC, d.session_id ASC, d.sequence ASC, d.event_id ASC`)
	}
	statement.WriteString(` LIMIT ?`)
	args = append(args, limit)
	return statement.String(), args
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

func appendFilterCursor(statement *strings.Builder, args *[]any, cursor search.Cursor) {
	if cursor.Before {
		if cursor.Timestamp == "" {
			statement.WriteString(` AND (d.timestamp IS NOT NULL OR
				d.timestamp IS NULL AND
				(d.session_id < ? OR (d.session_id = ? AND
				 (d.sequence < ? OR (d.sequence = ? AND d.event_id < ?)))))`)
			*args = append(*args, cursor.SessionID, cursor.SessionID, cursor.Sequence, cursor.Sequence, cursor.EventID)
		} else {
			statement.WriteString(` AND (
				d.timestamp IS NOT NULL AND julianday(d.timestamp) > julianday(?) OR
				julianday(d.timestamp) = julianday(?) AND (d.session_id < ? OR (d.session_id = ? AND
				 (d.sequence < ? OR (d.sequence = ? AND d.event_id < ?))))
			)`)
			*args = append(*args, cursor.Timestamp, cursor.Timestamp, cursor.SessionID, cursor.SessionID, cursor.Sequence, cursor.Sequence, cursor.EventID)
		}
		return
	}
	if cursor.Timestamp == "" {
		statement.WriteString(` AND d.timestamp IS NULL AND
			(d.session_id > ? OR (d.session_id = ? AND
			 (d.sequence > ? OR (d.sequence = ? AND d.event_id > ?))))`)
		*args = append(*args, cursor.SessionID, cursor.SessionID, cursor.Sequence, cursor.Sequence, cursor.EventID)
	} else {
		statement.WriteString(` AND (
			d.timestamp IS NULL OR julianday(d.timestamp) < julianday(?) OR
			julianday(d.timestamp) = julianday(?) AND (d.session_id > ? OR (d.session_id = ? AND
			 (d.sequence > ? OR (d.sequence = ? AND d.event_id > ?))))
		)`)
		*args = append(*args, cursor.Timestamp, cursor.Timestamp, cursor.SessionID, cursor.SessionID, cursor.Sequence, cursor.Sequence, cursor.EventID)
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
