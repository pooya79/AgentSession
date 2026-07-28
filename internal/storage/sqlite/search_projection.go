package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/search"
)

var _ search.ProjectionWriter = (*ImportStore)(nil)

func (s *ImportStore) StageSearchDocuments(ctx context.Context, token string, documents []search.Document) (err error) {
	if token == "" {
		return errors.New("sqlite search projection: build token is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite search projection: begin stage batch: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	for _, document := range documents {
		var timestamp any
		if document.Timestamp != "" {
			timestamp = document.Timestamp
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO search_document_stage (
				build_token, session_id, event_id, sequence, timestamp, kind,
				summary, content, tool_name, command_text, projection_version, canonical_revision
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, token, document.SessionID, document.EventID, document.Sequence, timestamp, document.Kind,
			document.Summary, document.Content, document.ToolName, document.CommandText,
			document.ProjectionVersion, document.CanonicalRevision); err != nil {
			return fmt.Errorf("sqlite search projection: insert staged document: %w", err)
		}
		for _, file := range document.Files {
			if _, err := tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO search_file_stage (build_token, event_id, path)
				VALUES (?, ?, ?)
			`, token, document.EventID, file); err != nil {
				return fmt.Errorf("sqlite search projection: insert staged file: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite search projection: commit stage batch: %w", err)
	}
	return nil
}

func (s *ImportStore) PublishSearchDocuments(ctx context.Context, token string, sessionID model.SessionID, version string, revision int64) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite search projection: begin publication: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	var invalid int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM search_document_stage
		WHERE build_token = ? AND (
			session_id <> ? OR projection_version <> ? OR canonical_revision <> ?
		)
	`, token, sessionID, version, revision).Scan(&invalid); err != nil {
		return fmt.Errorf("sqlite search projection: validate stage: %w", err)
	}
	if invalid != 0 {
		return errors.New("sqlite search projection: staged metadata does not match claim")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM search_documents WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("sqlite search projection: replace active documents: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO search_documents (
			session_id, event_id, sequence, timestamp, kind, summary, content,
			tool_name, command_text, projection_version, canonical_revision
		)
		SELECT session_id, event_id, sequence, timestamp, kind, summary, content,
		       tool_name, command_text, projection_version, canonical_revision
		FROM search_document_stage
		WHERE build_token = ?
		ORDER BY sequence
	`, token); err != nil {
		return fmt.Errorf("sqlite search projection: publish documents: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO search_document_files (document_rowid, path)
		SELECT d.rowid, f.path
		FROM search_file_stage f
		JOIN search_documents d ON d.event_id = f.event_id
		WHERE f.build_token = ? AND d.session_id = ?
	`, token, sessionID); err != nil {
		return fmt.Errorf("sqlite search projection: publish file associations: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite search projection: commit publication: %w", err)
	}
	return nil
}

func (s *ImportStore) CleanupSearchStage(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM search_document_stage WHERE build_token = ?`, token); err != nil {
		return fmt.Errorf("sqlite search projection: clean stage: %w", err)
	}
	return nil
}
