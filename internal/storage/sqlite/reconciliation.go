package sqlite

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

type sqliteReconciliation struct {
	store    *ImportStore
	runID    string
	sourceID model.SourceID
}

// BeginReconciliation creates an isolated staging run after verifying that the
// live checkpoint still matches the adapter's expected source generation.
func (s *ImportStore) BeginReconciliation(ctx context.Context, sourceID model.SourceID, expected importer.ImportCheckpoint) (importer.Reconciliation, error) {
	if strings.TrimSpace(string(sourceID)) == "" {
		return nil, errors.New("sqlite import store: begin reconciliation: source ID is required")
	}
	if err := expected.Validate(); err != nil {
		return nil, fmt.Errorf("sqlite import store: begin reconciliation for source %q: expected checkpoint: %w", sourceID, err)
	}
	if expected.SourceID != sourceID {
		return nil, fmt.Errorf("sqlite import store: begin reconciliation: checkpoint source %q does not match %q", expected.SourceID, sourceID)
	}
	runID, err := newReconciliationRunID()
	if err != nil {
		return nil, fmt.Errorf("sqlite import store: begin reconciliation for source %q: %w", sourceID, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("sqlite import store: begin reconciliation for source %q: begin transaction: %w", sourceID, err)
	}
	defer tx.Rollback()
	current, found, err := selectCheckpoint(ctx, tx, sourceID)
	if err != nil {
		return nil, fmt.Errorf("sqlite import store: begin reconciliation for source %q: read live checkpoint: %w", sourceID, err)
	}
	if !found || !importer.CheckpointEqual(current, expected) {
		return nil, fmt.Errorf("%w: source %q no longer matches expected generation", importer.ErrCheckpointConflict, sourceID)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM reconciliation_runs WHERE source_id = ?`, sourceID); err != nil {
		return nil, fmt.Errorf("sqlite import store: begin reconciliation for source %q: clear abandoned staging: %w", sourceID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO reconciliation_runs (
			run_id, source_id, expected_record_sequence, expected_state_version, expected_cursor, expected_fingerprint
		) VALUES (?, ?, ?, ?, ?, ?)
	`, runID, sourceID, expected.RecordSequence, expected.StateVersion, expected.Cursor, expected.Fingerprint); err != nil {
		return nil, fmt.Errorf("sqlite import store: begin reconciliation for source %q: persist staging run: %w", sourceID, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("sqlite import store: begin reconciliation for source %q: commit staging run: %w", sourceID, err)
	}
	return &sqliteReconciliation{store: s, runID: runID, sourceID: sourceID}, nil
}

func newReconciliationRunID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate staging run ID: %w", err)
	}
	return "reconcile_" + hex.EncodeToString(value[:]), nil
}

