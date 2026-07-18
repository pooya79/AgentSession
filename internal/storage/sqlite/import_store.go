package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	storagecontract "github.com/pooya79/AgentSession/internal/storage"
)

// ImportStore persists authoritative import data in SQLite.
type ImportStore struct {
	db *sql.DB

	// beforeCommit is an internal lifecycle seam used to verify interruption
	// behavior deterministically. Production stores leave it nil.
	beforeCommit func()
}

var _ importer.ImportStore = (*ImportStore)(nil)
var _ storagecontract.SessionReader = (*ImportStore)(nil)
var _ storagecontract.SessionDeleter = (*ImportStore)(nil)

const rawRecordCompressionThreshold = storagecontract.InlinePayloadThresholdBytes

const (
	rawEncodingIdentity = storagecontract.EncodingIdentity
	rawEncodingZlib     = storagecontract.EncodingZlib
	payloadInline       = "inline"
	payloadDetached     = "detached"
)

// NewImportStore creates an import store backed by a migrated database.
func NewImportStore(db *sql.DB) (*ImportStore, error) {
	if db == nil {
		return nil, errors.New("sqlite import store: database is nil")
	}
	return &ImportStore{db: db}, nil
}

// CommitBatch atomically persists a canonical batch and its checkpoint.
func (s *ImportStore) CommitBatch(ctx context.Context, batch importer.ImportBatch) (err error) {
	return s.commitBatch(ctx, batch, false)
}

// ReconcileSource atomically removes stale authoritative data for a verified
// changed source and commits its first replacement batch and checkpoint.
func (s *ImportStore) ReconcileSource(ctx context.Context, batch importer.ImportBatch) error {
	return s.commitBatch(ctx, batch, true)
}

func (s *ImportStore) commitBatch(ctx context.Context, batch importer.ImportBatch, reconcile bool) (err error) {
	sourceID := batch.Checkpoint.SourceID
	operation := "commit batch"
	if reconcile {
		operation = "reconcile source"
	}
	wrap := func(detail string, cause error) error {
		return fmt.Errorf("sqlite import store: %s for source %q: %s: %w", operation, sourceID, detail, cause)
	}
	if err := batch.Validate(); err != nil {
		return wrap("validate batch", err)
	}
	if err := ctx.Err(); err != nil {
		return wrap("check cancellation", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin transaction", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, wrap("roll back transaction", rollbackErr))
		}
	}()

	if reconcile {
		if err := deleteSourceImport(ctx, tx, sourceID); err != nil {
			return wrap("remove stale source data", err)
		}
	}
	if err := upsertSession(ctx, tx, batch.Session); err != nil {
		return wrap("persist session", err)
	}
	for i, rawRecord := range batch.RawRecords {
		if err := persistRawRecord(ctx, tx, batch.Session.ID, rawRecord); err != nil {
			return wrap(fmt.Sprintf("persist raw record %d (%q)", i, rawRecord.Ref.ID), err)
		}
	}
	for i, event := range batch.Events {
		if err := persistEvent(ctx, tx, event); err != nil {
			return wrap(fmt.Sprintf("persist event %d (%q)", i, event.ID), err)
		}
	}
	for i, diagnostic := range batch.RecordDiagnostics {
		if err := persistRecordDiagnostic(ctx, tx, batch.Session.ID, diagnostic); err != nil {
			return wrap(fmt.Sprintf("persist record diagnostic %d for %q", i, diagnostic.RawRecordID), err)
		}
	}
	if err := replaceDiagnostics(ctx, tx, batch.Session); err != nil {
		return wrap("replace diagnostics", err)
	}
	if err := persistCheckpoint(ctx, tx, batch.Checkpoint); err != nil {
		return wrap("persist checkpoint", err)
	}

	if s.beforeCommit != nil {
		s.beforeCommit()
	}
	if err := ctx.Err(); err != nil {
		return wrap("check cancellation before commit", err)
	}
	if err := tx.Commit(); err != nil {
		return wrap("commit transaction", err)
	}
	committed = true
	return nil
}

