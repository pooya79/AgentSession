package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	storagecontract "github.com/pooya79/AgentSession/internal/storage"
)

type storedRawRecord struct {
	ID             string
	SessionID      string
	SourceID       string
	RecordSequence sql.NullInt64
	ByteOffset     sql.NullInt64
	ByteLength     sql.NullInt64
	ContentHash    string
	PolicyVersion  int
	Encoding       string
	OriginalSize   int64
	Content        []byte
}

func rawRecordForStorage(sessionID model.SessionID, rawRecord model.RawRecord) (storedRawRecord, error) {
	encoded, err := storagecontract.EncodePayload(rawRecord.Content)
	if err != nil {
		return storedRawRecord{}, err
	}
	stored := storedRawRecord{
		ID:            string(rawRecord.Ref.ID),
		SessionID:     string(sessionID),
		SourceID:      string(rawRecord.Ref.SourceID),
		ContentHash:   rawRecord.Ref.ContentHash,
		PolicyVersion: encoded.PolicyVersion,
		Encoding:      encoded.Encoding,
		OriginalSize:  encoded.OriginalSize,
		Content:       encoded.Content,
	}
	if rawRecord.Ref.RecordSequence != nil {
		stored.RecordSequence = sql.NullInt64{Int64: *rawRecord.Ref.RecordSequence, Valid: true}
	}
	if rawRecord.Ref.ByteRange != nil {
		stored.ByteOffset = sql.NullInt64{Int64: rawRecord.Ref.ByteRange.Offset, Valid: true}
		stored.ByteLength = sql.NullInt64{Int64: rawRecord.Ref.ByteRange.Length, Valid: true}
	}
	return stored, nil
}

