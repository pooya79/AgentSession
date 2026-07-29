package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	storagecontract "github.com/pooya79/AgentSession/internal/storage"
)

// Checkpoint returns the committed checkpoint for sourceID.
func (s *ImportStore) Checkpoint(ctx context.Context, sourceID model.SourceID) (importer.ImportCheckpoint, bool, error) {
	if strings.TrimSpace(string(sourceID)) == "" {
		return importer.ImportCheckpoint{}, false, errors.New("sqlite import store: read checkpoint: source ID is required")
	}
	checkpoint, found, err := selectCheckpoint(ctx, s.db, sourceID)
	if err != nil {
		return importer.ImportCheckpoint{}, false, fmt.Errorf("sqlite import store: read checkpoint for source %q: %w", sourceID, err)
	}
	return checkpoint, found, nil
}

// SourceState returns the checkpoint and canonical producer identity required
// to verify an append. Multiple sessions for one source are treated as corrupt
// state rather than selected arbitrarily.
func (s *ImportStore) SourceState(ctx context.Context, sourceID model.SourceID) (importer.SourceState, bool, error) {
	if strings.TrimSpace(string(sourceID)) == "" {
		return importer.SourceState{}, false, errors.New("sqlite import store: read source state: source ID is required")
	}
	checkpoint, found, err := selectCheckpoint(ctx, s.db, sourceID)
	if err != nil {
		return importer.SourceState{}, false, fmt.Errorf("sqlite import store: read source state for %q: checkpoint: %w", sourceID, err)
	}
	if !found {
		return importer.SourceState{}, false, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, adapter_name, adapter_version, format_version, model_version, normalization_version
		FROM sessions WHERE source_id = ?
		ORDER BY id
	`, sourceID)
	if err != nil {
		return importer.SourceState{}, false, fmt.Errorf("sqlite import store: read source state for %q: sessions: %w", sourceID, err)
	}
	defer rows.Close()
	var state importer.SourceState
	state.Checkpoint = checkpoint
	count := 0
	for rows.Next() {
		count++
		if count > 1 {
			return importer.SourceState{}, false, fmt.Errorf("sqlite import store: read source state for %q: multiple canonical sessions", sourceID)
		}
		state.Import.SourceID = sourceID
		if err := rows.Scan(
			&state.SessionID, &state.Import.AdapterName, &state.Import.AdapterVersion,
			&state.Import.FormatVersion, &state.Import.ModelVersion, &state.Import.NormalizationVersion,
		); err != nil {
			return importer.SourceState{}, false, fmt.Errorf("sqlite import store: read source state for %q: scan session: %w", sourceID, err)
		}
	}
	if err := rows.Err(); err != nil {
		return importer.SourceState{}, false, fmt.Errorf("sqlite import store: read source state for %q: iterate sessions: %w", sourceID, err)
	}
	if err := rows.Close(); err != nil {
		return importer.SourceState{}, false, fmt.Errorf("sqlite import store: read source state for %q: close sessions: %w", sourceID, err)
	}
	if count == 0 {
		return importer.SourceState{}, false, fmt.Errorf("sqlite import store: read source state for %q: checkpoint has no canonical session", sourceID)
	}
	session, sessionFound, err := s.Session(ctx, state.SessionID)
	if err != nil {
		return importer.SourceState{}, false, fmt.Errorf("sqlite import store: read source state for %q: canonical session: %w", sourceID, err)
	}
	if !sessionFound {
		return importer.SourceState{}, false, fmt.Errorf("sqlite import store: read source state for %q: canonical session disappeared", sourceID)
	}
	state.Session = session
	var last sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(sequence) FROM events WHERE session_id = ?`, state.SessionID).Scan(&last); err != nil {
		return importer.SourceState{}, false, fmt.Errorf("sqlite import store: read source state for %q: last event sequence: %w", sourceID, err)
	}
	if last.Valid {
		sequence := last.Int64
		state.LastEventSequence = &sequence
	}
	return state, true, nil
}

