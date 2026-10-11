package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/pooya79/AgentSession/internal/analysis"
	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/projection"
	"github.com/pooya79/AgentSession/internal/storage"
)

// PublishFindings replaces a complete bounded result only for an active claim.
// Lifecycle completion is separate; until then this output cannot be served.
func (s *ImportStore) PublishFindings(ctx context.Context, claim projection.Claim, findings []model.Finding) (err error) {
	if claim.Kind != projection.KindFindings || len(findings) != 1 {
		return errors.New("sqlite findings: invalid complete result")
	}
	f := findings[0]
	if err := f.Validate(); err != nil {
		return fmt.Errorf("sqlite findings: invalid assessment: %w", err)
	}
	if f.SessionID != claim.SessionID || f.RuleID != analysis.RepeatedCommandRule || f.RuleVersion != analysis.RuleVersion || len(f.EventIDs) > analysis.MaxEvidenceReferences {
		return errors.New("sqlite findings: assessment identity or bound mismatch")
	}
	encoded, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(encoded) > 16384 {
		return errors.New("sqlite findings: assessment exceeds bound")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if e := tx.Rollback(); e != nil && !errors.Is(e, sql.ErrTxDone) {
			err = errors.Join(err, e)
		}
	}()
	// Taking the write lock with the guard prevents a newer import or claim
	// replacing the target between validation and publication.
	result, err := tx.ExecContext(ctx, `UPDATE session_projection_states SET run_token=run_token WHERE session_id=? AND kind='findings' AND status='running' AND run_token=? AND target_version=? AND target_revision=? AND EXISTS(SELECT 1 FROM sessions WHERE id=? AND canonical_revision=?)`, claim.SessionID, claim.RunToken, claim.Version, claim.Revision, claim.SessionID, claim.Revision)
	if err != nil {
		return fmt.Errorf("sqlite findings: guard publication: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("sqlite findings: superseded claim")
	}
	for _, id := range f.EventIDs {
		var owned int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE id=? AND session_id=?`, id, claim.SessionID).Scan(&owned); err != nil {
			return err
		}
		if owned != 1 {
			return errors.New("sqlite findings: evidence does not belong to assessed session")
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM findings WHERE session_id=?`, claim.SessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO findings(session_id,id,projection_version,canonical_revision,assessment_json) VALUES(?,?,?,?,?)`, claim.SessionID, f.ID, claim.Version, claim.Revision, string(encoded)); err != nil {
		return err
	}
	return tx.Commit()
}

// SessionFindings reads state and output in one snapshot, excluding every
// unusable revision/version and never substituting an empty ready assessment.
func (s *ImportStore) SessionFindings(ctx context.Context, session model.SessionID) (storage.FindingsSnapshot, error) {
	var snapshot storage.FindingsSnapshot
	var status sql.NullString
	var usable, stale bool
	var encoded sql.NullString
	err := s.db.QueryRowContext(ctx, `
 SELECT p.status,
  COALESCE(p.status='ready' AND p.ready_version=p.target_version AND p.ready_revision=p.target_revision AND p.target_revision=s.canonical_revision,0),
  COALESCE(p.ready_revision IS NOT NULL AND (p.ready_version<>p.target_version OR p.ready_revision<>s.canonical_revision),0),
  CASE WHEN p.status='ready' AND p.ready_version=p.target_version AND p.ready_revision=p.target_revision AND p.target_revision=s.canonical_revision THEN f.assessment_json END
 FROM sessions s LEFT JOIN session_projection_states p ON p.session_id=s.id AND p.kind='findings'
 LEFT JOIN findings f ON f.session_id=s.id AND f.projection_version=p.target_version AND f.canonical_revision=s.canonical_revision
 WHERE s.id=?`, session).Scan(&status, &usable, &stale, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, nil
	}
	if err != nil {
		return snapshot, fmt.Errorf("sqlite findings: read snapshot: %w", err)
	}
	snapshot.Found = true
	snapshot.Status = status.String
	snapshot.Stale = stale
	snapshot.Usable = usable && encoded.Valid
	if snapshot.Usable {
		var f model.Finding
		if err := json.Unmarshal([]byte(encoded.String), &f); err != nil {
			return snapshot, errors.New("sqlite findings: invalid stored assessment")
		}
		snapshot.Assessments = []model.Finding{f}
	}
	return snapshot, nil
}