// StageBatch appends a validated batch to this reconciliation without exposing
// it through canonical readers.
func (r *sqliteReconciliation) StageBatch(ctx context.Context, batch importer.ImportBatch) error {
	if err := batch.Validate(); err != nil {
		return fmt.Errorf("sqlite import store: stage reconciliation for source %q: validate batch: %w", r.sourceID, err)
	}
	if batch.Checkpoint.SourceID != r.sourceID {
		return fmt.Errorf("sqlite import store: stage reconciliation source %q does not match %q", batch.Checkpoint.SourceID, r.sourceID)
	}
	encoded, err := encodeImportBatch(batch)
	if err != nil {
		return fmt.Errorf("sqlite import store: stage reconciliation for source %q: encode batch: %w", r.sourceID, err)
	}
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite import store: stage reconciliation for source %q: begin transaction: %w", r.sourceID, err)
	}
	defer tx.Rollback()
	var previous []byte
	err = tx.QueryRowContext(ctx, `
		SELECT batch FROM reconciliation_batches WHERE run_id = ? ORDER BY ordinal DESC LIMIT 1
	`, r.runID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("sqlite import store: stage reconciliation for source %q: read previous batch: %w", r.sourceID, err)
	}
	if err == nil {
		prior, decodeErr := decodeImportBatch(previous)
		if decodeErr != nil {
			return fmt.Errorf("sqlite import store: stage reconciliation for source %q: decode previous batch: %w", r.sourceID, decodeErr)
		}
		if batch.Checkpoint.RecordSequence <= prior.Checkpoint.RecordSequence {
			return fmt.Errorf("%w: staged sequence %d does not advance beyond %d", importer.ErrCheckpointRegression, batch.Checkpoint.RecordSequence, prior.Checkpoint.RecordSequence)
		}
		if err := importer.ValidateSessionTransition(prior.Session, batch.Session); err != nil {
			return fmt.Errorf("sqlite import store: stage reconciliation for source %q: session transition: %w", r.sourceID, err)
		}
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO reconciliation_batches (run_id, ordinal, batch)
		SELECT ?, COALESCE(MAX(ordinal) + 1, 0), ? FROM reconciliation_batches WHERE run_id = ?
	`, r.runID, encoded, r.runID)
	if err != nil {
		return fmt.Errorf("sqlite import store: stage reconciliation for source %q: persist batch: %w", r.sourceID, err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return fmt.Errorf("sqlite import store: stage reconciliation for source %q: staging run is unavailable", r.sourceID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite import store: stage reconciliation for source %q: commit batch: %w", r.sourceID, err)
	}
	return nil
}

// Finalize atomically replaces the source's canonical generation when its live
// checkpoint still matches the checkpoint captured at reconciliation start.
func (r *sqliteReconciliation) Finalize(ctx context.Context) (err error) {
	wrap := func(detail string, cause error) error {
		return fmt.Errorf("sqlite import store: finalize reconciliation for source %q: %s: %w", r.sourceID, detail, cause)
	}
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin transaction", err)
	}
	committed := false
	defer func() {
		if !committed {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				err = errors.Join(err, wrap("roll back", rollbackErr))
			}
		}
	}()
	expected, err := selectReconciliationExpected(ctx, tx, r.runID, r.sourceID)
	if err != nil {
		return wrap("read expected checkpoint", err)
	}
	current, found, err := selectCheckpoint(ctx, tx, r.sourceID)
	if err != nil {
		return wrap("read live checkpoint", err)
	}
	if !found || !importer.CheckpointEqual(current, expected) {
		return wrap("compare live checkpoint", importer.ErrCheckpointConflict)
	}
	var batchCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM reconciliation_batches WHERE run_id = ?`, r.runID).Scan(&batchCount); err != nil {
		return wrap("count staged batches", err)
	}
	if batchCount == 0 {
		return wrap("validate staged batches", errors.New("no completed batch was staged"))
	}
	priorRevisions := make(map[model.SessionID]int64)
	rows, err := tx.QueryContext(ctx, `SELECT id, canonical_revision FROM sessions WHERE source_id = ?`, r.sourceID)
	if err != nil {
		return wrap("read prior canonical revisions", err)
	}
	for rows.Next() {
		var sessionID model.SessionID
		var revision int64
		if err := rows.Scan(&sessionID, &revision); err != nil {
			rows.Close()
			return wrap("scan prior canonical revision", err)
		}
		priorRevisions[sessionID] = revision
	}
	if err := rows.Close(); err != nil {
		return wrap("close prior canonical revisions", err)
	}
	priorDigest, err := canonicalSourceDigest(ctx, tx, r.sourceID)
	if err != nil {
		return wrap("fingerprint prior canonical evidence", err)
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT replace_canonical_evidence`); err != nil {
		return wrap("begin canonical replacement savepoint", err)
	}
	if err := deleteSourceImport(ctx, tx, r.sourceID); err != nil {
		return wrap("remove stale source data", err)
	}
	for i := 0; i < batchCount; i++ {
		var encoded []byte
		if err := tx.QueryRowContext(ctx, `SELECT batch FROM reconciliation_batches WHERE run_id = ? AND ordinal = ?`, r.runID, i).Scan(&encoded); err != nil {
			return wrap(fmt.Sprintf("read staged batch %d", i), err)
		}
		batch, err := decodeImportBatch(encoded)
		if err != nil {
			return wrap(fmt.Sprintf("decode staged batch %d", i), err)
		}
		var revisionBase *int64
		if prior, exists := priorRevisions[batch.Session.ID]; exists {
			revisionBase = &prior
			delete(priorRevisions, batch.Session.ID)
		}
		if err := persistImportBatchWithRevisionBase(ctx, tx, batch, revisionBase); err != nil {
			return wrap(fmt.Sprintf("promote staged batch %d", i), err)
		}
	}
	currentDigest, err := canonicalSourceDigest(ctx, tx, r.sourceID)
	if err != nil {
		return wrap("fingerprint promoted canonical evidence", err)
	}
	if bytes.Equal(priorDigest, currentDigest) {
		// Preserve existing rows and projection state when a retry produced
		// byte-for-byte identical canonical evidence.
		if _, err := tx.ExecContext(ctx, `ROLLBACK TO replace_canonical_evidence`); err != nil {
			return wrap("restore idempotent canonical evidence", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `RELEASE replace_canonical_evidence`); err != nil {
		return wrap("finish canonical replacement savepoint", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM reconciliation_runs WHERE run_id = ?`, r.runID); err != nil {
		return wrap("remove staging run", err)
	}
	if r.store.beforeCommit != nil {
		r.store.beforeCommit()
	}
	if err := ctx.Err(); err != nil {
		return wrap("check cancellation before commit", err)
	}
	if err := tx.Commit(); err != nil {
		return wrap("commit", err)
	}
	committed = true
	return nil
}

