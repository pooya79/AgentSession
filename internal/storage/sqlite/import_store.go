package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

const (
	payloadInline   = "inline"
	payloadDetached = "detached"
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

// persistImportBatchWithRevisionBase writes one validated batch into an
// existing transaction. Reconciliation supplies revisionBase so replacing
// identical canonical evidence does not manufacture a new revision history.
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
	if err := refreshSessionExploration(ctx, tx, batch.Session.ID); err != nil {
		return fmt.Errorf("refresh session exploration fields: %w", err)
	}
	if changed {
		if err := advanceCanonicalRevision(ctx, tx, batch.Session.ID); err != nil {
			return fmt.Errorf("advance canonical revision: %w", err)
		}
	}
	return nil
}

// refreshSessionExploration maintains the bounded, sortable fields used by
// session-list reads. It runs in the import transaction so readers never see
// canonical evidence and its exploration metadata at different revisions.
//
// Last activity prefers the explicit session end, then the latest timestamped
// event, then the session start. Timestamps are padded to nanosecond precision
// because RFC3339 values with optional fractional seconds do not sort
// chronologically as plain text.
func refreshSessionExploration(ctx context.Context, db sqlExecer, sessionID model.SessionID) error {
	_, err := db.ExecContext(ctx, `
		UPDATE sessions
		SET last_activity_at = (
		        SELECT CASE
		            WHEN activity.value IS NULL THEN NULL
		            WHEN instr(activity.value, '.') = 0 THEN substr(activity.value, 1, 19) || '.000000000Z'
		            ELSE substr(activity.value, 1, 20) ||
		                 substr(substr(activity.value, 21, length(activity.value) - 21) || '000000000', 1, 9) || 'Z'
		        END
		        FROM (
		            SELECT COALESCE(
		                sessions.ended_at,
		                (SELECT events.timestamp
		                 FROM events
		                 WHERE events.session_id = sessions.id AND events.timestamp IS NOT NULL
		                 ORDER BY CASE
		                     WHEN instr(events.timestamp, '.') = 0 THEN substr(events.timestamp, 1, 19) || '.000000000Z'
		                     ELSE substr(events.timestamp, 1, 20) ||
		                          substr(substr(events.timestamp, 21, length(events.timestamp) - 21) || '000000000', 1, 9) || 'Z'
		                 END DESC
		                 LIMIT 1),
		                sessions.started_at
		            ) AS value
		        ) AS activity
		    ),
		    first_user_message = COALESCE((
		        SELECT substr(events.searchable_text, 1, 1024)
		        FROM events
		        WHERE events.session_id = sessions.id
		          AND events.kind = 'message'
		          AND events.message_role = 'user'
		        ORDER BY events.sequence
		        LIMIT 1
		    ), ''),
		    event_count = (
		        SELECT COUNT(*) FROM events WHERE events.session_id = sessions.id
		    )
		WHERE id = ?
	`, sessionID)
	return err
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
	// Canonical changes make every derived projection pending in the same
	// transaction, preventing readers from treating stale output as current.
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
