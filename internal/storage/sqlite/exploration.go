package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/pooya79/AgentSession/internal/model"
	storagecontract "github.com/pooya79/AgentSession/internal/storage"
)

var _ storagecontract.ExplorationReader = (*ImportStore)(nil)

func (s *ImportStore) ListSessions(ctx context.Context, after *storagecontract.SessionCursor, limit int) ([]storagecontract.SessionSummary, bool, error) {
	if limit <= 0 {
		return nil, false, errors.New("sqlite exploration: list sessions: limit must be positive")
	}
	query := `
		SELECT s.id, s.title, s.summary, s.started_at, s.ended_at, s.source_id, COUNT(e.id)
		FROM sessions s LEFT JOIN events e ON e.session_id = s.id`
	args := make([]any, 0, 4)
	if after != nil {
		if after.StartedAt == nil {
			query += ` WHERE s.started_at IS NULL AND s.id > ?`
			args = append(args, after.ID)
		} else {
			encoded := after.StartedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
			query += ` WHERE s.started_at IS NULL OR s.started_at < ? OR (s.started_at = ? AND s.id > ?)`
			args = append(args, encoded, encoded, after.ID)
		}
	}
	query += ` GROUP BY s.id ORDER BY s.started_at DESC NULLS LAST, s.id ASC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("sqlite exploration: list sessions: %w", err)
	}
	defer rows.Close()
	items := make([]storagecontract.SessionSummary, 0, limit+1)
	for rows.Next() {
		var item storagecontract.SessionSummary
		var started, ended sql.NullString
		if err := rows.Scan(&item.ID, &item.Title, &item.Summary, &started, &ended, &item.SourceID, &item.EventCount); err != nil {
			return nil, false, fmt.Errorf("sqlite exploration: scan session: %w", err)
		}
		if item.StartedAt, err = decodeTime(started); err != nil {
			return nil, false, fmt.Errorf("sqlite exploration: decode session %q start: %w", item.ID, err)
		}
		if item.EndedAt, err = decodeTime(ended); err != nil {
			return nil, false, fmt.Errorf("sqlite exploration: decode session %q end: %w", item.ID, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("sqlite exploration: iterate sessions: %w", err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

func (s *ImportStore) SessionExists(ctx context.Context, sessionID model.SessionID) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ?`, sessionID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("sqlite exploration: find session %q: %w", sessionID, err)
	}
	return true, nil
}

func (s *ImportStore) EventSummaryPage(ctx context.Context, sessionID model.SessionID, after *int64, limit int) ([]model.EventSummary, bool, error) {
	if limit <= 0 {
		return nil, false, errors.New("sqlite exploration: timeline: limit must be positive")
	}
	query := `SELECT id, session_id, sequence, timestamp, kind, summary FROM events WHERE session_id = ?`
	args := []any{sessionID}
	if after != nil {
		query += ` AND sequence > ?`
		args = append(args, *after)
	}
	query += ` ORDER BY sequence LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("sqlite exploration: timeline for %q: %w", sessionID, err)
	}
	defer rows.Close()
	items := make([]model.EventSummary, 0, limit+1)
	for rows.Next() {
		var item model.EventSummary
		var timestamp sql.NullString
		if err := rows.Scan(&item.ID, &item.SessionID, &item.Sequence, &timestamp, &item.Kind, &item.Summary); err != nil {
			return nil, false, fmt.Errorf("sqlite exploration: scan timeline for %q: %w", sessionID, err)
		}
		if item.Timestamp, err = decodeTime(timestamp); err != nil {
			return nil, false, fmt.Errorf("sqlite exploration: decode event %q timestamp: %w", item.ID, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("sqlite exploration: iterate timeline for %q: %w", sessionID, err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

func (s *ImportStore) EventEnvelope(ctx context.Context, sessionID model.SessionID, eventID model.EventID) (storagecontract.EventEnvelope, bool, error) {
	var item storagecontract.EventEnvelope
	var timestamp sql.NullString
	var recordSequence, byteOffset, byteLength sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, sequence, timestamp, kind, summary, raw_record_id,
		       raw_source_id, raw_record_sequence, raw_byte_offset, raw_byte_length, raw_content_hash
		FROM events WHERE session_id = ? AND id = ?
	`, sessionID, eventID).Scan(
		&item.ID, &item.SessionID, &item.Sequence, &timestamp, &item.Kind, &item.Summary,
		&item.RawRecord.ID, &item.RawRecord.SourceID, &recordSequence, &byteOffset, &byteLength, &item.RawRecord.ContentHash,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storagecontract.EventEnvelope{}, false, nil
	}
	if err != nil {
		return storagecontract.EventEnvelope{}, false, fmt.Errorf("sqlite exploration: event envelope %q: %w", eventID, err)
	}
	if item.Timestamp, err = decodeTime(timestamp); err != nil {
		return storagecontract.EventEnvelope{}, false, fmt.Errorf("sqlite exploration: decode event %q timestamp: %w", eventID, err)
	}
	if recordSequence.Valid {
		value := recordSequence.Int64
		item.RawRecord.RecordSequence = &value
	}
	if byteOffset.Valid {
		item.RawRecord.ByteRange = &model.ByteRange{Offset: byteOffset.Int64, Length: byteLength.Int64}
	}
	return item, true, nil
}

