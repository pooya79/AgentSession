package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/pooya79/AgentSession/internal/analysis"
	"github.com/pooya79/AgentSession/internal/model"
)

func persistChangeEvidence(ctx context.Context, tx *sql.Tx, event model.Event) error {
	p, ok := event.Data.(model.PatchData)
	if !ok {
		if mutation, yes := event.Data.(model.FileMutationData); yes {
			// Other adapters do not yet establish change confirmation.
			p = model.PatchData{Status: model.ChangeUnknown, Changes: []model.FileMutationData{mutation}}
		} else {
			return nil
		}
	}
	bound := func(s string, n int) string {
		if len(s) > n || !utf8.ValidString(s) || strings.ContainsAny(s, "\x00\r\n") {
			p.Status = model.ChangeUnknown
			return ""
		}
		return s
	}
	id := bound(p.ChangeID, analysis.MaxInvocationBytes)
	directory := bound(p.WorkingDirectory, analysis.MaxDirectoryBytes)
	// Hash the complete canonical effect before bounding fields, so differently
	// oversized paths or results cannot silently collapse to one confirmation.
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	_ = encoder.Encode(p.Status)
	_ = encoder.Encode(p.WorkingDirectory)
	for _, change := range p.Changes {
		_ = encoder.Encode(change)
	}
	signature := fmt.Sprintf("%x", hash.Sum(nil))
	// Store one row even for absent paths so uncertain/no-op/failed results remain
	// distinguishable. Individual paths have fixed field bounds, never sampling.
	paths := p.Changes
	for _, change := range paths {
		_ = bound(change.Path, analysis.MaxDirectoryBytes)
		_ = bound(change.PreviousPath, analysis.MaxDirectoryBytes)
	}
	if id == "" && p.Status != model.ChangeNoOp && p.Status != model.ChangeFailed {
		p.Status = model.ChangeUnknown
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO change_evidence(event_id,change_id,status,directory,signature) VALUES(?,?,?,?,?)`, event.ID, id, p.Status, directory, signature); err != nil {
		return err
	}
	for i, change := range paths {
		change.Path = bound(change.Path, analysis.MaxDirectoryBytes)
		change.PreviousPath = bound(change.PreviousPath, analysis.MaxDirectoryBytes)
		if _, err := tx.ExecContext(ctx, `INSERT INTO change_paths(event_id,ordinal,path,previous_path,operation) VALUES(?,?,?,?,?)`, event.ID, i, change.Path, change.PreviousPath, change.Operation); err != nil {
			return err
		}
	}
	return nil
}

// WalkChanges reconciles linked requests/results over the complete record.
// Keyset pages release their cursor before callbacks; nested command/gap reads
// therefore work even with a single database connection and bounded Go memory.
func (s *ImportStore) WalkChanges(ctx context.Context, session model.SessionID, visit func(analysis.ChangeEvidence) error) error {
	position, ordinal := int64(-1), int64(-2)
	for {
		rows, err := s.db.QueryContext(ctx, changeWalkSQL, session, position, position, ordinal)
		if err != nil {
			return fmt.Errorf("sqlite findings: read changes: %w", err)
		}
		type item struct {
			evidence analysis.ChangeEvidence
			ordinal  int64
		}
		page := make([]item, 0, 128)
		for rows.Next() {
			var v item
			if err = rows.Scan(&v.evidence.EventID, &v.evidence.Position, &v.ordinal, &v.evidence.Status, &v.evidence.Directory, &v.evidence.Path, &v.evidence.PreviousPath, &v.evidence.Operation); err != nil {
				break
			}
			page = append(page, v)
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return fmt.Errorf("sqlite findings: scan changes: %w", err)
		}
		for _, v := range page {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(v.evidence); err != nil {
				return err
			}
			position, ordinal = v.evidence.Position, v.ordinal
		}
		if len(page) < 128 {
			return ctx.Err()
		}
	}
}

const changeWalkSQL = `WITH records AS (
 SELECT c.*,e.sequence,CASE WHEN c.change_id='' THEN e.id ELSE c.change_id END AS identity
 FROM change_evidence c JOIN events e ON e.id=c.event_id WHERE e.session_id=?
), selected AS (
 SELECT *, ROW_NUMBER() OVER(PARTITION BY identity ORDER BY status='requested',sequence) AS choice,
  (SELECT COUNT(DISTINCT other.signature) FROM records other WHERE other.identity=r.identity AND other.status<>'requested') AS results
 FROM records r
)
SELECT c.event_id,c.sequence,COALESCE(p.ordinal,-1),CASE WHEN c.results>1 THEN 'unknown' ELSE c.status END,c.directory,COALESCE(p.path,''),COALESCE(p.previous_path,''),COALESCE(p.operation,'unknown')
FROM selected c LEFT JOIN change_paths p ON p.event_id=c.event_id
WHERE c.choice=1 AND (c.sequence>? OR (c.sequence=? AND COALESCE(p.ordinal,-1)>?))
ORDER BY c.sequence,COALESCE(p.ordinal,-1) LIMIT 128`

// WalkEvidenceGaps exposes authoritative source ordering without diagnostic or
// output prose. Nil positions conservatively represent unavailable ordering.
func (s *ImportStore) WalkEvidenceGaps(ctx context.Context, session model.SessionID, visit func(analysis.EvidenceGap) error) error {
	rows, err := s.db.QueryContext(ctx, `SELECT position,event_id,invocation_start FROM (`+commandGapsSQL+`) ORDER BY position,event_id`, session, session)
	if err != nil {
		return fmt.Errorf("sqlite findings: read interpretation gaps: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var gap analysis.EvidenceGap
		var position, start sql.NullInt64
		var event sql.NullString
		if err := rows.Scan(&position, &event, &start); err != nil {
			return err
		}
		if position.Valid {
			gap.Position = &position.Int64
		}
		if start.Valid {
			gap.InvocationStart = &start.Int64
		}
		gap.EventID = model.EventID(event.String)
		if err := visit(gap); err != nil {
			return err
		}
	}
	return rows.Err()
}