func deleteSourceImport(ctx context.Context, tx *sql.Tx, sourceID model.SourceID) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE source_id = ?`, sourceID); err != nil {
		return fmt.Errorf("delete sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM import_checkpoints WHERE source_id = ?`, sourceID); err != nil {
		return fmt.Errorf("delete checkpoint: %w", err)
	}
	return nil
}

func upsertSession(ctx context.Context, tx *sql.Tx, session model.Session) error {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (
			id, title, summary, started_at, ended_at, source_id,
			adapter_name, adapter_version, format_version, model_version, normalization_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			summary = excluded.summary,
			started_at = excluded.started_at,
			ended_at = excluded.ended_at,
			adapter_name = excluded.adapter_name,
			adapter_version = excluded.adapter_version,
			format_version = excluded.format_version,
			model_version = excluded.model_version,
			normalization_version = excluded.normalization_version
		WHERE sessions.source_id = excluded.source_id
	`,
		session.ID,
		session.Title,
		session.Summary,
		encodeTime(session.StartedAt),
		encodeTime(session.EndedAt),
		session.Import.SourceID,
		session.Import.AdapterName,
		session.Import.AdapterVersion,
		session.Import.FormatVersion,
		session.Import.ModelVersion,
		session.Import.NormalizationVersion,
	)
	if err != nil {
		return fmt.Errorf("upsert session %q: %w", session.ID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect session %q upsert: %w", session.ID, err)
	}
	if rows == 0 {
		return fmt.Errorf("session %q is already associated with another source", session.ID)
	}
	return nil
}

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

func persistRawRecord(ctx context.Context, tx *sql.Tx, sessionID model.SessionID, rawRecord model.RawRecord) error {
	stored, err := rawRecordForStorage(sessionID, rawRecord)
	if err != nil {
		return err
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
		return fmt.Errorf("insert raw record: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect raw record insert: %w", err)
	}
	if rows == 1 {
		return nil
	}
	existing, found, err := selectStoredRawRecord(ctx, tx, rawRecord.Ref.ID)
	if err != nil {
		return fmt.Errorf("load duplicate raw record: %w", err)
	}
	if !found || !storedRawRecordEqual(existing, stored) {
		return fmt.Errorf("%w: raw record ID %q has different source content", importer.ErrRawRecordConflict, rawRecord.Ref.ID)
	}
	return nil
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
	if len(dataJSON) > storagecontract.InlinePayloadThresholdBytes {
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

func persistEvent(ctx context.Context, tx *sql.Tx, event model.Event) error {
	stored, err := eventForStorage(event)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO events (
			id, session_id, sequence, timestamp, kind, summary, searchable_text, data_json,
			raw_record_id, raw_source_id, raw_record_sequence, raw_byte_offset, raw_byte_length, raw_content_hash,
			retention_policy_version, payload_storage
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING
	`, stored.values()...)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect event insert: %w", err)
	}
	if rows == 1 {
		if stored.Payload != nil {
			if err := persistEventPayload(ctx, tx, event.ID, *stored.Payload); err != nil {
				return err
			}
		}
		return nil
	}

	existing, found, err := selectStoredEvent(ctx, tx, event.ID)
	if err != nil {
		return fmt.Errorf("load duplicate event: %w", err)
	}
	if !found || !reflect.DeepEqual(existing, stored) {
		return fmt.Errorf("%w: event ID %q has different canonical content", importer.ErrEventConflict, event.ID)
	}
	return nil
}