func (s *ImportStore) EventPayload(ctx context.Context, sessionID model.SessionID, eventID model.EventID) (model.NormalizedData, bool, error) {
	var kind model.EventKind
	var encoded, payloadStorage string
	var policyVersion int
	err := s.db.QueryRowContext(ctx, `
		SELECT kind, data_json, payload_storage, retention_policy_version
		FROM events WHERE session_id = ? AND id = ?
	`, sessionID, eventID).Scan(&kind, &encoded, &payloadStorage, &policyVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("sqlite exploration: event payload %q: %w", eventID, err)
	}
	if policyVersion != storagecontract.FullRetentionPolicyVersion {
		return nil, true, fmt.Errorf("sqlite exploration: event payload %q: unsupported retention policy %d", eventID, policyVersion)
	}
	if payloadStorage == payloadDetached {
		var payload storagecontract.EncodedPayload
		err = s.db.QueryRowContext(ctx, `
			SELECT retention_policy_version, storage_encoding, original_size, content
			FROM event_payloads WHERE event_id = ?
		`, eventID).Scan(&payload.PolicyVersion, &payload.Encoding, &payload.OriginalSize, &payload.Content)
		if err != nil {
			return nil, true, fmt.Errorf("sqlite exploration: detached event payload %q: %w", eventID, err)
		}
		decoded, decodeErr := storagecontract.DecodePayload(payload)
		if decodeErr != nil {
			return nil, true, fmt.Errorf("sqlite exploration: decode event payload %q: %w", eventID, decodeErr)
		}
		encoded = string(decoded)
	} else if payloadStorage != payloadInline {
		return nil, true, fmt.Errorf("sqlite exploration: event payload %q: unsupported storage %q", eventID, payloadStorage)
	}
	data, err := decodeNormalizedData(kind, encoded)
	if err != nil {
		return nil, true, fmt.Errorf("sqlite exploration: event payload %q: %w", eventID, err)
	}
	return data, true, nil
}

func (s *ImportStore) Diagnostics(ctx context.Context, sessionID model.SessionID, eventID *model.EventID, limit int) (storagecontract.DiagnosticPage, error) {
	if limit < 0 {
		return storagecontract.DiagnosticPage{}, errors.New("sqlite exploration: diagnostics: limit must not be negative")
	}
	where := `session_id = ?`
	args := []any{sessionID}
	if eventID != nil {
		where += ` AND (EXISTS (SELECT 1 FROM json_each(event_ids_json) WHERE value = ?) OR EXISTS (
			SELECT 1 FROM events e WHERE e.id = ? AND e.raw_record_id IN (SELECT value FROM json_each(raw_record_ids_json))))`
		args = append(args, *eventID, *eventID)
	}
	union := `
		SELECT 0 AS source_order, position AS record_order, 0 AS item_order, '' AS tie_order,
		       code, severity, message, event_ids_json, raw_record_ids_json
		FROM session_diagnostics WHERE ` + where + `
		UNION ALL
		SELECT 1 AS source_order, COALESCE(r.record_sequence, r.byte_offset, 0) AS record_order,
		       d.ordinal AS item_order, d.raw_record_id AS tie_order,
		       d.code, d.severity, d.message, d.event_ids_json, d.raw_record_ids_json
		FROM record_diagnostics d JOIN raw_records r ON r.id = d.raw_record_id WHERE ` + strings.ReplaceAll(where, "session_id", "d.session_id")
	allArgs := append(append([]any(nil), args...), args...)
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+union+`)`, allArgs...).Scan(&total); err != nil {
		return storagecontract.DiagnosticPage{}, fmt.Errorf("sqlite exploration: count diagnostics for %q: %w", sessionID, err)
	}
	page := storagecontract.DiagnosticPage{Total: total}
	if limit == 0 || total == 0 {
		return page, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT code, severity, message, event_ids_json, raw_record_ids_json FROM (`+union+`) ORDER BY source_order, record_order, item_order, tie_order LIMIT ?`, append(allArgs, limit)...)
	if err != nil {
		return storagecontract.DiagnosticPage{}, fmt.Errorf("sqlite exploration: diagnostics for %q: %w", sessionID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var diagnostic model.Diagnostic
		var eventIDs, rawRecordIDs string
		if err := rows.Scan(&diagnostic.Code, &diagnostic.Severity, &diagnostic.Message, &eventIDs, &rawRecordIDs); err != nil {
			return storagecontract.DiagnosticPage{}, fmt.Errorf("sqlite exploration: scan diagnostic for %q: %w", sessionID, err)
		}
		if err := json.Unmarshal([]byte(eventIDs), &diagnostic.EventIDs); err != nil {
			return storagecontract.DiagnosticPage{}, fmt.Errorf("sqlite exploration: decode diagnostic events for %q: %w", sessionID, err)
		}
		if err := json.Unmarshal([]byte(rawRecordIDs), &diagnostic.RawRecordIDs); err != nil {
			return storagecontract.DiagnosticPage{}, fmt.Errorf("sqlite exploration: decode diagnostic records for %q: %w", sessionID, err)
		}
		page.Diagnostics = append(page.Diagnostics, diagnostic)
	}
	if err := rows.Err(); err != nil {
		return storagecontract.DiagnosticPage{}, fmt.Errorf("sqlite exploration: iterate diagnostics for %q: %w", sessionID, err)
	}
	return page, nil
}
