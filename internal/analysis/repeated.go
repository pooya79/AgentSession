// Package analysis assesses deterministic rules over source-neutral evidence.
package analysis

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"github.com/pooya79/AgentSession/internal/model"
)

const (
	RepeatedCommandRule   = "repeated_command_failures"
	RuleVersion           = "1"
	MaxEvidenceReferences = 12
	// Canonical command evidence is retained separately from arbitrary output.
	MaxCommandBytes    = 16 * 1024
	MaxDirectoryBytes  = 4 * 1024
	MaxInvocationBytes = 512
)

// CommandEvidence represents one distinct invocation after all linked results
// have been considered. Conflicting or missing results have a nil ExitCode.
// Readers walk in (command text, directory, source start order), with text
// trimmed only at the ends. GapBefore denotes uncertainty between this start
// and the previous same-identity invocation. Output prose is never required.
type CommandEvidence struct {
	Command, Directory      string
	StartEvent, ResultEvent model.EventID
	Start, Completion       int64
	ExitCode                *int
	GapBefore               bool
	LaterSuccess            CommandSuccess
	// RecoveryUncertain means a compatible interpretation gap or unknown-result
	// invocation after Completion could hide recovery. Readers compute this over
	// all evidence, independently of selected example links.
	RecoveryUncertain bool
}

// CommandSuccess links the first established successful invocation of the same
// established identity whose start follows established Completion, even when
// the earlier exit code is unknown. Empty event IDs mean none exists.
// Readers establish this relation across the full record, before sampling.
type CommandSuccess struct {
	StartEvent, ResultEvent model.EventID
	Start                   int64
}

// CommandCoverage describes interpretation or identity gaps, including gaps
// outside a pair, with bounded representative links and their full count.
type CommandCoverage struct {
	Gaps        int64
	EventIDs    []model.EventID
	TotalEvents int64
}

// CommandReader streams bounded invocation evidence and returns its coverage.
type CommandReader interface {
	WalkCommands(context.Context, model.SessionID, func(CommandEvidence) error) (CommandCoverage, error)
}

// AssessRepeatedFailures evaluates every invocation with constant working
// memory and bounded examples. A historical trigger survives later recovery.
func AssessRepeatedFailures(ctx context.Context, session model.SessionID, reader CommandReader) (model.Finding, error) {
	f := model.Finding{ID: model.FindingID(fmt.Sprintf("finding_%x", sha256.Sum256([]byte("agentsession:assessment:v1\x00"+string(session)+"\x00"+RepeatedCommandRule)))), SessionID: session, RuleID: RepeatedCommandRule, RuleVersion: RuleVersion}
	var attempts, failures, sequences, recovered, uncertain int64
	var previous CommandEvidence
	var havePrevious, inSequence bool
	var lastRepeatedCompletion int64
	var recovery CommandSuccess
	var groupCommand, groupDirectory string
	var haveGroup bool
	var fallback []model.EventID
	var fallbackTotal int64
	add := func(ids ...model.EventID) {
		for _, id := range ids {
			if id == "" {
				continue
			}
			if len(f.EventIDs) < MaxEvidenceReferences {
				duplicate := false
				for _, seen := range f.EventIDs {
					if seen == id {
						duplicate = true
						break
					}
				}
				if !duplicate {
					f.EventIDs = append(f.EventIDs, id)
				}
			}
		}
	}
	finishSequence := func() {
		if inSequence && recovery.StartEvent != "" && recovery.Start > lastRepeatedCompletion {
			recovered++
			add(recovery.StartEvent, recovery.ResultEvent)
		}
		inSequence = false
	}
	coverage, err := reader.WalkCommands(ctx, session, func(c CommandEvidence) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		attempts++
		c.Command = strings.TrimSpace(c.Command)
		for _, id := range []model.EventID{c.StartEvent, c.ResultEvent} {
			if id != "" {
				fallbackTotal++
				if len(fallback) < MaxEvidenceReferences {
					fallback = append(fallback, id)
				}
			}
		}
		same := haveGroup && c.Command == groupCommand && c.Directory == groupDirectory
		if !same {
			finishSequence()
			havePrevious = false
			groupCommand, groupDirectory = c.Command, c.Directory
			haveGroup = true
		}
		if c.ExitCode == nil || c.Command == "" || c.Directory == "" {
			uncertain++
			finishSequence()
			havePrevious = false
			return nil
		}
		if *c.ExitCode != 0 {
			failures++
		}
		separated := !c.GapBefore
		if !separated {
			uncertain++
			finishSequence()
		}
		if *c.ExitCode == 0 {
			finishSequence()
		} else if havePrevious && separated && previous.ExitCode != nil && *previous.ExitCode != 0 {
			if !inSequence {
				sequences++
				lastRepeatedCompletion = previous.Completion
				recovery = previous.LaterSuccess
				add(previous.StartEvent, previous.ResultEvent)
				inSequence = true
			}
			add(c.StartEvent, c.ResultEvent)
			if c.Completion > lastRepeatedCompletion {
				lastRepeatedCompletion = c.Completion
				recovery = c.LaterSuccess
			}
		} else {
			inSequence = false
		}
		previous = c
		havePrevious = true
		return nil
	})
	if err != nil {
		return model.Finding{}, fmt.Errorf("assess repeated commands: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return model.Finding{}, err
	}
	finishSequence()
	uncertain += coverage.Gaps
	switch {
	case sequences > 0:
		f.State = model.FindingTriggered
	case uncertain > 0:
		f.State = model.FindingInsufficientEvidence
	case attempts > 0:
		f.State = model.FindingNotTriggered
	default:
		f.State = model.FindingNotApplicable
	}
	if sequences == 0 {
		f.EventIDs = fallback
	}
	for _, id := range coverage.EventIDs {
		if len(f.EventIDs) < MaxEvidenceReferences {
			f.EventIDs = append(f.EventIDs, id)
		}
	}
	omitted := fallbackTotal + coverage.TotalEvents - int64(len(f.EventIDs))
	f.Metadata = map[string]string{"invocations": strconv.FormatInt(attempts, 10), "failures": strconv.FormatInt(failures, 10), "sequences": strconv.FormatInt(sequences, 10), "recovered": strconv.FormatInt(recovered, 10), "uncertainties": strconv.FormatInt(uncertain, 10), "omitted_references": strconv.FormatInt(omitted, 10)}
	f.Explanation = fmt.Sprintf("Within the currently imported record in source order: %d invocations, %d failed results, %d repeated failure sequences, %d later recorded recoveries. Command identity requires exact recorded text trimmed at the ends and an established matching directory.", attempts, failures, sequences, recovered)
	if uncertain > 0 {
		f.Explanation += " Missing, conflicting, unsupported, or oversized evidence limits affected sequences; independent positive evidence remains usable."
	}
	if attempts == 0 && uncertain == 0 {
		f.Explanation += " No applicable command evidence was recorded."
	}
	if omitted > 0 {
		f.Explanation += fmt.Sprintf(" Representative event links only; %d evaluated evidence references omitted. The canonical timeline retains the full evidence.", omitted)
	}
	return f, nil
}
