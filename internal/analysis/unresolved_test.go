package analysis_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/analysis"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestUnresolvedVerificationFailure(t *testing.T) {
	fail := 1
	got, err := analysis.AssessUnresolvedVerification(context.Background(), "session", evidence{commands: []analysis.CommandEvidence{command(0, &fail)}})
	if err != nil || got.State != model.FindingTriggered || got.Metadata["unresolved"] != "1" || got.Metadata["tests"] != "1" {
		t.Fatalf("assessment = %#v, %v", got, err)
	}
}

func TestUnresolvedVerificationStates(t *testing.T) {
	fail, success := 1, 0
	a, b, c := command(0, &fail), command(1, &success), command(2, &fail)
	parallel := b
	parallel.Start = a.Start
	parallel.Completion = a.Completion + 1
	trimmed := a
	trimmed.Command = "  go test ./... \n"
	gap := a
	gap.RecoveryUncertain = true
	missingEnd := a
	missingEnd.ResultEvent = ""
	missingStart := a
	missingStart.StartEvent = ""
	unordered := a
	unordered.Completion = unordered.Start
	vet := b
	vet.Command = "go vet ./..."
	build := b
	build.Command = "go build ./..."
	dir := b
	dir.Directory = "/elsewhere"
	text := b
	text.Command = "go  test ./..."
	unsupported := a
	unsupported.Command = "go test -run TestFoo ./..."
	noDir := a
	noDir.Directory = ""
	tests := []struct {
		name                  string
		input                 evidence
		state                 model.FindingState
		unresolved, recovered string
	}{
		{"empty", evidence{}, model.FindingNotApplicable, "0", "0"},
		{"success", evidence{commands: []analysis.CommandEvidence{b}}, model.FindingNotTriggered, "0", "0"},
		{"failure", evidence{commands: []analysis.CommandEvidence{a}}, model.FindingTriggered, "1", "0"},
		{"unknown result", evidence{commands: []analysis.CommandEvidence{command(0, nil)}}, model.FindingInsufficientEvidence, "0", "0"},
		{"unknown result before later success", evidence{commands: []analysis.CommandEvidence{command(0, nil), b}}, model.FindingNotTriggered, "0", "0"},
		{"unsupported", evidence{commands: []analysis.CommandEvidence{unsupported}}, model.FindingInsufficientEvidence, "0", "0"},
		{"unknown directory", evidence{commands: []analysis.CommandEvidence{noDir}}, model.FindingInsufficientEvidence, "0", "0"},
		{"missing end", evidence{commands: []analysis.CommandEvidence{missingEnd}}, model.FindingInsufficientEvidence, "0", "0"},
		{"missing start", evidence{commands: []analysis.CommandEvidence{missingStart}}, model.FindingInsufficientEvidence, "0", "0"},
		{"invalid relationship", evidence{commands: []analysis.CommandEvidence{unordered}}, model.FindingInsufficientEvidence, "0", "0"},
		{"trim surrounding whitespace", evidence{commands: []analysis.CommandEvidence{trimmed, b}}, model.FindingNotTriggered, "0", "1"},
		{"recovery", evidence{commands: []analysis.CommandEvidence{a, b}}, model.FindingNotTriggered, "0", "1"},
		{"reopening", evidence{commands: []analysis.CommandEvidence{a, b, c}}, model.FindingTriggered, "1", "1"},
		{"parallel", evidence{commands: []analysis.CommandEvidence{a, parallel}}, model.FindingTriggered, "1", "0"},
		{"static check cannot recover test", evidence{commands: []analysis.CommandEvidence{a, vet}}, model.FindingTriggered, "1", "0"},
		{"build cannot recover test", evidence{commands: []analysis.CommandEvidence{a, build}}, model.FindingTriggered, "1", "0"},
		{"directory differs", evidence{commands: []analysis.CommandEvidence{a, dir}}, model.FindingTriggered, "1", "0"},
		{"text differs", evidence{commands: []analysis.CommandEvidence{a, text}}, model.FindingTriggered, "1", "0"},
		{"possible recovery", evidence{commands: []analysis.CommandEvidence{gap}}, model.FindingInsufficientEvidence, "0", "0"},
		{"explicit recovery survives gap", evidence{commands: []analysis.CommandEvidence{gap, b}}, model.FindingNotTriggered, "0", "1"},
		{"independent trigger wins", evidence{commands: []analysis.CommandEvidence{gap, c}, gaps: 1}, model.FindingTriggered, "1", "0"},
		{"gap alone", evidence{gaps: 1}, model.FindingInsufficientEvidence, "0", "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := analysis.AssessUnresolvedVerification(context.Background(), "session", tt.input)
			if err != nil || got.State != tt.state || got.Metadata["unresolved"] != tt.unresolved || got.Metadata["recovered"] != tt.recovered {
				t.Fatalf("assessment = %#v, %v", got, err)
			}
			if err := got.Validate(); err != nil {
				t.Fatal(err)
			}
			again, err := analysis.AssessUnresolvedVerification(context.Background(), "session", tt.input)
			if err != nil || !reflect.DeepEqual(got, again) {
				t.Fatal("unstable assessment")
			}
			if got.RuleID != analysis.UnresolvedVerificationRule || got.RuleVersion != "1" || got.Severity != "" {
				t.Fatalf("identity = %#v", got)
			}
		})
	}
}

func TestUnresolvedVerificationBoundsAndCancellation(t *testing.T) {
	fail, success := 1, 0
	commands := make([]analysis.CommandEvidence, 1002)
	for i := range 1000 {
		commands[i] = command(i, &success)
	}
	commands[1000], commands[1001] = command(1000, &fail), command(1001, &fail)
	got, err := analysis.AssessUnresolvedVerification(context.Background(), "session", evidence{commands: commands})
	if err != nil || got.State != model.FindingTriggered || got.Metadata["invocations"] != "1002" || got.Metadata["unresolved"] != "2" || got.Metadata["omitted_references"] != "1992" || len(got.EventIDs) != analysis.MaxEvidenceReferences || got.EventIDs[0] != "start1000" || !strings.Contains(got.Explanation, "Representative event links only") {
		t.Fatalf("bounded = %#v, %v", got, err)
	}
	again, _ := analysis.AssessUnresolvedVerification(context.Background(), "session", evidence{commands: commands})
	if !reflect.DeepEqual(got, again) {
		t.Fatal("unstable bounded output")
	}
	short, _ := analysis.AssessUnresolvedVerification(context.Background(), "session", evidence{commands: commands[1000:]})
	if got.ID != short.ID {
		t.Fatal("identity depends on sampled evidence")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := analysis.AssessUnresolvedVerification(ctx, "session", evidence{commands: commands}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}