// canonicalSourceDigest hashes every authoritative value owned by sourceID.
// Explicit, ordered queries keep the digest stable and make schema additions
// deliberate rather than silently changing reconciliation behavior.
func canonicalSourceDigest(ctx context.Context, tx *sql.Tx, sourceID model.SourceID) ([]byte, error) {
	hash := sha256.New()
	queries := []string{
		`SELECT id, title, summary, started_at, ended_at, source_id, adapter_name, adapter_version, format_version, model_version, normalization_version FROM sessions WHERE source_id = ? ORDER BY id`,
		`SELECT id, session_id, source_id, record_sequence, byte_offset, byte_length, content_hash, storage_encoding, original_size, content, retention_policy_version FROM raw_records WHERE source_id = ? ORDER BY id`,
		`SELECT e.id, e.session_id, e.sequence, e.timestamp, e.kind, e.summary, e.searchable_text, e.data_json, e.message_role, e.raw_record_id, e.raw_source_id, e.raw_record_sequence, e.raw_byte_offset, e.raw_byte_length, e.raw_content_hash, e.retention_policy_version, e.payload_storage FROM events e JOIN sessions s ON s.id = e.session_id WHERE s.source_id = ? ORDER BY e.id`,
		`SELECT p.event_id, p.retention_policy_version, p.storage_encoding, p.original_size, p.content FROM event_payloads p JOIN events e ON e.id = p.event_id JOIN sessions s ON s.id = e.session_id WHERE s.source_id = ? ORDER BY p.event_id`,
		`SELECT d.session_id, d.position, d.code, d.severity, d.message, d.event_ids_json, d.raw_record_ids_json FROM session_diagnostics d JOIN sessions s ON s.id = d.session_id WHERE s.source_id = ? ORDER BY d.session_id, d.position`,
		`SELECT d.session_id, d.raw_record_id, d.ordinal, d.code, d.severity, d.message, d.interpretation_reason, d.event_ids_json, d.raw_record_ids_json FROM record_diagnostics d JOIN sessions s ON s.id = d.session_id WHERE s.source_id = ? ORDER BY d.session_id, d.raw_record_id, d.ordinal`,
		`SELECT source_id, record_sequence, state_version, cursor, fingerprint FROM import_checkpoints WHERE source_id = ? ORDER BY source_id`,
	}
	for tableIndex, query := range queries {
		if err := binary.Write(hash, binary.BigEndian, uint32(tableIndex)); err != nil {
			return nil, err
		}
		rows, err := tx.QueryContext(ctx, query, sourceID)
		if err != nil {
			return nil, err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				rows.Close()
				return nil, err
			}
			for _, value := range values {
				if err := writeDigestValue(hash, value); err != nil {
					rows.Close()
					return nil, err
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return hash.Sum(nil), nil
}

func writeDigestValue(hash interface{ Write([]byte) (int, error) }, value any) error {
	var kind byte
	var data []byte
	switch typed := value.(type) {
	case nil:
		kind = 0
	case int64:
		kind = 1
		data = make([]byte, 8)
		binary.BigEndian.PutUint64(data, uint64(typed))
	case float64:
		kind = 2
		data = []byte(fmt.Sprintf("%g", typed))
	case bool:
		kind = 3
		if typed {
			data = []byte{1}
		}
	case []byte:
		kind = 4
		data = typed
	case string:
		kind = 5
		data = []byte(typed)
	default:
		return fmt.Errorf("unsupported canonical digest value %T", value)
	}
	if _, err := hash.Write([]byte{kind}); err != nil {
		return err
	}
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(data)))
	if _, err := hash.Write(size[:]); err != nil {
		return err
	}
	_, err := hash.Write(data)
	return err
}

func selectReconciliationExpected(ctx context.Context, queryer rowQueryer, runID string, sourceID model.SourceID) (importer.ImportCheckpoint, error) {
	checkpoint := importer.ImportCheckpoint{SourceID: sourceID}
	err := queryer.QueryRowContext(ctx, `
		SELECT expected_record_sequence, expected_state_version, expected_cursor, expected_fingerprint
		FROM reconciliation_runs WHERE run_id = ? AND source_id = ?
	`, runID, sourceID).Scan(&checkpoint.RecordSequence, &checkpoint.StateVersion, &checkpoint.Cursor, &checkpoint.Fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return importer.ImportCheckpoint{}, errors.New("staging run is unavailable")
	}
	return checkpoint, err
}

// Abort discards this run's staged batches without touching canonical data.
func (r *sqliteReconciliation) Abort(ctx context.Context) error {
	_, err := r.store.db.ExecContext(ctx, `DELETE FROM reconciliation_runs WHERE run_id = ?`, r.runID)
	if err != nil {
		return fmt.Errorf("sqlite import store: abort reconciliation for source %q: %w", r.sourceID, err)
	}
	return nil
}

// encodeImportBatch preserves each normalized payload's concrete event kind so
// the interface value can be reconstructed after durable staging.
func encodeImportBatch(batch importer.ImportBatch) ([]byte, error) {
	staged := stagedImportBatch{
		Session: batch.Session, RawRecords: batch.RawRecords,
		RecordDiagnostics: batch.RecordDiagnostics, Checkpoint: batch.Checkpoint,
	}
	staged.Events = make([]stagedEvent, len(batch.Events))
	for i, event := range batch.Events {
		data, err := json.Marshal(event.Data)
		if err != nil {
			return nil, fmt.Errorf("event %d data: %w", i, err)
		}
		staged.Events[i] = stagedEvent{
			ID: event.ID, SessionID: event.SessionID, Sequence: event.Sequence, Timestamp: event.Timestamp,
			Kind: event.Kind, Summary: event.Summary, SearchableText: event.SearchableText,
			Data: data, RawRecord: event.RawRecord,
		}
	}
	return json.Marshal(staged)
}

func decodeImportBatch(encoded []byte) (importer.ImportBatch, error) {
	var staged stagedImportBatch
	if err := json.Unmarshal(encoded, &staged); err != nil {
		return importer.ImportBatch{}, err
	}
	batch := importer.ImportBatch{
		Session: staged.Session, RawRecords: staged.RawRecords,
		RecordDiagnostics: staged.RecordDiagnostics, Checkpoint: staged.Checkpoint,
		Events: make([]model.Event, len(staged.Events)),
	}
	for i, event := range staged.Events {
		data, err := decodeNormalizedData(event.Kind, string(event.Data))
		if err != nil {
			return importer.ImportBatch{}, fmt.Errorf("event %d data: %w", i, err)
		}
		batch.Events[i] = model.Event{
			ID: event.ID, SessionID: event.SessionID, Sequence: event.Sequence, Timestamp: event.Timestamp,
			Kind: event.Kind, Summary: event.Summary, SearchableText: event.SearchableText,
			Data: data, RawRecord: event.RawRecord,
		}
	}
	if err := batch.Validate(); err != nil {
		return importer.ImportBatch{}, err
	}
	return batch, nil
}

type stagedImportBatch struct {
	Session           model.Session
	RawRecords        []model.RawRecord
	Events            []stagedEvent
	RecordDiagnostics []model.RecordDiagnostic
	Checkpoint        importer.ImportCheckpoint
}

type stagedEvent struct {
	ID             model.EventID
	SessionID      model.SessionID
	Sequence       int64
	Timestamp      *time.Time
	Kind           model.EventKind
	Summary        string
	SearchableText string
	Data           json.RawMessage
	RawRecord      model.RawRecordRef
}
