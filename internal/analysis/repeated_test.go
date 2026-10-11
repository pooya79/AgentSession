package analysis_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/analysis"
	"github.com/pooya79/AgentSession/internal/model"
)

type evidence struct {
	commands []analysis.CommandEvidence
	gaps     int64
}

func (e evidence) WalkCommands(ctx context.Context, _ model.SessionID, visit func(analysis.CommandEvidence) error) (analysis.CommandCoverage, error) {
	for _, c := range e.commands {
		if err := ctx.Err(); err != nil {
			return analysis.CommandCoverage{}, err
		}
		for _, next := range e.commands {
			if next.StartEvent != "" && next.ResultEvent != "" && next.Completion > next.Start && next.ExitCode != nil && *next.ExitCode == 0 && strings.TrimSpace(next.Command) == strings.TrimSpace(c.Command) && next.Directory == c.Directory && next.Start > c.Completion && (c.LaterSuccess.StartEvent == "" || next.Start < c.LaterSuccess.Start) {
				c.LaterSuccess = analysis.CommandSuccess{StartEvent: next.StartEvent, ResultEvent: next.ResultEvent, Start: next.Start}
			}
		}
		if err := visit(c); err != nil {
			return analysis.CommandCoverage{}, err
		}
	}
	return analysis.CommandCoverage{Gaps: e.gaps}, nil
}
func command(n int, exit *int) analysis.CommandEvidence {
	return analysis.CommandEvidence{Command: "go test ./...", Directory: "/workspace", StartEvent: model.EventID(fmt.Sprintf("start%d", n)), ResultEvent: model.EventID(fmt.Sprintf("result%d", n)), Start: int64(n * 3), Completion: int64(n*3 + 1), ExitCode: exit}
}
func TestRepeatedFailures(t *testing.T) {
	fail, success := 1, 0
	a, b, c := command(0, &fail), command(1, &fail), command(2, &success)
	unknown := command(1, nil)
	gap := b
	gap.GapBefore = true
	different := b
	different.Command = "go  test ./..."
	dir := b
	dir.Directory = "/elsewhere"
	overlap := b
	overlap.Start = a.Completion
	lateFailure := a
	lateFailure.Completion = 100
	tests := []struct {
		name                   string
		input                  evidence
		state                  model.FindingState
		repetitions, recovered string
	}{
		{"empty", evidence{}, model.FindingNotApplicable, "0", "0"},
		{"single", evidence{commands: []analysis.CommandEvidence{a}}, model.FindingNotTriggered, "0", "0"},
		{"repeat", evidence{commands: []analysis.CommandEvidence{a, b}}, model.FindingTriggered, "1", "0"},
		{"parallel success is not recovery", evidence{commands: []analysis.CommandEvidence{a, b, {Command: b.Command, Directory: b.Directory, StartEvent: "parallel", ResultEvent: "parallel-result", Start: b.Start + 1, Completion: b.Completion + 1, ExitCode: &success}}}, model.FindingTriggered, "1", "0"},
		{"recovery after unknown", evidence{commands: []analysis.CommandEvidence{a, b, command(2, nil), command(3, &success)}}, model.FindingTriggered, "1", "1"},
		{"recovery", evidence{commands: []analysis.CommandEvidence{a, b, c}}, model.FindingTriggered, "1", "1"},
		{"independent recovery after unknown", evidence{commands: []analysis.CommandEvidence{lateFailure, b, command(2, nil), command(3, &fail), command(4, &fail), command(5, &success)}}, model.FindingTriggered, "2", "1"},
		{"independent recovery after parallel success", evidence{commands: []analysis.CommandEvidence{lateFailure, b, command(2, &success), command(3, &fail), command(4, &fail), command(5, &success)}}, model.FindingTriggered, "2", "1"},
		{"later success recovers both sequences", evidence{commands: []analysis.CommandEvidence{lateFailure, b, command(2, nil), command(3, &fail), command(4, &fail), command(34, &success)}}, model.FindingTriggered, "2", "2"},
		{"intervening success", evidence{commands: []analysis.CommandEvidence{a, command(1, &success), command(2, &fail)}}, model.FindingNotTriggered, "0", "0"},
		{"intervening unknown", evidence{commands: []analysis.CommandEvidence{a, unknown, command(2, &fail)}}, model.FindingInsufficientEvidence, "0", "0"},
		{"internal whitespace", evidence{commands: []analysis.CommandEvidence{a, different}}, model.FindingNotTriggered, "0", "0"},
		{"different directory", evidence{commands: []analysis.CommandEvidence{a, dir}}, model.FindingNotTriggered, "0", "0"},
		{"relevant gap", evidence{commands: []analysis.CommandEvidence{a, gap}, gaps: 1}, model.FindingInsufficientEvidence, "0", "0"},
		{"unrelated gap", evidence{commands: []analysis.CommandEvidence{a, b}, gaps: 1}, model.FindingTriggered, "1", "0"},
		{"overlap", evidence{commands: []analysis.CommandEvidence{a, overlap}}, model.FindingTriggered, "1", "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := analysis.AssessRepeatedFailures(context.Background(), "session", tt.input)
			if err != nil || got.State != tt.state || got.Metadata["sequences"] != tt.repetitions || got.Metadata["recovered"] != tt.recovered {
				t.Fatalf("assessment = %#v, %v", got, err)
			}
			if len(tt.input.commands) > 0 && len(got.EventIDs) == 0 {
				t.Fatal("applicable assessment omitted every evidence link")
			}
			if got.Explanation == "" || got.RuleVersion != "1" || got.Severity != "" {
				t.Fatalf("invalid assessment: %#v", got)
			}
			seen := map[model.EventID]bool{}
			for _, id := range got.EventIDs {
				if seen[id] {
					t.Fatalf("duplicate supporting link %s", id)
				}
				seen[id] = true
			}
			again, _ := analysis.AssessRepeatedFailures(context.Background(), "session", tt.input)
			if !reflect.DeepEqual(got, again) {
				t.Fatal("unstable rebuild")
			}
		})
	}
}
func TestRepeatedFailuresBoundsAndCancellation(t *testing.T) {
	fail := 1
	var commands []analysis.CommandEvidence
	for i := 0; i < 1000; i++ {
		commands = append(commands, command(i, &fail))
	}
	got, err := analysis.AssessRepeatedFailures(context.Background(), "session", evidence{commands: commands})
	if err != nil || got.State != model.FindingTriggered || got.Metadata["failures"] != "1000" || len(got.EventIDs) > analysis.MaxEvidenceReferences || !strings.Contains(got.Explanation, "omitted") {
		t.Fatalf("bounded assessment = %#v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := analysis.AssessRepeatedFailures(ctx, "session", evidence{commands: commands}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestTriggerBeyondRepresentativeExamplesIsStillEvaluated(t *testing.T) {
	success, fail := 0, 1
	commands := []analysis.CommandEvidence{}
	for i := 0; i < 1000; i++ {
		commands = append(commands, command(i, &success))
	}
	commands = append(commands, command(1000, &fail), command(1001, &fail))
	got, err := analysis.AssessRepeatedFailures(context.Background(), "session", evidence{commands: commands})
	if err != nil || got.State != model.FindingTriggered || got.Metadata["invocations"] != "1002" || got.Metadata["failures"] != "2" || len(got.EventIDs) != 4 || got.EventIDs[0] != "start1000" {
		t.Fatalf("late trigger=%#v, %v", got, err)
	}
}
