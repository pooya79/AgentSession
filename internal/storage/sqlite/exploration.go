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

// LibraryOverview returns exact library aggregates from committed canonical
// tables. UNION ensures a session with both diagnostic kinds is counted once.
func (s *ImportStore) LibraryOverview(ctx context.Context) (storagecontract.LibraryOverview, error) {
	var overview storagecontract.LibraryOverview
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM sessions),
			(SELECT COUNT(*) FROM events),
			(SELECT COUNT(DISTINCT adapter_name) FROM sessions),
			(SELECT COUNT(*) FROM (
				SELECT session_id FROM session_diagnostics
				UNION
				SELECT session_id FROM record_diagnostics
			))
	`).Scan(&overview.Sessions, &overview.Events, &overview.Agents, &overview.IssueSessions)
	if err != nil {
		return storagecontract.LibraryOverview{}, fmt.Errorf("sqlite exploration: library overview: %w", err)
	}
	return overview, nil
}

// ListSessions returns a bounded keyset page ordered by activity descending,
// placing sessions with unknown activity last and preserving stable ID ordering.
func (s *ImportStore) ListSessions(ctx context.Context, after *storagecontract.SessionCursor, limit int) ([]storagecontract.SessionSummary, bool, error) {
	if limit <= 0 {
		return nil, false, errors.New("sqlite exploration: list sessions: limit must be positive")
	}
	query := `
		SELECT s.id, s.title, s.summary, s.started_at, s.ended_at, s.last_activity_at,
		       s.source_id, s.adapter_name, s.first_user_message, s.event_count
		FROM sessions s`
	args := make([]any, 0, 4)
	if after != nil {
		// The keyset mirrors ORDER BY exactly. NULL activity sorts last, so a
		// cursor in that partition advances by ID without revisiting dated
		// sessions.
		if after.Before {
			if after.LastActivityAt == nil {
				query += ` WHERE s.last_activity_at IS NOT NULL OR (s.last_activity_at IS NULL AND s.id < ?)`
				args = append(args, after.ID)
			} else {
				encoded := after.LastActivityAt.UTC().Format("2006-01-02T15:04:05.000000000Z")
				query += ` WHERE s.last_activity_at > ? OR (s.last_activity_at = ? AND s.id < ?)`
				args = append(args, encoded, encoded, after.ID)
			}
		} else if after.LastActivityAt == nil {
			query += ` WHERE s.last_activity_at IS NULL AND s.id > ?`
			args = append(args, after.ID)
		} else {
			encoded := after.LastActivityAt.UTC().Format("2006-01-02T15:04:05.000000000Z")
			query += ` WHERE s.last_activity_at IS NULL OR s.last_activity_at < ? OR (s.last_activity_at = ? AND s.id > ?)`
			args = append(args, encoded, encoded, after.ID)
		}
	}
	if after != nil && after.Before {
		query += ` ORDER BY s.last_activity_at ASC NULLS FIRST, s.id DESC LIMIT ?`
	} else {
		query += ` ORDER BY s.last_activity_at DESC NULLS LAST, s.id ASC LIMIT ?`
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("sqlite exploration: list sessions: %w", err)
	}
	defer rows.Close()
	items := make([]storagecontract.SessionSummary, 0, limit+1)
	for rows.Next() {
		var item storagecontract.SessionSummary
		var started, ended, activity sql.NullString
		if err := rows.Scan(
			&item.ID, &item.Title, &item.Summary, &started, &ended, &activity,
			&item.SourceID, &item.AgentName, &item.FirstUserMessage, &item.EventCount,
		); err != nil {
			return nil, false, fmt.Errorf("sqlite exploration: scan session: %w", err)
		}
		if item.StartedAt, err = decodeTime(started); err != nil {
			return nil, false, fmt.Errorf("sqlite exploration: decode session %q start: %w", item.ID, err)
		}
		if item.EndedAt, err = decodeTime(ended); err != nil {
			return nil, false, fmt.Errorf("sqlite exploration: decode session %q end: %w", item.ID, err)
		}
		if item.LastActivityAt, err = decodeTime(activity); err != nil {
			return nil, false, fmt.Errorf("sqlite exploration: decode session %q last activity: %w", item.ID, err)
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
	if after != nil && after.Before {
		for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
			items[left], items[right] = items[right], items[left]
		}
	}
	return items, hasMore, nil
}

// SessionExists checks retained canonical metadata without loading session events.
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

// EventSummaryPage returns canonical events in source order without normalized payloads.
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

// EventSummaryWindow returns an ordered bounded window ending at a focused sequence.
func (s *ImportStore) EventSummaryWindow(ctx context.Context, sessionID model.SessionID, endingAt int64, limit int) (items []model.EventSummary, hasMore bool, err error) {
	if limit <= 0 {
		return nil, false, errors.New("sqlite exploration: timeline window: limit must be positive")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, sequence, timestamp, kind, summary
		FROM events WHERE session_id = ? AND sequence <= ?
		ORDER BY sequence DESC LIMIT ?
	`, sessionID, endingAt, limit)
	if err != nil {
		return nil, false, fmt.Errorf("sqlite exploration: timeline window for %q: %w", sessionID, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			closeErr = fmt.Errorf("sqlite exploration: close timeline window for %q: %w", sessionID, closeErr)
			err = errors.Join(err, closeErr)
		}
	}()
	items = make([]model.EventSummary, 0, limit)
	for rows.Next() {
		var item model.EventSummary
		var timestamp sql.NullString
		if err := rows.Scan(&item.ID, &item.SessionID, &item.Sequence, &timestamp, &item.Kind, &item.Summary); err != nil {
			return nil, false, fmt.Errorf("sqlite exploration: scan timeline window for %q: %w", sessionID, err)
		}
		if item.Timestamp, err = decodeTime(timestamp); err != nil {
			return nil, false, fmt.Errorf("sqlite exploration: decode event %q timestamp: %w", item.ID, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("sqlite exploration: iterate timeline window for %q: %w", sessionID, err)
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	var later int
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE session_id = ? AND sequence > ?)`, sessionID, endingAt).Scan(&later); err != nil {
		return nil, false, fmt.Errorf("sqlite exploration: check later timeline events for %q: %w", sessionID, err)
	}
	return items, later != 0, nil
}

// EventLocations resolves event ownership and ordering in one bounded query.
func (s *ImportStore) EventLocations(ctx context.Context, eventIDs []model.EventID) (map[model.EventID]storagecontract.EventLocation, error) {
	locations := make(map[model.EventID]storagecontract.EventLocation, len(eventIDs))
	if len(eventIDs) == 0 {
		return locations, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(eventIDs)), ",")
	args := make([]any, len(eventIDs))
	for i, id := range eventIDs {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, sequence FROM events WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite exploration: locate events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var location storagecontract.EventLocation
		if err := rows.Scan(&location.EventID, &location.SessionID, &location.Sequence); err != nil {
			return nil, fmt.Errorf("sqlite exploration: scan event location: %w", err)
		}
		locations[location.EventID] = location
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite exploration: iterate event locations: %w", err)
	}
	return locations, nil
}

// EventEnvelope reads lightweight event and raw-record references without payload content.
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

// EventPayload loads and decodes normalized data only for an explicitly selected event.
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

// EventPayloads loads normalized payloads for one bounded set of events. The
// query selects only normalized inline/detached payload columns and never
// joins retained raw records.
func (s *ImportStore) EventPayloads(ctx context.Context, sessionID model.SessionID, eventIDs []model.EventID) (map[model.EventID]model.NormalizedData, error) {
	const maximumBatchSize = 200
	payloads := make(map[model.EventID]model.NormalizedData, len(eventIDs))
	if len(eventIDs) == 0 {
		return payloads, nil
	}
	if len(eventIDs) > maximumBatchSize {
		return nil, fmt.Errorf("sqlite exploration: event payload batch exceeds %d events", maximumBatchSize)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(eventIDs)), ",")
	args := make([]any, 0, len(eventIDs)+1)
	args = append(args, sessionID)
	for _, eventID := range eventIDs {
		args = append(args, eventID)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.kind, e.data_json, e.payload_storage, e.retention_policy_version,
		       p.retention_policy_version, p.storage_encoding, p.original_size, p.content
		FROM events e
		LEFT JOIN event_payloads p ON p.event_id = e.id
		WHERE e.session_id = ? AND e.id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite exploration: event payload batch: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var eventID model.EventID
		var kind model.EventKind
		var encoded, payloadStorage string
		var policyVersion int
		var detachedPolicy, originalSize sql.NullInt64
		var storageEncoding sql.NullString
		var content []byte
		if err := rows.Scan(
			&eventID, &kind, &encoded, &payloadStorage, &policyVersion,
			&detachedPolicy, &storageEncoding, &originalSize, &content,
		); err != nil {
			return nil, fmt.Errorf("sqlite exploration: scan event payload batch: %w", err)
		}
		if policyVersion != storagecontract.FullRetentionPolicyVersion {
			return nil, fmt.Errorf("sqlite exploration: event payload %q: unsupported retention policy %d", eventID, policyVersion)
		}
		switch payloadStorage {
		case payloadInline:
		case payloadDetached:
			if !detachedPolicy.Valid || !storageEncoding.Valid || !originalSize.Valid {
				return nil, fmt.Errorf("sqlite exploration: detached event payload %q: missing payload", eventID)
			}
			decoded, decodeErr := storagecontract.DecodePayload(storagecontract.EncodedPayload{
				PolicyVersion: int(detachedPolicy.Int64),
				Encoding:      storageEncoding.String,
				OriginalSize:  originalSize.Int64,
				Content:       content,
			})
			if decodeErr != nil {
				return nil, fmt.Errorf("sqlite exploration: decode event payload %q: %w", eventID, decodeErr)
			}
			encoded = string(decoded)
		default:
			return nil, fmt.Errorf("sqlite exploration: event payload %q: unsupported storage %q", eventID, payloadStorage)
		}
		data, decodeErr := decodeNormalizedData(kind, encoded)
		if decodeErr != nil {
			return nil, fmt.Errorf("sqlite exploration: event payload %q: %w", eventID, decodeErr)
		}
		payloads[eventID] = data
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite exploration: iterate event payload batch: %w", err)
	}
	return payloads, nil
}

// Diagnostics returns an exact total and a bounded, deterministically ordered sample.
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
		       code, severity, message, '' AS interpretation_reason, event_ids_json, raw_record_ids_json
		FROM session_diagnostics WHERE ` + where + `
		UNION ALL
		SELECT 1 AS source_order, COALESCE(r.record_sequence, r.byte_offset, 0) AS record_order,
		       d.ordinal AS item_order, d.raw_record_id AS tie_order,
		       d.code, d.severity, d.message, d.interpretation_reason, d.event_ids_json, d.raw_record_ids_json
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
	rows, err := s.db.QueryContext(ctx, `SELECT code, severity, message, interpretation_reason, event_ids_json, raw_record_ids_json FROM (`+union+`) ORDER BY source_order, record_order, item_order, tie_order LIMIT ?`, append(allArgs, limit)...)
	if err != nil {
		return storagecontract.DiagnosticPage{}, fmt.Errorf("sqlite exploration: diagnostics for %q: %w", sessionID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var diagnostic model.Diagnostic
		var eventIDs, rawRecordIDs string
		if err := rows.Scan(&diagnostic.Code, &diagnostic.Severity, &diagnostic.Message, &diagnostic.InterpretationReason, &eventIDs, &rawRecordIDs); err != nil {
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

// InterpretationCoverage derives exact coverage counts from committed
// canonical evidence instead of persisting a mutable session flag.
func (s *ImportStore) InterpretationCoverage(ctx context.Context, sessionID model.SessionID) (storagecontract.InterpretationCoverage, error) {
	var coverage storagecontract.InterpretationCoverage
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM events WHERE session_id = ? AND kind = ?),
			(SELECT COUNT(DISTINCT raw_record_id) FROM record_diagnostics
			 WHERE session_id = ? AND interpretation_reason IN (?, ?))
	`, sessionID, model.EventKindUnknown, sessionID,
		model.InterpretationMissingDiscriminant, model.InterpretationStructurallyInvalidKnownRecord,
	).Scan(&coverage.UnknownEvents, &coverage.MalformedRecords)
	if err != nil {
		return storagecontract.InterpretationCoverage{}, fmt.Errorf("sqlite exploration: interpretation coverage for %q: %w", sessionID, err)
	}
	return coverage, nil
}

