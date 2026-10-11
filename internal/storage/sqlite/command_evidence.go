package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/pooya79/AgentSession/internal/analysis"
	"github.com/pooya79/AgentSession/internal/model"
)

func persistCommandEvidence(ctx context.Context, tx *sql.Tx, event model.Event) error {
	c, ok := event.Data.(model.CommandData)
	if !ok {
		return nil
	}
	oversized := false
	bound := func(s string, n int) string {
		if len(s) > n || !utf8.ValidString(s) || strings.IndexByte(s, 0) >= 0 {
			oversized = true
			return ""
		}
		return s
	}
	invocation := bound(c.InvocationID, analysis.MaxInvocationBytes)
	command := bound(strings.TrimSpace(c.Command), analysis.MaxCommandBytes)
	directory := bound(c.WorkingDirectory, analysis.MaxDirectoryBytes)
	_, err := tx.ExecContext(ctx, `INSERT INTO command_evidence(event_id,invocation_id,phase,command_text,directory,exit_code,oversized) VALUES(?,?,?,?,?,?,?)`, event.ID, invocation, c.Phase, command, directory, c.ExitCode, oversized)
	return err
}

// WalkCommands reads only bounded canonical command fields. SQLite groups all
// linked records before walking invocations in identity/source order, so late
// contradictions and duplicate results cannot be hidden by example sampling.
func (s *ImportStore) WalkCommands(ctx context.Context, session model.SessionID, visit func(analysis.CommandEvidence) error) (analysis.CommandCoverage, error) {
	rows, err := s.db.QueryContext(ctx, commandWalkSQL, session, session, session)
	if err != nil {
		return analysis.CommandCoverage{}, fmt.Errorf("sqlite findings: read command evidence: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return analysis.CommandCoverage{}, err
		}
		var c analysis.CommandEvidence
		var exit sql.NullInt64
		if err := rows.Scan(&c.Command, &c.Directory, &c.StartEvent, &c.ResultEvent, &c.Start, &c.Completion, &exit, &c.GapBefore, &c.LaterSuccess.StartEvent, &c.LaterSuccess.ResultEvent, &c.LaterSuccess.Start, &c.RecoveryUncertain); err != nil {
			return analysis.CommandCoverage{}, fmt.Errorf("sqlite findings: scan command evidence: %w", err)
		}
		if exit.Valid {
			value := int(exit.Int64)
			c.ExitCode = &value
		}
		if err := visit(c); err != nil {
			return analysis.CommandCoverage{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return analysis.CommandCoverage{}, err
	}
	if err := rows.Close(); err != nil {
		return analysis.CommandCoverage{}, err
	}
	var coverage analysis.CommandCoverage
	if err := s.db.QueryRowContext(ctx, commandGapCountSQL, session, session).Scan(&coverage.Gaps, &coverage.TotalEvents); err != nil {
		return analysis.CommandCoverage{}, fmt.Errorf("sqlite findings: count uncertainty: %w", err)
	}
	examples, err := s.db.QueryContext(ctx, `SELECT event_id FROM (`+commandGapsSQL+`) WHERE event_id IS NOT NULL ORDER BY position,event_id LIMIT ?`, session, session, analysis.MaxEvidenceReferences)
	if err != nil {
		return analysis.CommandCoverage{}, err
	}
	defer examples.Close()
	for examples.Next() {
		var id model.EventID
		if err := examples.Scan(&id); err != nil {
			return analysis.CommandCoverage{}, err
		}
		coverage.EventIDs = append(coverage.EventIDs, id)
	}
	return coverage, examples.Err()
}

// Unknown and generic tool evidence may hide relevant commands. Malformed
// records are located by retained source order, never by diagnostic prose.
// Conflicting identity fields make every possible membership uncertain.
const commandConflictPrefixSQL = `EXISTS(
 SELECT 1 FROM command_evidence other JOIN events oe ON oe.id=other.event_id
 WHERE oe.session_id=e.session_id AND other.invocation_id=c.invocation_id AND c.invocation_id<>'' AND `
const commandTextConflictSQL = commandConflictPrefixSQL + `(other.command_text<>'' AND c.command_text<>'' AND other.command_text<>c.command_text))`
const commandDirectoryConflictSQL = commandConflictPrefixSQL + `(other.directory<>'' AND c.directory<>'' AND other.directory<>c.directory))`
const commandIdentityConflictSQL = `(` + commandTextConflictSQL + ` OR ` + commandDirectoryConflictSQL + `)`
const commandGapsSQL = `
 SELECT e.sequence AS position,
 CASE WHEN e.kind<>'command' OR c.invocation_id='' OR NOT EXISTS(SELECT 1 FROM command_evidence start JOIN events se ON se.id=start.event_id WHERE se.session_id=e.session_id AND start.invocation_id=c.invocation_id AND start.phase='started') THEN e.id END AS event_id,
 CASE WHEN ` + commandTextConflictSQL + ` THEN NULL ELSE COALESCE(NULLIF(c.command_text,''),(SELECT MIN(NULLIF(start.command_text,'')) FROM command_evidence start JOIN events se ON se.id=start.event_id WHERE se.session_id=e.session_id AND start.invocation_id=c.invocation_id AND start.phase='started')) END AS command,
 CASE WHEN ` + commandDirectoryConflictSQL + ` THEN NULL ELSE NULLIF(c.directory,'') END AS directory,
 (SELECT MIN(se.sequence) FROM command_evidence start JOIN events se ON se.id=start.event_id WHERE se.session_id=e.session_id AND start.invocation_id=c.invocation_id AND c.invocation_id<>'' AND start.phase='started') AS invocation_start
 FROM events e LEFT JOIN command_evidence c ON c.event_id=e.id
 WHERE e.session_id=? AND (e.kind IN ('unknown','tool_call','tool_result') OR
  (e.kind='command' AND (c.invocation_id='' OR c.phase NOT IN ('started','completed') OR c.oversized=1 OR ` + commandIdentityConflictSQL + ` OR (c.phase='started' AND (c.command_text='' OR c.directory='')) OR
   NOT EXISTS(SELECT 1 FROM command_evidence start JOIN events se ON se.id=start.event_id WHERE se.session_id=e.session_id AND start.invocation_id=c.invocation_id AND start.phase='started'))))
 UNION ALL
 SELECT CASE WHEN r.record_sequence IS NULL OR EXISTS(SELECT 1 FROM events e WHERE e.session_id=d.session_id AND e.raw_record_sequence IS NULL) THEN NULL
  ELSE COALESCE((SELECT MIN(e.sequence) FROM events e WHERE e.session_id=d.session_id AND e.raw_record_sequence>=r.record_sequence),9223372036854775807) END, NULL AS event_id, NULL AS command, NULL AS directory, NULL AS invocation_start
 FROM record_diagnostics d JOIN raw_records r ON r.id=d.raw_record_id
 WHERE d.session_id=? AND d.interpretation_reason<>'' GROUP BY d.raw_record_id
`
const commandGapCountSQL = `SELECT COUNT(*), COUNT(event_id) FROM (` + commandGapsSQL + `)`
const commandWalkSQL = `
 WITH records AS (
  SELECT c.*, e.sequence, e.session_id FROM command_evidence c JOIN events e ON e.id=c.event_id WHERE e.session_id=?
 ), invocations AS (
  SELECT invocation_id,
   MIN(CASE WHEN phase='started' THEN command_text END) AS command,
   MIN(CASE WHEN phase='started' THEN directory END) AS directory,
   MIN(CASE WHEN phase='started' THEN sequence END) AS start,
   MIN(CASE WHEN phase='completed' THEN sequence END) AS completion,
   MIN(NULLIF(command_text,''))=MAX(NULLIF(command_text,'')) AND MIN(NULLIF(directory,''))=MAX(NULLIF(directory,'')) AND MAX(oversized)=0 AS identity_established,
   MIN(CASE WHEN phase='completed' THEN sequence END)>MAX(CASE WHEN phase='started' THEN sequence END) AS completion_established,
   CASE WHEN MIN(NULLIF(command_text,''))=MAX(NULLIF(command_text,'')) AND MIN(NULLIF(directory,''))=MAX(NULLIF(directory,'')) AND MAX(oversized)=0
     AND SUM(CASE WHEN phase='completed' AND exit_code IS NULL THEN 1 ELSE 0 END)=0
     AND MIN(CASE WHEN phase='completed' THEN exit_code END)=MAX(CASE WHEN phase='completed' THEN exit_code END)
     AND MIN(CASE WHEN phase='completed' THEN sequence END)>MAX(CASE WHEN phase='started' THEN sequence END)
    THEN MIN(CASE WHEN phase='completed' THEN exit_code END) END AS exit_code
  FROM records WHERE invocation_id<>'' AND phase IN ('started','completed') GROUP BY invocation_id
  HAVING COUNT(CASE WHEN phase='started' THEN 1 END)>0
 ), ordered AS (
  SELECT *, LAG(start) OVER(PARTITION BY command,directory ORDER BY start) AS previous_start FROM invocations
 ), gaps AS (` + commandGapsSQL + `)
 SELECT i.command,i.directory,
  (SELECT event_id FROM records WHERE sequence=i.start),
  COALESCE((SELECT event_id FROM records WHERE sequence=i.completion),''),
  i.start,COALESCE(i.completion,i.start),i.exit_code,
  EXISTS(SELECT 1 FROM gaps WHERE (position IS NULL OR (position>i.previous_start AND position<=i.start)) AND (gaps.command IS NULL OR gaps.command=i.command) AND (gaps.directory IS NULL OR gaps.directory=i.directory)),
  COALESCE((SELECT event_id FROM records WHERE sequence=later.start),''),
  COALESCE((SELECT event_id FROM records WHERE sequence=later.completion),''),COALESCE(later.start,0),
  (EXISTS(SELECT 1 FROM gaps WHERE (position IS NULL OR position>i.completion) AND (invocation_start IS NULL OR invocation_start>i.completion) AND (gaps.command IS NULL OR gaps.command=i.command) AND (gaps.directory IS NULL OR gaps.directory=i.directory)) OR
   EXISTS(SELECT 1 FROM invocations next WHERE next.command=i.command AND next.directory=i.directory AND next.start>i.completion AND next.exit_code IS NULL))
 FROM ordered i LEFT JOIN invocations later ON later.start=(
  SELECT MIN(next.start) FROM invocations next WHERE i.identity_established AND i.completion_established AND next.command=i.command AND next.directory=i.directory AND next.exit_code=0 AND next.start>i.completion
 ) ORDER BY i.command,i.directory,i.start
`
