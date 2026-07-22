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
var _ importer.ContainerMembershipStore = (*ImportStore)(nil)
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

// SyncContainerMembers transactionally replaces a physical container's
// logical-source inventory and removes only AgentSession-owned imports for
// children that are no longer present.
func (s *ImportStore) SyncContainerMembers(ctx context.Context, containerID model.SourceID, members []model.SourceID) (err error) {
	if strings.TrimSpace(string(containerID)) == "" {
		return errors.New("sqlite import store: synchronize container: container source ID is required")
	}
	wanted := make(map[model.SourceID]struct{}, len(members))
	for i, member := range members {
		if strings.TrimSpace(string(member)) == "" {
			return fmt.Errorf("sqlite import store: synchronize container %q: member %d is empty", containerID, i)
		}
		if member == containerID {
			return fmt.Errorf("sqlite import store: synchronize container %q: container cannot be its own member", containerID)
		}
		if _, exists := wanted[member]; exists {
			return fmt.Errorf("sqlite import store: synchronize container %q: duplicate member %q", containerID, member)
		}
		wanted[member] = struct{}{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite import store: synchronize container %q: begin transaction: %w", containerID, err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	rows, err := tx.QueryContext(ctx, `SELECT child_source_id FROM container_memberships WHERE container_source_id = ?`, containerID)
	if err != nil {
		return fmt.Errorf("sqlite import store: synchronize container %q: read prior inventory: %w", containerID, err)
	}
	var stale []model.SourceID
	for rows.Next() {
		var child model.SourceID
		if err := rows.Scan(&child); err != nil {
			rows.Close()
			return fmt.Errorf("sqlite import store: synchronize container %q: scan prior inventory: %w", containerID, err)
		}
		if _, exists := wanted[child]; !exists {
			stale = append(stale, child)
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("sqlite import store: synchronize container %q: close prior inventory: %w", containerID, err)
	}
	for _, child := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM reconciliation_runs WHERE source_id = ?`, child); err != nil {
			return fmt.Errorf("sqlite import store: synchronize container %q: remove stale reconciliation for %q: %w", containerID, child, err)
		}
		if err := deleteSourceImport(ctx, tx, child); err != nil {
			return fmt.Errorf("sqlite import store: synchronize container %q: remove stale member %q: %w", containerID, child, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM container_memberships WHERE container_source_id = ?`, containerID); err != nil {
		return fmt.Errorf("sqlite import store: synchronize container %q: replace inventory: %w", containerID, err)
	}
	for _, member := range members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO container_memberships (container_source_id, child_source_id) VALUES (?, ?)`, containerID, member); err != nil {
			return fmt.Errorf("sqlite import store: synchronize container %q: add member %q: %w", containerID, member, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite import store: synchronize container %q: commit: %w", containerID, err)
	}
	return nil
}

// CommitBatch atomically persists a canonical batch and its checkpoint.
func (s *ImportStore) CommitBatch(ctx context.Context, batch importer.ImportBatch) (err error) {
	return s.commitBatch(ctx, batch)
}

func (s *ImportStore) commitBatch(ctx context.Context, batch importer.ImportBatch) (err error) {
	sourceID := batch.Checkpoint.SourceID
	wrap := func(detail string, cause error) error {
		return fmt.Errorf("sqlite import store: commit batch for source %q: %s: %w", sourceID, detail, cause)
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

	if err := persistImportBatch(ctx, tx, batch); err != nil {
		return wrap("persist canonical evidence", err)
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

func persistImportBatch(ctx context.Context, tx *sql.Tx, batch importer.ImportBatch) error {
	return persistImportBatchWithRevisionBase(ctx, tx, batch, nil)
}

func persistImportBatchWithRevisionBase(ctx context.Context, tx *sql.Tx, batch importer.ImportBatch, revisionBase *int64) error {
	changed, err := upsertSession(ctx, tx, batch.Session)
	if err != nil {
		return fmt.Errorf("persist session: %w", err)
	}
	if revisionBase != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET canonical_revision = ? WHERE id = ?`, *revisionBase, batch.Session.ID); err != nil {
			return fmt.Errorf("restore canonical revision: %w", err)
		}
	}
	for i, rawRecord := range batch.RawRecords {
		inserted, err := persistRawRecord(ctx, tx, batch.Session.ID, rawRecord)
		if err != nil {
			return fmt.Errorf("persist raw record %d (%q): %w", i, rawRecord.Ref.ID, err)
		}
		changed = changed || inserted
	}
	for i, event := range batch.Events {
		inserted, err := persistEvent(ctx, tx, event)
		if err != nil {
			return fmt.Errorf("persist event %d (%q): %w", i, event.ID, err)
		}
		changed = changed || inserted
	}
	for i, diagnostic := range batch.RecordDiagnostics {
		inserted, err := persistRecordDiagnostic(ctx, tx, batch.Session.ID, diagnostic)
		if err != nil {
			return fmt.Errorf("persist record diagnostic %d for %q: %w", i, diagnostic.RawRecordID, err)
		}
		changed = changed || inserted
	}
	diagnosticsChanged, err := replaceDiagnostics(ctx, tx, batch.Session)
	if err != nil {
		return fmt.Errorf("replace diagnostics: %w", err)
	}
	changed = changed || diagnosticsChanged
	checkpointChanged, err := persistCheckpoint(ctx, tx, batch.Checkpoint)
	if err != nil {
		return fmt.Errorf("persist checkpoint: %w", err)
	}
	changed = changed || checkpointChanged
	if changed {
		if err := advanceCanonicalRevision(ctx, tx, batch.Session.ID); err != nil {
			return fmt.Errorf("advance canonical revision: %w", err)
		}
	}
	return nil
}