// RawRecordPrefix lazily decodes only the requested retained-record prefix.
func (s *ImportStore) RawRecordPrefix(ctx context.Context, sessionID model.SessionID, rawRecordID model.RawRecordID, limit int64) (storagecontract.RawRecordPrefix, bool, error) {
	if limit < 0 {
		return storagecontract.RawRecordPrefix{}, false, errors.New("sqlite exploration: raw record prefix limit must not be negative")
	}
	var payload storagecontract.EncodedPayload
	err := s.db.QueryRowContext(ctx, `
		SELECT retention_policy_version, storage_encoding, original_size, content
		FROM raw_records WHERE session_id = ? AND id = ?
	`, sessionID, rawRecordID).Scan(&payload.PolicyVersion, &payload.Encoding, &payload.OriginalSize, &payload.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return storagecontract.RawRecordPrefix{}, false, nil
	}
	if err != nil {
		return storagecontract.RawRecordPrefix{}, false, fmt.Errorf("sqlite exploration: raw record prefix %q: %w", rawRecordID, err)
	}
	content, err := storagecontract.DecodePayloadPrefix(payload, limit)
	if err != nil {
		return storagecontract.RawRecordPrefix{}, true, fmt.Errorf("sqlite exploration: decode raw record prefix %q: %w", rawRecordID, err)
	}
	return storagecontract.RawRecordPrefix{Content: content, OriginalSize: payload.OriginalSize}, true, nil
}
