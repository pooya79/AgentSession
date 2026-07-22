package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/projection"
)

var _ projection.Store = (*ImportStore)(nil)
var _ projection.Reader = (*ImportStore)(nil)

const projectionLeaseDuration = time.Minute

func (s *ImportStore) Register(ctx context.Context, definitions []projection.Definition) (err error) {
	seen := make(map[projection.Kind]struct{}, len(definitions))
	for _, definition := range definitions {
		if err := definition.Validate(); err != nil {
			return fmt.Errorf("sqlite projection store: register: %w", err)
		}
		if _, exists := seen[definition.Kind]; exists {
			return fmt.Errorf("sqlite projection store: register: duplicate kind %q", definition.Kind)
		}
		seen[definition.Kind] = struct{}{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite projection store: register: begin transaction: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, definition := range definitions {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO projection_definitions (kind, target_version, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(kind) DO UPDATE SET target_version = excluded.target_version, updated_at = excluded.updated_at
		`, definition.Kind, definition.Version, now); err != nil {
			return fmt.Errorf("sqlite projection store: register %q: definition: %w", definition.Kind, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO session_projection_states (
				session_id, kind, status, target_version, target_revision, updated_at
			)
			SELECT id, ?, 'pending', ?, canonical_revision, ? FROM sessions WHERE true
			ON CONFLICT(session_id, kind) DO UPDATE SET
				status = CASE
					WHEN session_projection_states.target_version <> excluded.target_version
					  OR session_projection_states.target_revision <> excluded.target_revision
					THEN 'pending' ELSE session_projection_states.status END,
				target_version = excluded.target_version,
				target_revision = excluded.target_revision,
				attempt_count = CASE
					WHEN session_projection_states.target_version <> excluded.target_version
					  OR session_projection_states.target_revision <> excluded.target_revision
					THEN 0 ELSE session_projection_states.attempt_count END,
				run_token = CASE
					WHEN session_projection_states.target_version <> excluded.target_version
					  OR session_projection_states.target_revision <> excluded.target_revision
					THEN NULL ELSE session_projection_states.run_token END,
				lease_expires_at = CASE
					WHEN session_projection_states.target_version <> excluded.target_version
					  OR session_projection_states.target_revision <> excluded.target_revision
					THEN NULL ELSE session_projection_states.lease_expires_at END,
				started_at = CASE
					WHEN session_projection_states.target_version <> excluded.target_version
					  OR session_projection_states.target_revision <> excluded.target_revision
					THEN NULL ELSE session_projection_states.started_at END,
				failure_code = CASE WHEN session_projection_states.target_version <> excluded.target_version OR session_projection_states.target_revision <> excluded.target_revision THEN NULL ELSE session_projection_states.failure_code END,
				failure_summary = CASE WHEN session_projection_states.target_version <> excluded.target_version OR session_projection_states.target_revision <> excluded.target_revision THEN NULL ELSE session_projection_states.failure_summary END,
				failure_attempt = CASE WHEN session_projection_states.target_version <> excluded.target_version OR session_projection_states.target_revision <> excluded.target_revision THEN NULL ELSE session_projection_states.failure_attempt END,
				failure_at = CASE WHEN session_projection_states.target_version <> excluded.target_version OR session_projection_states.target_revision <> excluded.target_revision THEN NULL ELSE session_projection_states.failure_at END,
				updated_at = CASE
					WHEN session_projection_states.target_version <> excluded.target_version
					  OR session_projection_states.target_revision <> excluded.target_revision
					THEN excluded.updated_at ELSE session_projection_states.updated_at END
		`, definition.Kind, definition.Version, now); err != nil {
			return fmt.Errorf("sqlite projection store: register %q: session states: %w", definition.Kind, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite projection store: register: commit: %w", err)
	}
	return nil
}

func (s *ImportStore) CanonicalRevision(ctx context.Context, sessionID model.SessionID) (int64, bool, error) {
	if strings.TrimSpace(string(sessionID)) == "" {
		return 0, false, errors.New("sqlite projection store: canonical revision: session ID is required")
	}
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT canonical_revision FROM sessions WHERE id = ?`, sessionID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("sqlite projection store: canonical revision for %q: %w", sessionID, err)
	}
	return revision, true, nil
}

func (s *ImportStore) States(ctx context.Context, sessionID model.SessionID) ([]projection.State, error) {
	if strings.TrimSpace(string(sessionID)) == "" {
		return nil, errors.New("sqlite projection store: states: session ID is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT kind, status, target_version, target_revision, ready_version, ready_revision,
		       attempt_count, started_at, updated_at, failure_code, failure_summary, failure_attempt, failure_at
		FROM session_projection_states WHERE session_id = ?
		ORDER BY CASE kind WHEN 'search' THEN 1 WHEN 'git_correlation' THEN 2 WHEN 'findings' THEN 3 WHEN 'outcomes' THEN 4 ELSE 5 END
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("sqlite projection store: states for %q: %w", sessionID, err)
	}
	defer rows.Close()
	var states []projection.State
	for rows.Next() {
		state := projection.State{SessionID: sessionID}
		var readyVersion, started, failureCode, failureSummary, failureAt sql.NullString
		var readyRevision, failureAttempt sql.NullInt64
		var updated string
		if err := rows.Scan(&state.Kind, &state.Status, &state.TargetVersion, &state.TargetRevision,
			&readyVersion, &readyRevision, &state.AttemptCount, &started, &updated,
			&failureCode, &failureSummary, &failureAttempt, &failureAt); err != nil {
			return nil, fmt.Errorf("sqlite projection store: scan state for %q: %w", sessionID, err)
		}
		state.ReadyVersion = readyVersion.String
		if readyRevision.Valid {
			value := readyRevision.Int64
			state.ReadyRevision = &value
		}
		state.UpdatedAt, err = parseProjectionTime(updated)
		if err != nil {
			return nil, fmt.Errorf("sqlite projection store: state %q time: %w", state.Kind, err)
		}
		if started.Valid {
			value, parseErr := parseProjectionTime(started.String)
			if parseErr != nil {
				return nil, fmt.Errorf("sqlite projection store: state %q start time: %w", state.Kind, parseErr)
			}
			state.StartedAt = &value
		}
		if failureCode.Valid {
			at, parseErr := parseProjectionTime(failureAt.String)
			if parseErr != nil {
				return nil, fmt.Errorf("sqlite projection store: state %q failure time: %w", state.Kind, parseErr)
			}
			state.Diagnostic = &projection.Diagnostic{
				Kind: state.Kind, TargetVersion: state.TargetVersion, TargetRevision: state.TargetRevision,
				Code: failureCode.String, Summary: failureSummary.String, Attempt: failureAttempt.Int64, At: at,
			}
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite projection store: iterate states for %q: %w", sessionID, err)
	}
	return states, nil
}

func (s *ImportStore) Claim(ctx context.Context, sessionID model.SessionID, kind projection.Kind) (projection.Claim, bool, error) {
	if !kind.Valid() || strings.TrimSpace(string(sessionID)) == "" {
		return projection.Claim{}, false, errors.New("sqlite projection store: claim: valid session ID and kind are required")
	}
	token, err := newProjectionRunToken()
	if err != nil {
		return projection.Claim{}, false, fmt.Errorf("sqlite projection store: claim %q: %w", kind, err)
	}
	nowTime := time.Now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	leaseExpires := nowTime.Add(projectionLeaseDuration).Format(time.RFC3339Nano)
	claim := projection.Claim{SessionID: sessionID, Kind: kind, RunToken: token}
	err = s.db.QueryRowContext(ctx, `
		UPDATE session_projection_states
		SET status = 'running', run_token = ?, started_at = ?, lease_expires_at = ?, updated_at = ?, attempt_count = attempt_count + 1
		WHERE session_id = ? AND kind = ?
		  AND (status IN ('pending', 'failed') OR (status = 'running' AND julianday(lease_expires_at) <= julianday(?)))
		  AND target_revision = (SELECT canonical_revision FROM sessions WHERE id = ?)
		RETURNING target_version, target_revision, attempt_count
	`, token, now, leaseExpires, now, sessionID, kind, now, sessionID).Scan(&claim.Version, &claim.Revision, &claim.Attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return projection.Claim{}, false, nil
	}
	if err != nil {
		return projection.Claim{}, false, fmt.Errorf("sqlite projection store: claim %q for %q: %w", kind, sessionID, err)
	}
	return claim, true, nil
}

func (s *ImportStore) Renew(ctx context.Context, claim projection.Claim) (bool, error) {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE session_projection_states SET lease_expires_at = ?, updated_at = ?
		WHERE session_id = ? AND kind = ? AND status = 'running' AND run_token = ?
		  AND target_version = ? AND target_revision = ?
	`, now.Add(projectionLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		claim.SessionID, claim.Kind, claim.RunToken, claim.Version, claim.Revision)
	if err != nil {
		return false, fmt.Errorf("sqlite projection store: renew %q for %q: %w", claim.Kind, claim.SessionID, err)
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (s *ImportStore) Complete(ctx context.Context, claim projection.Claim) (bool, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
		UPDATE session_projection_states
		SET status = 'ready', ready_version = ?, ready_revision = ?, run_token = NULL, started_at = NULL, lease_expires_at = NULL,
		    updated_at = ?, failure_code = NULL, failure_summary = NULL, failure_attempt = NULL, failure_at = NULL
		WHERE session_id = ? AND kind = ? AND status = 'running' AND run_token = ?
		  AND target_version = ? AND target_revision = ?
		  AND EXISTS (SELECT 1 FROM sessions WHERE id = ? AND canonical_revision = ?)
	`, claim.Version, claim.Revision, now, claim.SessionID, claim.Kind, claim.RunToken,
		claim.Version, claim.Revision, claim.SessionID, claim.Revision)
	if err != nil {
		return false, fmt.Errorf("sqlite projection store: complete %q for %q: %w", claim.Kind, claim.SessionID, err)
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (s *ImportStore) Fail(ctx context.Context, claim projection.Claim, diagnostic projection.Diagnostic) (bool, error) {
	code, summary := boundedDiagnostic(diagnostic.Code, diagnostic.Summary)
	at := diagnostic.At.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE session_projection_states
		SET status = 'failed', run_token = NULL, started_at = NULL, lease_expires_at = NULL, updated_at = ?,
		    failure_code = ?, failure_summary = ?, failure_attempt = ?, failure_at = ?
		WHERE session_id = ? AND kind = ? AND status = 'running' AND run_token = ?
		  AND target_version = ? AND target_revision = ?
		  AND EXISTS (SELECT 1 FROM sessions WHERE id = ? AND canonical_revision = ?)
	`, at.Format(time.RFC3339Nano), code, summary, claim.Attempt, at.Format(time.RFC3339Nano),
		claim.SessionID, claim.Kind, claim.RunToken, claim.Version, claim.Revision, claim.SessionID, claim.Revision)
	if err != nil {
		return false, fmt.Errorf("sqlite projection store: fail %q for %q: %w", claim.Kind, claim.SessionID, err)
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (s *ImportStore) Release(ctx context.Context, claim projection.Claim) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE session_projection_states SET status = 'pending', run_token = NULL, started_at = NULL, lease_expires_at = NULL, updated_at = ?
		WHERE session_id = ? AND kind = ? AND status = 'running' AND run_token = ?
	`, time.Now().UTC().Format(time.RFC3339Nano), claim.SessionID, claim.Kind, claim.RunToken)
	if err != nil {
		return fmt.Errorf("sqlite projection store: release %q for %q: %w", claim.Kind, claim.SessionID, err)
	}
	return nil
}

func (s *ImportStore) Invalidate(ctx context.Context, sessionID model.SessionID, kind *projection.Kind) error {
	query := `
		UPDATE session_projection_states SET status = 'pending', target_revision = (
			SELECT canonical_revision FROM sessions WHERE id = session_projection_states.session_id
		), run_token = NULL, started_at = NULL, lease_expires_at = NULL, updated_at = ? WHERE session_id = ?`
	args := []any{time.Now().UTC().Format(time.RFC3339Nano), sessionID}
	if kind != nil {
		if !kind.Valid() {
			return fmt.Errorf("sqlite projection store: invalidate: invalid kind %q", *kind)
		}
		query += ` AND kind = ?`
		args = append(args, *kind)
	}
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("sqlite projection store: invalidate for %q: %w", sessionID, err)
	}
	return nil
}

func newProjectionRunToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "projection_" + hex.EncodeToString(value[:]), nil
}

func boundedDiagnostic(code, summary string) (string, string) {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 64 {
		code = "projection.build_failed"
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = "Projection build failed. Retry or inspect projection status."
	}
	if len(summary) > 256 {
		summary = summary[:256]
	}
	return code, summary
}

func parseProjectionTime(value string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, nil
	}
	return time.Parse("2006-01-02 15:04:05", value)
}
