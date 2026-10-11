package analysis_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/pooya79/AgentSession/internal/analysis"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestMissingVerificationConfigurationScopes(t *testing.T) {
	zero := 0
	change := func(file string, position int64) analysis.ChangeEvidence {
		return analysis.ChangeEvidence{EventID: model.EventID(file), Position: position, Status: model.ChangeConfirmed, Path: file, Directory: "/workspace", Operation: model.FileMutationUpdate}
	}
	mod, sum := change("go.mod", 3), change("go.sum", 4)
	check := func(text, dir string, start, end int64) analysis.CommandEvidence {
		c := command(int(start), &zero)
		c.Command, c.Directory, c.Start, c.Completion = text, dir, start, end
		return c
	}
	narrow := check("go test ./internal/core", "/workspace", 6, 7)
	whole := check("go test ./...", "/workspace", 6, 7)
	deleted := mod
	deleted.Operation = model.FileMutationDelete
	requested := mod
	requested.Status = model.ChangeRequested
	pending := whole
	pending.ExitCode = nil
	pending.ResultEvent = ""
	later := mod
	later.Position = 5
	nested := change("nested/go.mod", 2)
	tests := []struct {
		name     string
		changes  []analysis.ChangeEvidence
		commands []analysis.CommandEvidence
		state    model.FindingState
	}{
		{"module missing", []analysis.ChangeEvidence{mod}, nil, model.FindingTriggered},
		{"module narrow", []analysis.ChangeEvidence{mod}, []analysis.CommandEvidence{narrow}, model.FindingTriggered},
		{"module root package only", []analysis.ChangeEvidence{mod}, []analysis.CommandEvidence{check("go test .", "/workspace", 6, 7)}, model.FindingTriggered},
		{"module whole", []analysis.ChangeEvidence{mod}, []analysis.CommandEvidence{whole}, model.FindingNotTriggered},
		{"module whole vet", []analysis.ChangeEvidence{mod}, []analysis.CommandEvidence{check("go vet ./...", "/workspace", 6, 7)}, model.FindingNotTriggered},
		{"module whole build", []analysis.ChangeEvidence{mod}, []analysis.CommandEvidence{check("go build ./...", "/workspace", 6, 7)}, model.FindingNotTriggered},
		{"whole target from parent", []analysis.ChangeEvidence{mod}, []analysis.CommandEvidence{check("go test ./workspace/...", "/", 6, 7)}, model.FindingNotTriggered},
		{"ancestor recursive excludes module", []analysis.ChangeEvidence{mod}, []analysis.CommandEvidence{check("go test ./...", "/", 6, 7)}, model.FindingTriggered},
		{"module subdirectory recursive", []analysis.ChangeEvidence{mod}, []analysis.CommandEvidence{check("go test ./...", "/workspace/internal", 6, 7)}, model.FindingTriggered},
		{"module pending", []analysis.ChangeEvidence{mod}, []analysis.CommandEvidence{pending}, model.FindingInsufficientEvidence},
		{"module check overlapping", []analysis.ChangeEvidence{mod}, []analysis.CommandEvidence{check("go test ./...", "/workspace", 1, 7)}, model.FindingTriggered},
		{"sum with established module", []analysis.ChangeEvidence{mod, sum}, []analysis.CommandEvidence{whole}, model.FindingNotTriggered},
		{"sum working directory insufficient", []analysis.ChangeEvidence{sum}, []analysis.CommandEvidence{whole}, model.FindingInsufficientEvidence},
		{"sum later module marker insufficient", []analysis.ChangeEvidence{sum, later}, []analysis.CommandEvidence{whole}, model.FindingInsufficientEvidence},
		{"sum requested marker insufficient", []analysis.ChangeEvidence{requested, sum}, []analysis.CommandEvidence{whole}, model.FindingInsufficientEvidence},
		{"deleted module uncertain", []analysis.ChangeEvidence{deleted}, []analysis.CommandEvidence{whole}, model.FindingInsufficientEvidence},
		{"workspace uncertain", []analysis.ChangeEvidence{change("go.work", 3)}, []analysis.CommandEvidence{whole}, model.FindingInsufficientEvidence},
		{"workspace sum uncertain", []analysis.ChangeEvidence{change("go.work.sum", 3)}, []analysis.CommandEvidence{whole}, model.FindingInsufficientEvidence},
		{"workspace membership unknown", []analysis.ChangeEvidence{change("go.work", 2), mod}, []analysis.CommandEvidence{whole}, model.FindingInsufficientEvidence},
		{"nested modules uncertain", []analysis.ChangeEvidence{nested, mod}, []analysis.CommandEvidence{whole}, model.FindingInsufficientEvidence},
		{"unrelated modules independent", []analysis.ChangeEvidence{change("/other/go.mod", 2), mod}, []analysis.CommandEvidence{whole, check("go vet ./...", "/other", 8, 9)}, model.FindingNotTriggered},
		{"mixed independent code missing", []analysis.ChangeEvidence{change("go.work", 2), change("internal/core/core.go", 3)}, nil, model.FindingTriggered},
		{"docs exempt", []analysis.ChangeEvidence{mod, change("README.md", 4)}, []analysis.CommandEvidence{whole}, model.FindingNotTriggered},
		{"known nested source exclusion", []analysis.ChangeEvidence{nested, change("nested/core.go", 3)}, []analysis.CommandEvidence{whole, check("go test ./nested/...", "/workspace", 6, 7)}, model.FindingNotTriggered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := analysis.AssessMissingVerification(context.Background(), "session", changeEvidence{evidence: evidence{commands: tt.commands}, changes: tt.changes})
			if err == nil {
				if err := got.Validate(); err != nil {
					t.Fatal(err)
				}
			}
			again, _ := analysis.AssessMissingVerification(context.Background(), "session", changeEvidence{evidence: evidence{commands: tt.commands}, changes: tt.changes})
			if !reflect.DeepEqual(got, again) {
				t.Fatal("unstable configuration assessment")
			}
			if err != nil || got.State != tt.state {
				t.Fatalf("state = %s, want %s (%v): %s", got.State, tt.state, err, got.Explanation)
			}
		})
	}
}

func TestMissingVerificationDoesNotCrossRecordedModuleBoundary(t *testing.T) {
	zero := 0
	input := changeEvidence{changes: []analysis.ChangeEvidence{
		{EventID: "module", Position: 1, Status: model.ChangeConfirmed, Path: "nested/go.mod", Directory: "/workspace", Operation: model.FileMutationUpdate},
		{EventID: "edit", Position: 2, Status: model.ChangeConfirmed, Path: "nested/core.go", Directory: "/workspace", Operation: model.FileMutationUpdate},
	}}
	check := command(3, &zero)
	check.Command = "go test ./..."
	input.commands = []analysis.CommandEvidence{check}
	got, err := analysis.AssessMissingVerification(context.Background(), "session", input)
	if err != nil || got.Metadata["verified_changes"] != "0" || got.Metadata["uncertainties"] != "1" {
		t.Fatalf("cross-module target = %#v, %v", got, err)
	}
}
