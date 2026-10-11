package analysis

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"

	"github.com/pooya79/AgentSession/internal/model"
)

const UnresolvedVerificationRule = "unresolved_failed_verification"

// AssessUnresolvedVerification evaluates every recognized verification failure.
// Only an established same-identity success starting after completion resolves
// it. Compatible gaps can hide recovery, but cannot erase explicit recovery or
// an independently established unresolved failure.
func AssessUnresolvedVerification(ctx context.Context, session model.SessionID, reader CommandReader) (model.Finding, error) {
	f := model.Finding{ID: model.FindingID(fmt.Sprintf("finding_%x", sha256.Sum256([]byte("agentsession:assessment:v1\x00"+string(session)+"\x00"+UnresolvedVerificationRule)))), SessionID: session, RuleID: UnresolvedVerificationRule, RuleVersion: RuleVersion}
	var attempts, failures, recovered, unresolved, uncertain, references int64
	var settledUnknown int64
	var tests, staticChecks, builds int64
	var fallback, recoveryLinks, uncertaintyLinks []model.EventID
	add := func(target *[]model.EventID, ids ...model.EventID) {
		for _, id := range ids {
			if id == "" {
				continue
			}
			duplicate := false
			for _, seen := range *target {
				if seen == id {
					duplicate = true
					break
				}
			}
			if !duplicate && len(*target) < MaxEvidenceReferences {
				*target = append(*target, id)
			}
		}
	}
	coverage, err := reader.WalkCommands(ctx, session, func(c CommandEvidence) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, id := range []model.EventID{c.StartEvent, c.ResultEvent} {
			if id != "" {
				references++
			}
		}
		add(&fallback, c.StartEvent, c.ResultEvent)
		v, ok := RecognizeVerification(c.Command, c.Directory)
		if !ok {
			uncertain++
			add(&uncertaintyLinks, c.StartEvent, c.ResultEvent)
			return nil
		}
		attempts++
		switch v.Kind {
		case VerificationTest:
			tests++
		case VerificationStaticCheck:
			staticChecks++
		case VerificationBuild:
			builds++
		}
		if c.StartEvent == "" || c.ResultEvent == "" || c.Completion <= c.Start {
			uncertain++
			add(&uncertaintyLinks, c.StartEvent, c.ResultEvent)
			return nil
		}
		hasRecovery := c.LaterSuccess.StartEvent != "" && c.LaterSuccess.ResultEvent != "" && c.LaterSuccess.Start > c.Completion
		if c.ExitCode == nil {
			if hasRecovery {
				// Either possible result leaves no unresolved failure after this
				// success. The unknown exit is not converted into a known result.
				settledUnknown++
				add(&recoveryLinks, c.StartEvent, c.ResultEvent, c.LaterSuccess.StartEvent, c.LaterSuccess.ResultEvent)
			} else {
				uncertain++
				add(&uncertaintyLinks, c.StartEvent, c.ResultEvent)
			}
			return nil
		}
		if *c.ExitCode == 0 {
			return nil
		}
		failures++
		switch {
		case hasRecovery:
			recovered++
			add(&recoveryLinks, c.StartEvent, c.ResultEvent, c.LaterSuccess.StartEvent, c.LaterSuccess.ResultEvent)
		case c.RecoveryUncertain:
			uncertain++
			add(&uncertaintyLinks, c.StartEvent, c.ResultEvent)
		default:
			unresolved++
			add(&f.EventIDs, c.StartEvent, c.ResultEvent)
		}
		return nil
	})
	if err != nil {
		return model.Finding{}, fmt.Errorf("assess unresolved verification: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return model.Finding{}, err
	}
	uncertain += coverage.Gaps
	switch {
	case unresolved > 0:
		f.State = model.FindingTriggered
	case uncertain > 0:
		f.State = model.FindingInsufficientEvidence
	case attempts > 0:
		f.State = model.FindingNotTriggered
	default:
		f.State = model.FindingNotApplicable
	}
	add(&f.EventIDs, recoveryLinks...)
	add(&f.EventIDs, uncertaintyLinks...)
	add(&f.EventIDs, coverage.EventIDs...)
	add(&f.EventIDs, fallback...)
	omitted := references + coverage.TotalEvents - int64(len(f.EventIDs))
	f.Metadata = map[string]string{"invocations": strconv.FormatInt(attempts, 10), "failures": strconv.FormatInt(failures, 10), "recovered": strconv.FormatInt(recovered, 10), "unresolved": strconv.FormatInt(unresolved, 10), "uncertainties": strconv.FormatInt(uncertain, 10), "omitted_references": strconv.FormatInt(omitted, 10), "tests": strconv.FormatInt(tests, 10), "static_checks": strconv.FormatInt(staticChecks, 10), "builds": strconv.FormatInt(builds, 10), "settled_unknown_results": strconv.FormatInt(settledUnknown, 10)}
	f.Explanation = fmt.Sprintf("Within the currently imported record in source order: %d recognized Go verification invocations (%d tests, %d static checks, %d builds), %d failed results, %d failures with later recorded recovery, %d conclusively unresolved failures. Recovery requires exact recorded command text trimmed at the ends and an established matching directory, with the successful invocation starting after the failure completed. Parallel success, a different check, pending reruns, and assistant assurance do not establish recovery.", attempts, tests, staticChecks, builds, failures, recovered, unresolved)
	if settledUnknown > 0 {
		f.Explanation += fmt.Sprintf(" %d completed invocations retain unknown exit results, but cannot leave an unresolved failure after their later same-identity success.", settledUnknown)
	}
	if uncertain > 0 {
		f.Explanation += " Missing, conflicting, unsupported, or oversized evidence limits affected conclusions; compatible gaps or unknown results may hide recovery. Independent conclusive instances remain usable."
	}
	if attempts == 0 && uncertain == 0 {
		f.Explanation += " No applicable verification evidence was recorded."
	}
	f.Explanation += " Recognition is limited to documented standalone Go test, vet, and build forms. Declared scope does not establish file participation, coverage, or correctness; build success does not establish test success."
	if omitted > 0 {
		f.Explanation += fmt.Sprintf(" Representative event links only; %d evaluated evidence references omitted. The canonical timeline retains the full evidence.", omitted)
	}
	return f, nil
}