func advanceCanonicalRevision(ctx context.Context, tx *sql.Tx, sessionID model.SessionID) error {
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET canonical_revision = canonical_revision + 1 WHERE id = ?`, sessionID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return err
		}
		return fmt.Errorf("session %q is unavailable", sessionID)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO session_projection_states (
			session_id, kind, status, target_version, target_revision, updated_at
		)
		SELECT s.id, d.kind, 'pending', d.target_version, s.canonical_revision, ?
		FROM sessions s CROSS JOIN projection_definitions d WHERE s.id = ?
		ON CONFLICT(session_id, kind) DO UPDATE SET
			status = 'pending', target_version = excluded.target_version,
			target_revision = excluded.target_revision, run_token = NULL, started_at = NULL, lease_expires_at = NULL,
			attempt_count = 0, updated_at = excluded.updated_at,
			failure_code = NULL, failure_summary = NULL, failure_attempt = NULL, failure_at = NULL
	`, now, sessionID)
	return err
}

type sqliteReconciliation struct {
	store    *ImportStore
	runID    string
	sourceID model.SourceID
}

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

func canonicalSourceDigest(ctx context.Context, tx *sql.Tx, sourceID model.SourceID) ([]byte, error) {
	hash := sha256.New()
	queries := []string{
		`SELECT id, title, summary, started_at, ended_at, source_id, adapter_name, adapter_version, format_version, model_version, normalization_version FROM sessions WHERE source_id = ? ORDER BY id`,
		`SELECT id, session_id, source_id, record_sequence, byte_offset, byte_length, content_hash, storage_encoding, original_size, content, retention_policy_version FROM raw_records WHERE source_id = ? ORDER BY id`,
		`SELECT e.id, e.session_id, e.sequence, e.timestamp, e.kind, e.summary, e.searchable_text, e.data_json, e.raw_record_id, e.raw_source_id, e.raw_record_sequence, e.raw_byte_offset, e.raw_byte_length, e.raw_content_hash, e.retention_policy_version, e.payload_storage FROM events e JOIN sessions s ON s.id = e.session_id WHERE s.source_id = ? ORDER BY e.id`,
		`SELECT p.event_id, p.retention_policy_version, p.storage_encoding, p.original_size, p.content FROM event_payloads p JOIN events e ON e.id = p.event_id JOIN sessions s ON s.id = e.session_id WHERE s.source_id = ? ORDER BY p.event_id`,
		`SELECT d.session_id, d.position, d.code, d.severity, d.message, d.event_ids_json, d.raw_record_ids_json FROM session_diagnostics d JOIN sessions s ON s.id = d.session_id WHERE s.source_id = ? ORDER BY d.session_id, d.position`,
		`SELECT d.session_id, d.raw_record_id, d.ordinal, d.code, d.severity, d.message, d.event_ids_json, d.raw_record_ids_json FROM record_diagnostics d JOIN sessions s ON s.id = d.session_id WHERE s.source_id = ? ORDER BY d.session_id, d.raw_record_id, d.ordinal`,
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

func (r *sqliteReconciliation) Abort(ctx context.Context) error {
	_, err := r.store.db.ExecContext(ctx, `DELETE FROM reconciliation_runs WHERE run_id = ?`, r.runID)
	if err != nil {
		return fmt.Errorf("sqlite import store: abort reconciliation for source %q: %w", r.sourceID, err)
	}
	return nil
}

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

func deleteSourceImport(ctx context.Context, tx *sql.Tx, sourceID model.SourceID) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE source_id = ?`, sourceID); err != nil {
		return fmt.Errorf("delete sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM import_checkpoints WHERE source_id = ?`, sourceID); err != nil {
		return fmt.Errorf("delete checkpoint: %w", err)
	}
	return nil
}

func upsertSession(ctx context.Context, tx *sql.Tx, session model.Session) (bool, error) {
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
		  AND (sessions.title IS NOT excluded.title OR sessions.summary IS NOT excluded.summary
		    OR sessions.started_at IS NOT excluded.started_at OR sessions.ended_at IS NOT excluded.ended_at
		    OR sessions.adapter_name IS NOT excluded.adapter_name OR sessions.adapter_version IS NOT excluded.adapter_version
		    OR sessions.format_version IS NOT excluded.format_version OR sessions.model_version IS NOT excluded.model_version
		    OR sessions.normalization_version IS NOT excluded.normalization_version)
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
		return false, fmt.Errorf("upsert session %q: %w", session.ID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect session %q upsert: %w", session.ID, err)
	}
	if rows == 1 {
		return true, nil
	}
	var sourceID model.SourceID
	if err := tx.QueryRowContext(ctx, `SELECT source_id FROM sessions WHERE id = ?`, session.ID).Scan(&sourceID); err != nil {
		return false, fmt.Errorf("inspect unchanged session %q: %w", session.ID, err)
	}
	if sourceID != session.Import.SourceID {
		return false, fmt.Errorf("session %q is already associated with another source", session.ID)
	}
	return false, nil
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

func persistEvent(ctx context.Context, tx *sql.Tx, event model.Event) (bool, error) {
	stored, err := eventForStorage(event)
	if err != nil {
		return false, err
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

func persistRecordDiagnostic(ctx context.Context, tx *sql.Tx, sessionID model.SessionID, diagnostic model.RecordDiagnostic) (bool, error) {
	stored, err := recordDiagnosticForStorage(sessionID, diagnostic)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO record_diagnostics (
			session_id, raw_record_id, ordinal, code, severity, message, event_ids_json, raw_record_ids_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(raw_record_id, ordinal) DO NOTHING
	`, stored.SessionID, stored.RawRecordID, stored.Ordinal, stored.Code, stored.Severity, stored.Message,
		stored.EventIDsJSON, stored.RawRecordIDsJSON)
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