func persistRawRecord(ctx context.Context, tx *sql.Tx, sessionID model.SessionID, rawRecord model.RawRecord) (bool, error) {
	stored, err := rawRecordForStorage(sessionID, rawRecord)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO raw_records (
			id, session_id, source_id, record_sequence, byte_offset, byte_length,
			content_hash, storage_encoding, original_size, content, retention_policy_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING
	`, stored.ID, stored.SessionID, stored.SourceID, nullableInt(stored.RecordSequence),
		nullableInt(stored.ByteOffset), nullableInt(stored.ByteLength), stored.ContentHash,
		stored.Encoding, stored.OriginalSize, stored.Content, stored.PolicyVersion)
	if err != nil {
		return false, fmt.Errorf("insert raw record: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect raw record insert: %w", err)
	}
	if rows == 1 {
		return true, nil
	}
	existing, found, err := selectStoredRawRecord(ctx, tx, rawRecord.Ref.ID)
	if err != nil {
		return false, fmt.Errorf("load duplicate raw record: %w", err)
	}
	if !found || !storedRawRecordEqual(existing, stored) {
		return false, fmt.Errorf("%w: raw record ID %q has different source content", importer.ErrRawRecordConflict, rawRecord.Ref.ID)
	}
	return false, nil
}

func storedRawRecordEqual(left, right storedRawRecord) bool {
	return left.ID == right.ID && left.SessionID == right.SessionID && left.SourceID == right.SourceID &&
		left.RecordSequence == right.RecordSequence && left.ByteOffset == right.ByteOffset &&
		left.ByteLength == right.ByteLength && left.ContentHash == right.ContentHash &&
		left.PolicyVersion == right.PolicyVersion && left.Encoding == right.Encoding && left.OriginalSize == right.OriginalSize &&
		bytes.Equal(left.Content, right.Content)
}

func selectStoredRawRecord(ctx context.Context, queryer rowQueryer, rawRecordID model.RawRecordID) (storedRawRecord, bool, error) {
	var rawRecord storedRawRecord
	err := queryer.QueryRowContext(ctx, `
		SELECT id, session_id, source_id, record_sequence, byte_offset, byte_length,
		       content_hash, retention_policy_version, storage_encoding, original_size, content
		FROM raw_records WHERE id = ?
	`, rawRecordID).Scan(
		&rawRecord.ID, &rawRecord.SessionID, &rawRecord.SourceID, &rawRecord.RecordSequence,
		&rawRecord.ByteOffset, &rawRecord.ByteLength, &rawRecord.ContentHash, &rawRecord.PolicyVersion, &rawRecord.Encoding,
		&rawRecord.OriginalSize, &rawRecord.Content,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedRawRecord{}, false, nil
	}
	if err != nil {
		return storedRawRecord{}, false, err
	}
	return rawRecord, true, nil
}

type storedEvent struct {
	ID                string
	SessionID         string
	Sequence          int64
	Timestamp         string
	Kind              string
	Summary           string
	SearchableText    string
	DataJSON          string
	MessageRole       string
	PolicyVersion     int
	PayloadStorage    string
	Payload           *storedEventPayload
	RawRecordID       string
	RawSourceID       string
	RawRecordSequence sql.NullInt64
	RawByteOffset     sql.NullInt64
	RawByteLength     sql.NullInt64
	RawContentHash    string
}

type storedEventPayload struct {
	PolicyVersion int
	Encoding      string
	OriginalSize  int64
	Content       []byte
}

func eventForStorage(event model.Event) (storedEvent, error) {
	dataJSON, err := json.Marshal(event.Data)
	if err != nil {
		return storedEvent{}, fmt.Errorf("encode normalized data: %w", err)
	}
	stored := storedEvent{
		ID:             string(event.ID),
		SessionID:      string(event.SessionID),
		Sequence:       event.Sequence,
		Timestamp:      timeString(event.Timestamp),
		Kind:           string(event.Kind),
		Summary:        event.Summary,
		SearchableText: event.SearchableText,
		DataJSON:       string(dataJSON),
		PolicyVersion:  storagecontract.FullRetentionPolicyVersion,
		PayloadStorage: payloadInline,
		RawRecordID:    string(event.RawRecord.ID),
		RawSourceID:    string(event.RawRecord.SourceID),
		RawContentHash: event.RawRecord.ContentHash,
	}
	if message, ok := event.Data.(model.MessageData); ok {
		stored.MessageRole = string(message.Role)
	}
	if len(dataJSON) > storagecontract.InlinePayloadThresholdBytes {
		// Keep timeline rows lightweight while retaining the complete
		// normalized payload in the transactionally owned side table.
		encoded, err := storagecontract.EncodePayload(dataJSON)
		if err != nil {
			return storedEvent{}, fmt.Errorf("encode detached normalized data: %w", err)
		}
		stored.DataJSON = ""
		stored.PayloadStorage = payloadDetached
		stored.Payload = &storedEventPayload{
			PolicyVersion: encoded.PolicyVersion,
			Encoding:      encoded.Encoding,
			OriginalSize:  encoded.OriginalSize,
			Content:       encoded.Content,
		}
	}
	if event.RawRecord.RecordSequence != nil {
		stored.RawRecordSequence = sql.NullInt64{Int64: *event.RawRecord.RecordSequence, Valid: true}
	}
	if event.RawRecord.ByteRange != nil {
		stored.RawByteOffset = sql.NullInt64{Int64: event.RawRecord.ByteRange.Offset, Valid: true}
		stored.RawByteLength = sql.NullInt64{Int64: event.RawRecord.ByteRange.Length, Valid: true}
	}
	return stored, nil
}

func persistEvent(ctx context.Context, tx *sql.Tx, event model.Event) (bool, error) {
	stored, err := eventForStorage(event)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO events (
			id, session_id, sequence, timestamp, kind, summary, searchable_text, data_json, message_role,
			raw_record_id, raw_source_id, raw_record_sequence, raw_byte_offset, raw_byte_length, raw_content_hash,
			retention_policy_version, payload_storage
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING
	`, stored.values()...)
	if err != nil {
		return false, fmt.Errorf("insert event: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect event insert: %w", err)
	}
	if rows == 1 {
		if stored.Payload != nil {
			if err := persistEventPayload(ctx, tx, event.ID, *stored.Payload); err != nil {
				return false, err
			}
		}
		return true, nil
	}

	existing, found, err := selectStoredEvent(ctx, tx, event.ID)
	if err != nil {
		return false, fmt.Errorf("load duplicate event: %w", err)
	}
	if !found || !reflect.DeepEqual(existing, stored) {
		return false, fmt.Errorf("%w: event ID %q has different canonical content", importer.ErrEventConflict, event.ID)
	}
	return false, nil
}

