package analysis

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pooya79/AgentSession/internal/model"
)

const MissingVerificationRule = "missing_verification"

// ChangeEvidence is one affected path after linked results have been reconciled.
// Unconfirmed, conflicting and unbounded evidence has an uncertain status.
type ChangeEvidence struct {
	EventID                       model.EventID
	Position                      int64
	Status                        model.ChangeStatus
	Path, PreviousPath, Directory string
	Operation                     model.FileMutationOperation
}

// EvidenceGap locates uninterpreted evidence. Nil Position means its ordering
// is unavailable. A known invocation start limits when it could verify an edit.
type EvidenceGap struct {
	EventID                   model.EventID
	Position, InvocationStart *int64
}

// ChangeReader streams bounded fields, without raw records or patch/output text.
// Implementations release database cursors before invoking change callbacks,
// allowing the rule to stream commands/gaps for each path with bounded memory.
type ChangeReader interface {
	CommandReader
	WalkChanges(context.Context, model.SessionID, func(ChangeEvidence) error) error
	WalkEvidenceGaps(context.Context, model.SessionID, func(EvidenceGap) error) error
}

// AssessMissingVerification evaluates each confirmed ordinary Go path against
// later declared targets. Failure is verification performed, independent of
// unresolved-failure analysis. All paths are assessed; only links are sampled.
func AssessMissingVerification(ctx context.Context, session model.SessionID, reader ChangeReader) (model.Finding, error) {
	f := model.Finding{ID: model.FindingID(fmt.Sprintf("finding_%x", sha256.Sum256([]byte("agentsession:assessment:v1\x00"+string(session)+"\x00"+MissingVerificationRule)))), SessionID: session, RuleID: MissingVerificationRule, RuleVersion: RuleVersion}
	var confirmed, applicable, verified, missing, exempt, uncertain, references int64
	var missingLinks, otherLinks []model.EventID
	add := func(to *[]model.EventID, ids ...model.EventID) {
		for _, id := range ids {
			if id == "" {
				continue
			}
			found := false
			for _, old := range *to {
				if old == id {
					found = true
					break
				}
			}
			if !found && len(*to) < MaxEvidenceReferences {
				*to = append(*to, id)
			}
		}
	}
	record := func(ids ...model.EventID) {
		for _, id := range ids {
			if id != "" {
				references++
			}
		}
		add(&otherLinks, ids...)
	}
	err := reader.WalkChanges(ctx, session, func(c ChangeEvidence) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if c.EventID != "" {
			record(c.EventID)
		}
		switch c.Status {
		case model.ChangeFailed:
			// A failed multi-file application can have partial effects. Recorded
			// intended paths do not establish which mutations, if any, occurred.
			if c.Path != "" {
				uncertain++
			}
			return nil
		case model.ChangeNoOp:
			return nil
		case model.ChangeConfirmed:
			confirmed++
		default:
			uncertain++
			return nil
		}
		file, ok := establishedPath(c.Path, c.Directory)
		if !ok || c.PreviousPath != "" || c.Operation == model.FileMutationRename || c.Operation == model.FileMutationUnknown {
			uncertain++
			return nil
		}
		if documentationPath(file) {
			exempt++
			return nil
		}
		if !ordinaryGoPath(file) {
			uncertain++
			return nil
		}
		applicable++
		checked, limited := false, false
		_, err := reader.WalkCommands(ctx, session, func(command CommandEvidence) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if command.StartEvent != "" && command.Start <= c.Position {
				return nil
			}
			v, ok := RecognizeVerification(command.Command, command.Directory)
			if !ok {
				limited = true
				record(command.StartEvent, command.ResultEvent)
				return nil
			}
			target, valid := establishedPath(strings.TrimSuffix(v.Target, "/..."), command.Directory)
			if !valid {
				limited = true
				return nil
			}
			recursive := strings.HasSuffix(v.Target, "/...")
			directory := path.Dir(file)
			if directory != target && !(recursive && strings.HasPrefix(directory, strings.TrimSuffix(target, "/")+"/")) {
				return nil
			}
			if v.Kind == VerificationBuild && strings.HasSuffix(file, "_test.go") {
				return nil
			}
			record(command.StartEvent, command.ResultEvent)
			if command.StartEvent == "" || command.ResultEvent == "" || command.Completion <= command.Start || command.ExitCode == nil {
				limited = true
				return nil
			}
			checked = true
			return nil
		})
		if err != nil {
			return err
		}
		if checked {
			verified++
			return nil
		}
		err = reader.WalkEvidenceGaps(ctx, session, func(g EvidenceGap) error {
			if (g.Position == nil || *g.Position > c.Position) && (g.InvocationStart == nil || *g.InvocationStart > c.Position) {
				limited = true
				add(&otherLinks, g.EventID)
			}
			return ctx.Err()
		})
		if err != nil {
			return err
		}
		if limited {
			uncertain++
		} else {
			missing++
			add(&missingLinks, c.EventID)
		}
		return nil
	})
	if err != nil {
		return model.Finding{}, fmt.Errorf("assess missing verification: %w", err)
	}
	// Gaps may also hide applicable changes, but do not invalidate independently
	// established instances or a completed relevant check for a known change.
	err = reader.WalkEvidenceGaps(ctx, session, func(g EvidenceGap) error {
		uncertain++
		if g.EventID != "" {
			record(g.EventID)
		}
		return ctx.Err()
	})
	if err != nil {
		return model.Finding{}, fmt.Errorf("assess missing verification coverage: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return model.Finding{}, err
	}
	switch {
	case missing > 0:
		f.State = model.FindingTriggered
	case uncertain > 0:
		f.State = model.FindingInsufficientEvidence
	case applicable > 0:
		f.State = model.FindingNotTriggered
	default:
		f.State = model.FindingNotApplicable
	}
	add(&f.EventIDs, missingLinks...)
	add(&f.EventIDs, otherLinks...)
	omitted := references - int64(len(f.EventIDs))
	if omitted < 0 {
		omitted = 0
	}
	f.Metadata = map[string]string{"confirmed_changes": strconv.FormatInt(confirmed, 10), "applicable_changes": strconv.FormatInt(applicable, 10), "verified_changes": strconv.FormatInt(verified, 10), "missing_changes": strconv.FormatInt(missing, 10), "documentation_changes": strconv.FormatInt(exempt, 10), "uncertainties": strconv.FormatInt(uncertain, 10), "omitted_references": strconv.FormatInt(omitted, 10)}
	f.Explanation = fmt.Sprintf("Within the currently imported record in source order: %d confirmed affected paths, %d applicable ordinary Go changes, %d with completed relevant verification, %d conclusively missing verification, %d documentation-only exemptions. A suitable check must begin after the change and target its package/directory; a failed completed check counts as verification performed. Earlier and still-running checks do not verify a later edit. Build checks exclude test files. Declared local or recursive targets establish relevance only, not actual file participation, correctness, test coverage, or dependency coverage.", confirmed, applicable, verified, missing, exempt)
	if uncertain > 0 {
		f.Explanation += " Unconfirmed or contradictory changes, unknown results/commands, interpretation gaps, unfamiliar file roles, unclear path bases, moves, and module/workspace boundaries limit affected conclusions. Independent conclusive instances remain usable."
	}
	if omitted > 0 {
		f.Explanation += fmt.Sprintf(" Representative event links only; %d evaluated evidence references omitted. The canonical timeline retains the full evidence.", omitted)
	}
	return f, nil
}

func establishedPath(value, base string) (string, bool) {
	valid := func(v string) bool {
		return v != "" && len(v) <= MaxDirectoryBytes && utf8.ValidString(v) && !strings.ContainsAny(v, "\x00\r\n\\:")
	}
	if !valid(value) || strings.HasPrefix(value, "//") {
		return "", false
	}
	if !strings.HasPrefix(value, "/") {
		if !valid(base) || !strings.HasPrefix(base, "/") {
			return "", false
		}
		value = base + "/" + value
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", false
		}
	}
	return path.Clean(value), true
}

func documentationPath(file string) bool {
	switch strings.ToLower(path.Ext(file)) {
	case ".md", ".rst", ".adoc":
		return true
	}
	switch path.Base(file) {
	case "LICENSE", "COPYING":
		return true
	}
	return false
}

func ordinaryGoPath(file string) bool {
	if !strings.HasSuffix(file, ".go") {
		return false
	}
	for _, part := range strings.Split(strings.TrimPrefix(file, "/"), "/") {
		if part == "vendor" || part == "testdata" || strings.HasPrefix(part, ".") || strings.HasPrefix(part, "_") {
			return false
		}
	}
	return true
}