// RawRecord returns retained, untrusted source content without rendering it.
func (s *ImportStore) RawRecord(ctx context.Context, rawRecordID model.RawRecordID) (model.RawRecord, bool, error) {
	if strings.TrimSpace(string(rawRecordID)) == "" {
		return model.RawRecord{}, false, errors.New("sqlite import store: read raw record: raw record ID is required")
	}
	stored, found, err := selectStoredRawRecord(ctx, s.db, rawRecordID)
	if err != nil {
		return model.RawRecord{}, false, fmt.Errorf("sqlite import store: read raw record %q: %w", rawRecordID, err)
	}
	if !found {
		return model.RawRecord{}, false, nil
	}
	rawRecord, err := stored.toModel()
	if err != nil {
		return model.RawRecord{}, false, fmt.Errorf("sqlite import store: decode raw record %q: %w", rawRecordID, err)
	}
	return rawRecord, true, nil
}

func (r storedRawRecord) toModel() (model.RawRecord, error) {
	content, err := storagecontract.DecodePayload(storagecontract.EncodedPayload{
		PolicyVersion: r.PolicyVersion,
		Encoding:      r.Encoding,
		OriginalSize:  r.OriginalSize,
		Content:       r.Content,
	})
	if err != nil {
		return model.RawRecord{}, err
	}
	rawRecord := model.RawRecord{
		Ref: model.RawRecordRef{
			ID:          model.RawRecordID(r.ID),
			SourceID:    model.SourceID(r.SourceID),
			ContentHash: r.ContentHash,
		},
		Content: content,
	}
	if r.RecordSequence.Valid {
		value := r.RecordSequence.Int64
		rawRecord.Ref.RecordSequence = &value
	}
	if r.ByteOffset.Valid {
		rawRecord.Ref.ByteRange = &model.ByteRange{Offset: r.ByteOffset.Int64, Length: r.ByteLength.Int64}
	}
	if err := rawRecord.Validate(); err != nil {
		return model.RawRecord{}, fmt.Errorf("validate stored raw record: %w", err)
	}
	return rawRecord, nil
}

// Session returns a canonical session and its ordered diagnostic snapshot.
func (s *ImportStore) Session(ctx context.Context, sessionID model.SessionID) (model.Session, bool, error) {
	var session model.Session
	var startedAt, endedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, title, summary, started_at, ended_at, source_id,
		       adapter_name, adapter_version, format_version, model_version, normalization_version
		FROM sessions WHERE id = ?
	`, sessionID).Scan(
		&session.ID, &session.Title, &session.Summary, &startedAt, &endedAt, &session.Import.SourceID,
		&session.Import.AdapterName, &session.Import.AdapterVersion, &session.Import.FormatVersion,
		&session.Import.ModelVersion, &session.Import.NormalizationVersion,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Session{}, false, nil
	}
	if err != nil {
		return model.Session{}, false, fmt.Errorf("sqlite import store: read session %q: %w", sessionID, err)
	}
	if session.StartedAt, err = decodeTime(startedAt); err != nil {
		return model.Session{}, false, fmt.Errorf("sqlite import store: decode session %q start time: %w", sessionID, err)
	}
	if session.EndedAt, err = decodeTime(endedAt); err != nil {
		return model.Session{}, false, fmt.Errorf("sqlite import store: decode session %q end time: %w", sessionID, err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT code, severity, message, event_ids_json, raw_record_ids_json
		FROM session_diagnostics WHERE session_id = ? ORDER BY position
	`, sessionID)
	if err != nil {
		return model.Session{}, false, fmt.Errorf("sqlite import store: read diagnostics for session %q: %w", sessionID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var diagnostic model.Diagnostic
		var eventIDs, rawRecordIDs string
		if err := rows.Scan(&diagnostic.Code, &diagnostic.Severity, &diagnostic.Message, &eventIDs, &rawRecordIDs); err != nil {
			return model.Session{}, false, fmt.Errorf("sqlite import store: scan diagnostic for session %q: %w", sessionID, err)
		}
		if err := json.Unmarshal([]byte(eventIDs), &diagnostic.EventIDs); err != nil {
			return model.Session{}, false, fmt.Errorf("sqlite import store: decode diagnostic event IDs for session %q: %w", sessionID, err)
		}
		if err := json.Unmarshal([]byte(rawRecordIDs), &diagnostic.RawRecordIDs); err != nil {
			return model.Session{}, false, fmt.Errorf("sqlite import store: decode diagnostic raw record IDs for session %q: %w", sessionID, err)
		}
		session.Diagnostics = append(session.Diagnostics, diagnostic)
	}
	if err := rows.Err(); err != nil {
		return model.Session{}, false, fmt.Errorf("sqlite import store: iterate diagnostics for session %q: %w", sessionID, err)
	}
	return session, true, nil
}