func (e storedEvent) values() []any {
	return []any{
		e.ID, e.SessionID, e.Sequence, nullIfEmpty(e.Timestamp), e.Kind, e.Summary, e.SearchableText, e.DataJSON,
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

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
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
	SELECT e.id, e.session_id, e.sequence, e.timestamp, e.kind, e.summary, e.searchable_text, e.data_json,
	       e.raw_record_id, e.raw_source_id, e.raw_record_sequence, e.raw_byte_offset, e.raw_byte_length,
	       e.raw_content_hash, e.retention_policy_version, e.payload_storage,
	       p.retention_policy_version, p.storage_encoding, p.original_size, p.content
	FROM events e LEFT JOIN event_payloads p ON p.event_id = e.id`

type rowScanner interface {
	Scan(...any) error
}

func scanStoredEvent(scanner rowScanner) (storedEvent, error) {
	var event storedEvent
	var timestamp, payloadEncoding sql.NullString
	var payloadPolicy, payloadSize sql.NullInt64
	var payloadContent []byte
	if err := scanner.Scan(
		&event.ID, &event.SessionID, &event.Sequence, &timestamp, &event.Kind, &event.Summary,
		&event.SearchableText, &event.DataJSON, &event.RawRecordID, &event.RawSourceID,
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

func replaceDiagnostics(ctx context.Context, tx *sql.Tx, session model.Session) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_diagnostics WHERE session_id = ?`, session.ID); err != nil {
		return fmt.Errorf("delete diagnostics for session %q: %w", session.ID, err)
	}
	for i, diagnostic := range session.Diagnostics {
		eventIDs, err := json.Marshal(diagnostic.EventIDs)
		if err != nil {
			return fmt.Errorf("encode diagnostic %d event IDs: %w", i, err)
		}
		rawRecordIDs, err := json.Marshal(diagnostic.RawRecordIDs)
		if err != nil {
			return fmt.Errorf("encode diagnostic %d raw record IDs: %w", i, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO session_diagnostics (
				session_id, position, code, severity, message, event_ids_json, raw_record_ids_json
			) VALUES (?, ?, ?, ?, ?, ?, ?)
		`, session.ID, i, diagnostic.Code, diagnostic.Severity, diagnostic.Message, string(eventIDs), string(rawRecordIDs)); err != nil {
			return fmt.Errorf("insert diagnostic %d: %w", i, err)
		}
	}
	return nil
}

type storedRecordDiagnostic struct {
	SessionID        string
	RawRecordID      string
	Ordinal          int64
	Code             string
	Severity         string
	Message          string
	EventIDsJSON     string
	RawRecordIDsJSON string
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
		SessionID:        string(sessionID),
		RawRecordID:      string(diagnostic.RawRecordID),
		Ordinal:          diagnostic.Ordinal,
		Code:             diagnostic.Diagnostic.Code,
		Severity:         string(diagnostic.Diagnostic.Severity),
		Message:          diagnostic.Diagnostic.Message,
		EventIDsJSON:     string(eventIDs),
		RawRecordIDsJSON: string(rawRecordIDs),
	}, nil
}

func persistRecordDiagnostic(ctx context.Context, tx *sql.Tx, sessionID model.SessionID, diagnostic model.RecordDiagnostic) error {
	stored, err := recordDiagnosticForStorage(sessionID, diagnostic)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO record_diagnostics (
			session_id, raw_record_id, ordinal, code, severity, message, event_ids_json, raw_record_ids_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(raw_record_id, ordinal) DO NOTHING
	`, stored.SessionID, stored.RawRecordID, stored.Ordinal, stored.Code, stored.Severity, stored.Message,
		stored.EventIDsJSON, stored.RawRecordIDsJSON)
	if err != nil {
		return fmt.Errorf("insert record diagnostic: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect record diagnostic insert: %w", err)
	}
	if rows == 1 {
		return nil
	}
	existing, found, err := selectStoredRecordDiagnostic(ctx, tx, diagnostic.RawRecordID, diagnostic.Ordinal)
	if err != nil {
		return fmt.Errorf("load duplicate record diagnostic: %w", err)
	}
	if !found || existing != stored {
		return fmt.Errorf("%w: raw record %q ordinal %d has different diagnostic content", importer.ErrDiagnosticConflict, diagnostic.RawRecordID, diagnostic.Ordinal)
	}
	return nil
}

func selectStoredRecordDiagnostic(ctx context.Context, queryer rowQueryer, rawRecordID model.RawRecordID, ordinal int64) (storedRecordDiagnostic, bool, error) {
	var diagnostic storedRecordDiagnostic
	err := queryer.QueryRowContext(ctx, `
		SELECT session_id, raw_record_id, ordinal, code, severity, message, event_ids_json, raw_record_ids_json
		FROM record_diagnostics WHERE raw_record_id = ? AND ordinal = ?
	`, rawRecordID, ordinal).Scan(
		&diagnostic.SessionID, &diagnostic.RawRecordID, &diagnostic.Ordinal, &diagnostic.Code,
		&diagnostic.Severity, &diagnostic.Message, &diagnostic.EventIDsJSON, &diagnostic.RawRecordIDsJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedRecordDiagnostic{}, false, nil
	}
	if err != nil {
		return storedRecordDiagnostic{}, false, err
	}
	return diagnostic, true, nil
}

func persistCheckpoint(ctx context.Context, tx *sql.Tx, checkpoint importer.ImportCheckpoint) error {
	existing, found, err := selectCheckpoint(ctx, tx, checkpoint.SourceID)
	if err != nil {
		return fmt.Errorf("load current checkpoint: %w", err)
	}
	if found && (checkpoint.ByteOffset < existing.ByteOffset ||
		checkpoint.RecordSequence < existing.RecordSequence ||
		checkpoint.SourceSize < existing.SourceSize) {
		return fmt.Errorf(
			"%w: source %q cursor (%d, %d, %d) is behind (%d, %d, %d)",
			importer.ErrCheckpointRegression,
			checkpoint.SourceID,
			checkpoint.ByteOffset,
			checkpoint.RecordSequence,
			checkpoint.SourceSize,
			existing.ByteOffset,
			existing.RecordSequence,
			existing.SourceSize,
		)
	}
	if found && checkpoint.ByteOffset == existing.ByteOffset && checkpoint.RecordSequence == existing.RecordSequence &&
		(checkpoint.PrefixHash != existing.PrefixHash || checkpoint.LastRecordHash != existing.LastRecordHash) {
		return fmt.Errorf("%w: source %q fingerprints changed at the committed cursor", importer.ErrCheckpointRegression, checkpoint.SourceID)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO import_checkpoints (
			source_id, byte_offset, record_sequence, prefix_hash, last_record_hash, source_size
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id) DO UPDATE SET
			byte_offset = excluded.byte_offset,
			record_sequence = excluded.record_sequence,
			prefix_hash = excluded.prefix_hash,
			last_record_hash = excluded.last_record_hash,
			source_size = excluded.source_size
	`, checkpoint.SourceID, checkpoint.ByteOffset, checkpoint.RecordSequence, checkpoint.PrefixHash, checkpoint.LastRecordHash, checkpoint.SourceSize)
	if err != nil {
		return fmt.Errorf("upsert source checkpoint: %w", err)
	}
	return nil
}

func selectCheckpoint(ctx context.Context, queryer rowQueryer, sourceID model.SourceID) (importer.ImportCheckpoint, bool, error) {
	var checkpoint importer.ImportCheckpoint
	err := queryer.QueryRowContext(ctx, `
		SELECT source_id, byte_offset, record_sequence, prefix_hash, last_record_hash, source_size
		FROM import_checkpoints WHERE source_id = ?
	`, sourceID).Scan(
		&checkpoint.SourceID,
		&checkpoint.ByteOffset,
		&checkpoint.RecordSequence,
		&checkpoint.PrefixHash,
		&checkpoint.LastRecordHash,
		&checkpoint.SourceSize,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return importer.ImportCheckpoint{}, false, nil
	}
	if err != nil {
		return importer.ImportCheckpoint{}, false, err
	}
	return checkpoint, true, nil
}

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
		SELECT d.raw_record_id, d.ordinal, d.code, d.severity, d.message,
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
			&diagnostic.Diagnostic.Severity, &diagnostic.Diagnostic.Message, &eventIDs, &rawRecordIDs,
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

func encodeTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func timeString(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func decodeTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	return decodeTimeString(value.String)
}

func decodeTimeString(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func nullableInt(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