func (e storedEvent) values() []any {
	return []any{
		e.ID, e.SessionID, e.Sequence, nullIfEmpty(e.Timestamp), e.Kind, e.Summary, e.SearchableText, e.DataJSON, e.MessageRole,
		e.RawRecordID, e.RawSourceID, nullableInt(e.RawRecordSequence), nullableInt(e.RawByteOffset),
		nullableInt(e.RawByteLength), e.RawContentHash, e.PolicyVersion, e.PayloadStorage,
	}
}

func persistEventPayload(ctx context.Context, tx *sql.Tx, eventID model.EventID, payload storedEventPayload) error {
	if payload.Encoding != storagecontract.EncodingZlib {
		return fmt.Errorf("detached event payload %q is not compressed", eventID)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO event_payloads (
			event_id, retention_policy_version, storage_encoding, original_size, content
		) VALUES (?, ?, ?, ?, ?)
	`, eventID, payload.PolicyVersion, payload.Encoding, payload.OriginalSize, payload.Content)
	if err != nil {
		return fmt.Errorf("insert detached event payload: %w", err)
	}
	return nil
}

func selectStoredEvent(ctx context.Context, queryer rowQueryer, eventID model.EventID) (storedEvent, bool, error) {
	event, err := scanStoredEvent(queryer.QueryRowContext(ctx, storedEventSelect+` WHERE e.id = ?`, eventID))
	if errors.Is(err, sql.ErrNoRows) {
		return storedEvent{}, false, nil
	}
	if err != nil {
		return storedEvent{}, false, err
	}
	return event, true, nil
}

const storedEventSelect = `
	SELECT e.id, e.session_id, e.sequence, e.timestamp, e.kind, e.summary, e.searchable_text, e.data_json, e.message_role,
	       e.raw_record_id, e.raw_source_id, e.raw_record_sequence, e.raw_byte_offset, e.raw_byte_length,
	       e.raw_content_hash, e.retention_policy_version, e.payload_storage,
	       p.retention_policy_version, p.storage_encoding, p.original_size, p.content
	FROM events e LEFT JOIN event_payloads p ON p.event_id = e.id`

func scanStoredEvent(scanner rowScanner) (storedEvent, error) {
	var event storedEvent
	var timestamp, payloadEncoding sql.NullString
	var payloadPolicy, payloadSize sql.NullInt64
	var payloadContent []byte
	if err := scanner.Scan(
		&event.ID, &event.SessionID, &event.Sequence, &timestamp, &event.Kind, &event.Summary,
		&event.SearchableText, &event.DataJSON, &event.MessageRole, &event.RawRecordID, &event.RawSourceID,
		&event.RawRecordSequence, &event.RawByteOffset, &event.RawByteLength, &event.RawContentHash,
		&event.PolicyVersion, &event.PayloadStorage, &payloadPolicy, &payloadEncoding, &payloadSize, &payloadContent,
	); err != nil {
		return storedEvent{}, err
	}
	if timestamp.Valid {
		event.Timestamp = timestamp.String
	}
	if payloadPolicy.Valid || payloadEncoding.Valid || payloadSize.Valid || payloadContent != nil {
		if !payloadPolicy.Valid || !payloadEncoding.Valid || !payloadSize.Valid || payloadContent == nil {
			return storedEvent{}, errors.New("detached event payload metadata is incomplete")
		}
		event.Payload = &storedEventPayload{
			PolicyVersion: int(payloadPolicy.Int64),
			Encoding:      payloadEncoding.String,
			OriginalSize:  payloadSize.Int64,
			Content:       payloadContent,
		}
	}
	return event, nil
}

func replaceDiagnostics(ctx context.Context, tx *sql.Tx, session model.Session) (bool, error) {
	existing, err := selectSessionDiagnostics(ctx, tx, session.ID)
	if err != nil {
		return false, err
	}
	changed := !reflect.DeepEqual(existing, session.Diagnostics)
	if !changed {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_diagnostics WHERE session_id = ?`, session.ID); err != nil {
		return false, fmt.Errorf("delete diagnostics for session %q: %w", session.ID, err)
	}
	for i, diagnostic := range session.Diagnostics {
		eventIDs, err := json.Marshal(diagnostic.EventIDs)
		if err != nil {
			return false, fmt.Errorf("encode diagnostic %d event IDs: %w", i, err)
		}
		rawRecordIDs, err := json.Marshal(diagnostic.RawRecordIDs)
		if err != nil {
			return false, fmt.Errorf("encode diagnostic %d raw record IDs: %w", i, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO session_diagnostics (
				session_id, position, code, severity, message, event_ids_json, raw_record_ids_json
			) VALUES (?, ?, ?, ?, ?, ?, ?)
		`, session.ID, i, diagnostic.Code, diagnostic.Severity, diagnostic.Message, string(eventIDs), string(rawRecordIDs)); err != nil {
			return false, fmt.Errorf("insert diagnostic %d: %w", i, err)
		}
	}
	return true, nil
}

func selectSessionDiagnostics(ctx context.Context, tx *sql.Tx, sessionID model.SessionID) ([]model.Diagnostic, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT code, severity, message, event_ids_json, raw_record_ids_json
		FROM session_diagnostics WHERE session_id = ? ORDER BY position
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("read diagnostics for session %q: %w", sessionID, err)
	}
	defer rows.Close()
	var diagnostics []model.Diagnostic
	for rows.Next() {
		var diagnostic model.Diagnostic
		var eventIDs, rawRecordIDs string
		if err := rows.Scan(&diagnostic.Code, &diagnostic.Severity, &diagnostic.Message, &eventIDs, &rawRecordIDs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(eventIDs), &diagnostic.EventIDs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(rawRecordIDs), &diagnostic.RawRecordIDs); err != nil {
			return nil, err
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	return diagnostics, rows.Err()
}

type storedRecordDiagnostic struct {
	SessionID            string
	RawRecordID          string
	Ordinal              int64
	Code                 string
	Severity             string
	Message              string
	InterpretationReason string
	EventIDsJSON         string
	RawRecordIDsJSON     string
}

func recordDiagnosticForStorage(sessionID model.SessionID, diagnostic model.RecordDiagnostic) (storedRecordDiagnostic, error) {
	eventIDs, err := json.Marshal(diagnostic.Diagnostic.EventIDs)
	if err != nil {
		return storedRecordDiagnostic{}, fmt.Errorf("encode event IDs: %w", err)
	}
	rawRecordIDs, err := json.Marshal(diagnostic.Diagnostic.RawRecordIDs)
	if err != nil {
		return storedRecordDiagnostic{}, fmt.Errorf("encode raw record IDs: %w", err)
	}
	return storedRecordDiagnostic{
		SessionID:            string(sessionID),
		RawRecordID:          string(diagnostic.RawRecordID),
		Ordinal:              diagnostic.Ordinal,
		Code:                 diagnostic.Diagnostic.Code,
		Severity:             string(diagnostic.Diagnostic.Severity),
		Message:              diagnostic.Diagnostic.Message,
		InterpretationReason: string(diagnostic.Diagnostic.InterpretationReason),
		EventIDsJSON:         string(eventIDs),
		RawRecordIDsJSON:     string(rawRecordIDs),
	}, nil
}

func persistRecordDiagnostic(ctx context.Context, tx *sql.Tx, sessionID model.SessionID, diagnostic model.RecordDiagnostic) (bool, error) {
	stored, err := recordDiagnosticForStorage(sessionID, diagnostic)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO record_diagnostics (
			session_id, raw_record_id, ordinal, code, severity, message, interpretation_reason, event_ids_json, raw_record_ids_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(raw_record_id, ordinal) DO NOTHING
	`, stored.SessionID, stored.RawRecordID, stored.Ordinal, stored.Code, stored.Severity, stored.Message,
		stored.InterpretationReason, stored.EventIDsJSON, stored.RawRecordIDsJSON)
	if err != nil {
		return false, fmt.Errorf("insert record diagnostic: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect record diagnostic insert: %w", err)
	}
	if rows == 1 {
		return true, nil
	}
	existing, found, err := selectStoredRecordDiagnostic(ctx, tx, diagnostic.RawRecordID, diagnostic.Ordinal)
	if err != nil {
		return false, fmt.Errorf("load duplicate record diagnostic: %w", err)
	}
	if !found || existing != stored {
		return false, fmt.Errorf("%w: raw record %q ordinal %d has different diagnostic content", importer.ErrDiagnosticConflict, diagnostic.RawRecordID, diagnostic.Ordinal)
	}
	return false, nil
}

func selectStoredRecordDiagnostic(ctx context.Context, queryer rowQueryer, rawRecordID model.RawRecordID, ordinal int64) (storedRecordDiagnostic, bool, error) {
	var diagnostic storedRecordDiagnostic
	err := queryer.QueryRowContext(ctx, `
		SELECT session_id, raw_record_id, ordinal, code, severity, message, interpretation_reason, event_ids_json, raw_record_ids_json
		FROM record_diagnostics WHERE raw_record_id = ? AND ordinal = ?
	`, rawRecordID, ordinal).Scan(
		&diagnostic.SessionID, &diagnostic.RawRecordID, &diagnostic.Ordinal, &diagnostic.Code,
		&diagnostic.Severity, &diagnostic.Message, &diagnostic.InterpretationReason, &diagnostic.EventIDsJSON, &diagnostic.RawRecordIDsJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedRecordDiagnostic{}, false, nil
	}
	if err != nil {
		return storedRecordDiagnostic{}, false, err
	}
	return diagnostic, true, nil
}

func persistCheckpoint(ctx context.Context, tx *sql.Tx, checkpoint importer.ImportCheckpoint) (bool, error) {
	existing, found, err := selectCheckpoint(ctx, tx, checkpoint.SourceID)
	if err != nil {
		return false, fmt.Errorf("load current checkpoint: %w", err)
	}
	if found && checkpoint.RecordSequence < existing.RecordSequence {
		return false, fmt.Errorf(
			"%w: source %q record sequence %d is behind %d",
			importer.ErrCheckpointRegression,
			checkpoint.SourceID,
			checkpoint.RecordSequence,
			existing.RecordSequence,
		)
	}
	if found && checkpoint.RecordSequence == existing.RecordSequence && !importer.CheckpointEqual(checkpoint, existing) {
		return false, fmt.Errorf("%w: source %q fingerprints changed at the committed cursor", importer.ErrCheckpointRegression, checkpoint.SourceID)
	}
	changed := !found || !importer.CheckpointEqual(checkpoint, existing)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO import_checkpoints (
			source_id, record_sequence, state_version, cursor, fingerprint
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(source_id) DO UPDATE SET
			record_sequence = excluded.record_sequence,
			state_version = excluded.state_version,
			cursor = excluded.cursor,
			fingerprint = excluded.fingerprint
	`, checkpoint.SourceID, checkpoint.RecordSequence, checkpoint.StateVersion, checkpoint.Cursor, checkpoint.Fingerprint)
	if err != nil {
		return false, fmt.Errorf("upsert source checkpoint: %w", err)
	}
	return changed, nil
}

func selectCheckpoint(ctx context.Context, queryer rowQueryer, sourceID model.SourceID) (importer.ImportCheckpoint, bool, error) {
	var checkpoint importer.ImportCheckpoint
	err := queryer.QueryRowContext(ctx, `
		SELECT source_id, record_sequence, state_version, cursor, fingerprint
		FROM import_checkpoints WHERE source_id = ?
	`, sourceID).Scan(
		&checkpoint.SourceID,
		&checkpoint.RecordSequence,
		&checkpoint.StateVersion,
		&checkpoint.Cursor,
		&checkpoint.Fingerprint,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return importer.ImportCheckpoint{}, false, nil
	}
	if err != nil {
		return importer.ImportCheckpoint{}, false, err
	}
	return checkpoint, true, nil
}