// RecordDiagnostics returns incrementally persisted record-level diagnostics in
// source-record and per-record ordinal order.
func (s *ImportStore) RecordDiagnostics(ctx context.Context, sessionID model.SessionID) ([]model.RecordDiagnostic, error) {
	if strings.TrimSpace(string(sessionID)) == "" {
		return nil, errors.New("sqlite import store: read record diagnostics: session ID is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.raw_record_id, d.ordinal, d.code, d.severity, d.message, d.interpretation_reason,
		       d.event_ids_json, d.raw_record_ids_json
		FROM record_diagnostics d
		JOIN raw_records r ON r.id = d.raw_record_id
		WHERE d.session_id = ?
		ORDER BY COALESCE(r.record_sequence, r.byte_offset), d.ordinal
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("sqlite import store: read record diagnostics for session %q: %w", sessionID, err)
	}
	defer rows.Close()

	var diagnostics []model.RecordDiagnostic
	for rows.Next() {
		var diagnostic model.RecordDiagnostic
		var eventIDs, rawRecordIDs string
		if err := rows.Scan(
			&diagnostic.RawRecordID, &diagnostic.Ordinal, &diagnostic.Diagnostic.Code,
			&diagnostic.Diagnostic.Severity, &diagnostic.Diagnostic.Message, &diagnostic.Diagnostic.InterpretationReason, &eventIDs, &rawRecordIDs,
		); err != nil {
			return nil, fmt.Errorf("sqlite import store: scan record diagnostic for session %q: %w", sessionID, err)
		}
		if err := json.Unmarshal([]byte(eventIDs), &diagnostic.Diagnostic.EventIDs); err != nil {
			return nil, fmt.Errorf("sqlite import store: decode record diagnostic event IDs for session %q: %w", sessionID, err)
		}
		if err := json.Unmarshal([]byte(rawRecordIDs), &diagnostic.Diagnostic.RawRecordIDs); err != nil {
			return nil, fmt.Errorf("sqlite import store: decode record diagnostic raw record IDs for session %q: %w", sessionID, err)
		}
		if err := diagnostic.Validate(); err != nil {
			return nil, fmt.Errorf("sqlite import store: validate record diagnostic for session %q: %w", sessionID, err)
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite import store: iterate record diagnostics for session %q: %w", sessionID, err)
	}
	return diagnostics, nil
}

// EventSummaries returns the ordered timeline envelope without normalized or raw payloads.
func (s *ImportStore) EventSummaries(ctx context.Context, sessionID model.SessionID) ([]model.EventSummary, error) {
	if strings.TrimSpace(string(sessionID)) == "" {
		return nil, errors.New("sqlite import store: read event summaries: session ID is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, sequence, timestamp, kind, summary
		FROM events WHERE session_id = ? ORDER BY sequence
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("sqlite import store: read event summaries for session %q: %w", sessionID, err)
	}
	defer rows.Close()

	var summaries []model.EventSummary
	for rows.Next() {
		var summary model.EventSummary
		var timestamp sql.NullString
		if err := rows.Scan(&summary.ID, &summary.SessionID, &summary.Sequence, &timestamp, &summary.Kind, &summary.Summary); err != nil {
			return nil, fmt.Errorf("sqlite import store: scan event summary for session %q: %w", sessionID, err)
		}
		if summary.Timestamp, err = decodeTime(timestamp); err != nil {
			return nil, fmt.Errorf("sqlite import store: decode event summary %q timestamp: %w", summary.ID, err)
		}
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite import store: iterate event summaries for session %q: %w", sessionID, err)
	}
	return summaries, nil
}

// Event returns full normalized event detail, resolving detached payloads on demand.
func (s *ImportStore) Event(ctx context.Context, eventID model.EventID) (model.Event, bool, error) {
	if strings.TrimSpace(string(eventID)) == "" {
		return model.Event{}, false, errors.New("sqlite import store: read event: event ID is required")
	}
	stored, found, err := selectStoredEvent(ctx, s.db, eventID)
	if err != nil {
		return model.Event{}, false, fmt.Errorf("sqlite import store: read event %q: %w", eventID, err)
	}
	if !found {
		return model.Event{}, false, nil
	}
	event, err := stored.toModel()
	if err != nil {
		return model.Event{}, false, fmt.Errorf("sqlite import store: decode event %q: %w", eventID, err)
	}
	return event, true, nil
}

// DeleteSession removes AgentSession-owned data without consulting or modifying the source.
func (s *ImportStore) DeleteSession(ctx context.Context, sessionID model.SessionID) (deleted bool, err error) {
	if strings.TrimSpace(string(sessionID)) == "" {
		return false, errors.New("sqlite import store: delete session: session ID is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("sqlite import store: delete session %q: begin transaction: %w", sessionID, err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("sqlite import store: delete session %q: roll back transaction: %w", sessionID, rollbackErr))
		}
	}()

	var sourceID model.SourceID
	if err := tx.QueryRowContext(ctx, `SELECT source_id FROM sessions WHERE id = ?`, sessionID).Scan(&sourceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if err := tx.Commit(); err != nil {
				return false, fmt.Errorf("sqlite import store: delete missing session %q: commit transaction: %w", sessionID, err)
			}
			committed = true
			return false, nil
		}
		return false, fmt.Errorf("sqlite import store: delete session %q: resolve source: %w", sessionID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sessionID); err != nil {
		return false, fmt.Errorf("sqlite import store: delete session %q: remove owned data: %w", sessionID, err)
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE source_id = ?`, sourceID).Scan(&remaining); err != nil {
		return false, fmt.Errorf("sqlite import store: delete session %q: count remaining source sessions: %w", sessionID, err)
	}
	if remaining == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM reconciliation_runs WHERE source_id = ?`, sourceID); err != nil {
			return false, fmt.Errorf("sqlite import store: delete session %q: remove staged reconciliation: %w", sessionID, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM import_checkpoints WHERE source_id = ?`, sourceID); err != nil {
			return false, fmt.Errorf("sqlite import store: delete session %q: remove source checkpoint: %w", sessionID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("sqlite import store: delete session %q: commit transaction: %w", sessionID, err)
	}
	committed = true
	return true, nil
}

// Events returns full canonical events in stable source order.
func (s *ImportStore) Events(ctx context.Context, sessionID model.SessionID) ([]model.Event, error) {
	rows, err := s.db.QueryContext(ctx, storedEventSelect+` WHERE e.session_id = ? ORDER BY e.sequence`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("sqlite import store: read events for session %q: %w", sessionID, err)
	}
	defer rows.Close()

	var events []model.Event
	for rows.Next() {
		stored, err := scanStoredEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite import store: scan event for session %q: %w", sessionID, err)
		}
		event, err := stored.toModel()
		if err != nil {
			return nil, fmt.Errorf("sqlite import store: decode event %q for session %q: %w", stored.ID, sessionID, err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite import store: iterate events for session %q: %w", sessionID, err)
	}
	return events, nil
}

// EventPage streams a bounded source-ordered portion of canonical evidence.
func (s *ImportStore) EventPage(ctx context.Context, sessionID model.SessionID, after *int64, limit int) ([]model.Event, error) {
	if limit <= 0 {
		return nil, errors.New("sqlite import store: event page limit must be positive")
	}
	query := storedEventSelect + ` WHERE e.session_id = ?`
	args := []any{sessionID}
	if after != nil {
		query += ` AND e.sequence > ?`
		args = append(args, *after)
	}
	query += ` ORDER BY e.sequence LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite import store: read event page for session %q: %w", sessionID, err)
	}
	defer rows.Close()
	events := make([]model.Event, 0, limit)
	for rows.Next() {
		stored, err := scanStoredEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite import store: scan event page for session %q: %w", sessionID, err)
		}
		event, err := stored.toModel()
		if err != nil {
			return nil, fmt.Errorf("sqlite import store: decode event %q for session %q: %w", stored.ID, sessionID, err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite import store: iterate event page for session %q: %w", sessionID, err)
	}
	return events, nil
}

func (e storedEvent) toModel() (model.Event, error) {
	timestamp, err := decodeTimeString(e.Timestamp)
	if err != nil {
		return model.Event{}, fmt.Errorf("decode timestamp: %w", err)
	}
	encodedData := e.DataJSON
	switch e.PayloadStorage {
	case payloadInline:
		if e.Payload != nil {
			return model.Event{}, errors.New("inline event unexpectedly has a detached payload")
		}
	case payloadDetached:
		if e.Payload == nil {
			return model.Event{}, errors.New("detached event payload is missing")
		}
		decoded, err := storagecontract.DecodePayload(storagecontract.EncodedPayload{
			PolicyVersion: e.Payload.PolicyVersion,
			Encoding:      e.Payload.Encoding,
			OriginalSize:  e.Payload.OriginalSize,
			Content:       e.Payload.Content,
		})
		if err != nil {
			return model.Event{}, fmt.Errorf("decode detached normalized data: %w", err)
		}
		encodedData = string(decoded)
	default:
		return model.Event{}, fmt.Errorf("unsupported event payload storage %q", e.PayloadStorage)
	}
	if e.PolicyVersion != storagecontract.FullRetentionPolicyVersion {
		return model.Event{}, fmt.Errorf("unsupported event retention policy version %d", e.PolicyVersion)
	}

	kind := model.EventKind(e.Kind)
	data, err := decodeNormalizedData(kind, encodedData)
	if err != nil {
		return model.Event{}, err
	}
	event := model.Event{
		ID:             model.EventID(e.ID),
		SessionID:      model.SessionID(e.SessionID),
		Sequence:       e.Sequence,
		Timestamp:      timestamp,
		Kind:           kind,
		Summary:        e.Summary,
		SearchableText: e.SearchableText,
		Data:           data,
		RawRecord: model.RawRecordRef{
			ID:          model.RawRecordID(e.RawRecordID),
			SourceID:    model.SourceID(e.RawSourceID),
			ContentHash: e.RawContentHash,
		},
	}
	if e.RawRecordSequence.Valid {
		value := e.RawRecordSequence.Int64
		event.RawRecord.RecordSequence = &value
	}
	if e.RawByteOffset.Valid {
		event.RawRecord.ByteRange = &model.ByteRange{Offset: e.RawByteOffset.Int64, Length: e.RawByteLength.Int64}
	}
	if err := event.Validate(); err != nil {
		return model.Event{}, fmt.Errorf("validate stored event: %w", err)
	}
	return event, nil
}

func decodeNormalizedData(kind model.EventKind, encoded string) (model.NormalizedData, error) {
	var target model.NormalizedData
	switch kind {
	case model.EventKindMessage:
		target = &model.MessageData{}
	case model.EventKindToolCall:
		target = &model.ToolCallData{}
	case model.EventKindToolResult:
		target = &model.ToolResultData{}
	case model.EventKindCommand:
		target = &model.CommandData{}
	case model.EventKindFileRead:
		target = &model.FileReadData{}
	case model.EventKindFileMutation:
		target = &model.FileMutationData{}
	case model.EventKindPatch:
		target = &model.PatchData{}
	case model.EventKindUsage:
		target = &model.UsageData{}
	case model.EventKindError:
		target = &model.ErrorData{}
	case model.EventKindSummary:
		target = &model.SummaryData{}
	case model.EventKindUnknown:
		target = &model.UnknownData{}
	default:
		return nil, fmt.Errorf("unsupported stored event kind %q", kind)
	}
	if err := json.Unmarshal([]byte(encoded), target); err != nil {
		return nil, fmt.Errorf("decode %q normalized data: %w", kind, err)
	}
	return reflect.ValueOf(target).Elem().Interface().(model.NormalizedData), nil
}
