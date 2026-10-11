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

type moveScopeCounts struct{ total, verified, missing, uncertain int64 }

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

// AssessMissingVerification evaluates each confirmed affected Go path against
// later declared targets. Failure is verification performed, independent of
// unresolved-failure analysis. All paths are assessed; only links are sampled.
func AssessMissingVerification(ctx context.Context, session model.SessionID, reader ChangeReader) (model.Finding, error) {
	f := model.Finding{ID: model.FindingID(fmt.Sprintf("finding_%x", sha256.Sum256([]byte("agentsession:assessment:v1\x00"+string(session)+"\x00"+MissingVerificationRule)))), SessionID: session, RuleID: MissingVerificationRule, RuleVersion: RuleVersion}
	var confirmed, applicable, verified, missing, exempt, uncertain, references, configurations int64
	var missingLinks, otherLinks []model.EventID
	var origins, destinations moveScopeCounts
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
			if id == "" {
				continue
			}
			retained := false
			for _, old := range otherLinks {
				if old == id {
					retained = true
					break
				}
			}
			if retained {
				continue
			}
			references++
			add(&otherLinks, id)
		}
	}
	assessPath := func(c ChangeEvidence) error {
		file, ok := establishedPath(c.Path, c.Directory)
		if !ok {
			uncertain++
			return nil
		}
		if documentationPath(file) {
			exempt++
			return nil
		}
		configuration := path.Base(file) == "go.mod" || path.Base(file) == "go.sum"
		if configuration {
			configurations++
			known, links, err := configurationScope(ctx, session, reader, c, file)
			if err != nil {
				return err
			}
			record(links...)
			if !known {
				uncertain++
				return nil
			}
		} else if !ordinaryGoPath(file) {
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
			if configuration && (!recursive || directory != target) {
				return nil
			}
			if directory != target && !(recursive && containsDirectory(target, directory)) {
				return nil
			}
			if v.Kind == VerificationBuild && strings.HasSuffix(file, "_test.go") {
				return nil
			}
			if !configuration && recursive && directory != target {
				boundary, err := crossesRecordedModule(ctx, session, reader, target, directory, command.Start)
				if err != nil {
					return err
				}
				if boundary != "" {
					limited = true
					record(boundary, command.StartEvent, command.ResultEvent)
					return nil
				}
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
				record(g.EventID)
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
	}
	assessMoveScope := func(c ChangeEvidence, counts *moveScopeCounts) error {
		v, m, u := verified, missing, uncertain
		err := assessPath(c)
		counts.total++
		counts.verified += verified - v
		counts.missing += missing - m
		counts.uncertain += uncertain - u
		return err
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
		if c.Operation == model.FileMutationUnknown {
			uncertain++
			return nil
		}
		if c.PreviousPath != "" || c.Operation == model.FileMutationRename {
			origin := c
			origin.Path = c.PreviousPath
			if err := assessMoveScope(origin, &origins); err != nil {
				return err
			}
			return assessMoveScope(c, &destinations)
		}
		return assessPath(c)
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
	f.Metadata = map[string]string{"confirmed_changes": strconv.FormatInt(confirmed, 10), "configuration_scopes": strconv.FormatInt(configurations, 10), "applicable_changes": strconv.FormatInt(applicable, 10), "verified_changes": strconv.FormatInt(verified, 10), "missing_changes": strconv.FormatInt(missing, 10), "documentation_changes": strconv.FormatInt(exempt, 10), "uncertainties": strconv.FormatInt(uncertain, 10), "omitted_references": strconv.FormatInt(omitted, 10)}
	for key, count := range map[string]int64{
		"move_origins": origins.total, "move_origins_verified": origins.verified, "move_origins_missing": origins.missing, "move_origins_uncertain": origins.uncertain,
		"move_destinations": destinations.total, "move_destinations_verified": destinations.verified, "move_destinations_missing": destinations.missing, "move_destinations_uncertain": destinations.uncertain,
	} {
		f.Metadata[key] = strconv.FormatInt(count, 10)
	}
	f.Explanation = fmt.Sprintf("Within the currently imported record in source order: %d confirmed mutations, %d applicable Go scopes, %d with completed relevant verification, %d conclusively missing verification, %d documentation-only exemptions. Both origin and destination of a confirmed move are assessed independently, including file roles and exclusions. A suitable check must begin after the change and target its package/directory; a failed completed check counts as verification performed. Earlier and still-running checks do not verify a later edit. Build checks exclude test files. Confirmed go.mod/go.sum configuration requires a recursive target rooted exactly at an established module directory; isolated package checks are insufficient. Module roots require recorded confirmed go.mod effects at or before the change. Workspace membership and nested module relationships remain uncertain. Declared local or recursive targets establish relevance only, not actual file participation, correctness, test coverage, or dependency coverage.", confirmed, applicable, verified, missing, exempt)
	if origins.total > 0 {
		f.Explanation += fmt.Sprintf(" Move origin scopes: %d verified, %d missing, %d uncertain; destination scopes: %d verified, %d missing, %d uncertain.", origins.verified, origins.missing, origins.uncertain, destinations.verified, destinations.missing, destinations.uncertain)
	}
	if configurations > 0 {
		f.Explanation += fmt.Sprintf(" %d module configuration scopes evaluated, requiring whole-module declared targets.", configurations)
	}
	if uncertain > 0 {
		f.Explanation += " Unconfirmed or contradictory changes, unknown results/commands, interpretation gaps, unfamiliar file roles, unclear path bases, unknown move endpoints, and module/workspace boundaries limit affected conclusions. Independent conclusive instances remain usable."
	}
	if omitted > 0 {
		f.Explanation += fmt.Sprintf(" Representative event links only; %d evaluated reference occurrences omitted (including repeated links beyond the sample bound). The canonical timeline retains the full evidence.", omitted)
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
	return strings.HasSuffix(file, ".go") && ordinaryGoDirectory(path.Dir(file)) && ordinaryGoComponent(path.Base(file))
}

func ordinaryGoDirectory(directory string) bool {
	for _, part := range strings.Split(strings.TrimPrefix(directory, "/"), "/") {
		if !ordinaryGoComponent(part) {
			return false
		}
	}
	return true
}

func ordinaryGoComponent(part string) bool {
	return part != "vendor" && part != "testdata" && !strings.HasPrefix(part, ".") && !strings.HasPrefix(part, "_")
}
